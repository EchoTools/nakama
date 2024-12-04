package server

import (
	"context"
	"database/sql"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"

	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/intinig/go-openskill/rating"
	"github.com/intinig/go-openskill/types"
	"github.com/samber/lo"
	"go.uber.org/atomic"
)

const (
	MaximumRankDelta  = 0.10
	RTTPropertyPrefix = "rtt_"
)

type SkillBasedMatchmaker struct {
	writeMu          *sync.RWMutex
	latestCandidates *atomic.Value // [][]runtime.MatchmakerEntry
	latestMatches    *atomic.Value // [][]runtime.MatchmakerEntry
}

func (s *SkillBasedMatchmaker) StoreLatestResult(candidates, madeMatches [][]runtime.MatchmakerEntry) {
	s.writeMu.Lock()
	s.latestCandidates.Store(candidates)
	s.latestMatches.Store(madeMatches)
	s.writeMu.Unlock()
}

func (s *SkillBasedMatchmaker) GetLatestResult() (candidates, madeMatches [][]runtime.MatchmakerEntry) {
	var ok bool
	candidates, ok = s.latestCandidates.Load().([][]runtime.MatchmakerEntry)
	if !ok {
		return
	}
	madeMatches, ok = s.latestMatches.Load().([][]runtime.MatchmakerEntry)
	if !ok {
		return candidates, nil
	}
	return
}

func NewSkillBasedMatchmaker() *SkillBasedMatchmaker {
	sbmm := SkillBasedMatchmaker{
		writeMu:          &sync.RWMutex{},
		latestCandidates: &atomic.Value{},
		latestMatches:    &atomic.Value{},
	}

	sbmm.latestCandidates.Store([][]runtime.MatchmakerEntry{})
	sbmm.latestMatches.Store([][]runtime.MatchmakerEntry{})
	return &sbmm
}

// Function to be used as a matchmaker function in Nakama (RegisterMatchmakerOverride)
func (m *SkillBasedMatchmaker) EvrMatchmakerFn(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, candidates [][]runtime.MatchmakerEntry) [][]runtime.MatchmakerEntry {

	if len(candidates) == 0 || len(candidates[0]) == 0 {
		logger.Error("No candidates found. Matchmaker cannot run.")
		return nil
	}

	groupID, ok := candidates[0][0].GetProperties()["group_id"].(string)
	if !ok || groupID == "" {
		logger.Error("Group ID not found in entry properties.")
		return nil
	}

	modestr, ok := candidates[0][0].GetProperties()["game_mode"].(string)
	if !ok || modestr == "" {
		logger.Error("Mode not found in entry properties. Matchmaker cannot run.")
		return nil
	}

	// Extract all players from the candidates
	allPlayers := make(map[string]struct{}, 0)
	for _, c := range candidates {
		for _, e := range c {
			allPlayers[e.GetPresence().GetUserId()] = struct{}{}
		}
	}
	matches, filterCounts := m.processPotentialMatches(candidates)

	// Extract all players from the matches
	matchedPlayerMap := make(map[string]struct{}, 0)
	for _, c := range matches {
		for _, e := range c {
			matchedPlayerMap[e.GetPresence().GetUserId()] = struct{}{}
		}
	}

	matchedPlayers := lo.Keys(matchedPlayerMap)

	// Create a list of excluded players
	unmatchedPlayers := lo.FilterMap(lo.Keys(allPlayers), func(p string, _ int) (string, bool) {
		_, ok := matchedPlayerMap[p]
		return p, !ok
	})

	logger.WithFields(map[string]interface{}{
		"mode":                modestr,
		"num_player_total":    len(allPlayers),
		"num_player_included": len(matchedPlayers),
		"num_match_options":   len(candidates),
		"num_match_made":      len(matches),
		"made_matches":        matches,
		"filter_counts":       filterCounts,
		"matched_players":     matchedPlayerMap,
		"unmatched_players":   unmatchedPlayers,
	}).Info("Skill-based matchmaker completed.")

	m.StoreLatestResult(candidates, matches)

	return matches
}

func (m *SkillBasedMatchmaker) processPotentialMatches(candidates [][]runtime.MatchmakerEntry) ([][]runtime.MatchmakerEntry, map[string]int) {

	filterCounts := make(map[string]int)

	// Remove identical matches
	candidates, filterCounts["duplicate"] = m.filterDuplicates(candidates)

	// Remove odd sized teams
	candidates, filterCounts["odd_size"] = m.filterOddSizedTeams(candidates)

	// Ensure that everyone in the match is within their max_rtt of a common server
	candidates, filterCounts["no_matching_servers"] = m.filterWithinMaxRTT(candidates)

	// Create a list of balanced matches with predictions
	predictions := m.predictOutcomes(candidates)

	m.sortByDraw(predictions)
	m.sortLimitRankSpread(predictions, MaximumRankDelta)
	m.sortBySize(predictions)
	//m.sortPriority(predictions)

	madeMatches := m.assembleUniqueMatches(predictions)

	return madeMatches, filterCounts
}

func (m *SkillBasedMatchmaker) filterDuplicates(candidates [][]runtime.MatchmakerEntry) ([][]runtime.MatchmakerEntry, int) {
	// Filter out the duplicates
	seen := make(map[string]struct{})
	count := 0
	for i := 0; i < len(candidates); i++ {

		sort.Slice(candidates[i], func(k, l int) bool {
			return candidates[i][k].GetPresence().GetUserId() < candidates[i][l].GetPresence().GetUserId()
		})

		key := ""
		for _, e := range candidates[i] {
			key += e.GetPresence().GetUserId()
		}

		if _, ok := seen[key]; ok {
			count++
			candidates = append(candidates[:i], candidates[i+1:]...)
			i--
		} else {
			seen[key] = struct{}{}
		}
	}
	return candidates, count
}

func (m *SkillBasedMatchmaker) filterOddSizedTeams(candidates [][]runtime.MatchmakerEntry) ([][]runtime.MatchmakerEntry, int) {
	oddSizedCount := 0
	for i := 0; i < len(candidates); i++ {
		if len(candidates[i])%2 != 0 {
			oddSizedCount++
			candidates = append(candidates[:i], candidates[i+1:]...)
			i--
		}
	}
	return candidates, oddSizedCount
}

// Ensure that everyone in the match is within their max_rtt of a common server
func (m *SkillBasedMatchmaker) filterWithinMaxRTT(candidates [][]runtime.MatchmakerEntry) ([][]runtime.MatchmakerEntry, int) {

	var filteredCount int
	for i := 0; i < len(candidates); i++ {

		serverRTTs := make(map[string][]float64)

		for _, entry := range candidates[i] {

			maxRTT := 500.0
			if rtt, ok := entry.GetProperties()["max_rtt"].(float64); ok && rtt > 0 {
				maxRTT = rtt
			}

			for k, v := range entry.GetProperties() {

				if !strings.HasPrefix(k, RTTPropertyPrefix) {
					continue
				}

				if v.(float64) > maxRTT {
					// Server is too far away from this player
					continue
				}

				serverRTTs[k] = append(serverRTTs[k], v.(float64))
			}
		}

		for k, rtts := range serverRTTs {
			if len(rtts) != len(candidates[i]) {
				// Server is unreachable to one or more players
				delete(serverRTTs, k)
			}
		}

		if len(serverRTTs) == 0 {
			// No common servers for players
			candidates = append(candidates[:i], candidates[i+1:]...)
			i--
			filteredCount++
		}
	}
	return candidates, filteredCount
}

func (*SkillBasedMatchmaker) teamStrength(team RatedEntryTeam) float64 {
	s := 0.0
	for _, p := range team {
		s += p.Rating.Mu
	}
	return s
}

func (*SkillBasedMatchmaker) predictDraw(teams []RatedEntryTeam) float64 {
	team1 := make(types.Team, 0, len(teams[0]))
	team2 := make(types.Team, 0, len(teams[1]))
	for _, e := range teams[0] {
		team1 = append(team1, e.Rating)
	}
	for _, e := range teams[1] {
		team2 = append(team2, e.Rating)
	}
	return rating.PredictDraw([]types.Team{team1, team2}, nil)
}

func (m *SkillBasedMatchmaker) sortPriority(predictions []PredictedMatch) {
	now := float64(time.Now().UTC().Unix())

	slices.SortStableFunc(predictions, func(a, b PredictedMatch) int {
		// If a player has a priority_threshold set, and it's less than "now" it should be sorted to the top
		for _, o := range []PredictedMatch{a, b} {
			for _, team := range o.Teams() {
				for _, player := range team {
					if p, ok := player.Entry.GetProperties()["priority_threshold"].(float64); ok && p < now {
						return -1
					}
				}
			}
		}
		return 0
	})
}

func (m *SkillBasedMatchmaker) sortLimitRankSpread(predictions []PredictedMatch, maximumRankSpread float64) {
	slices.SortStableFunc(predictions, func(a, b PredictedMatch) int {
		rankTeamAvgs := make([][]float64, 2)

		for i, o := range []PredictedMatch{a, b} {
			for j, team := range o.Teams() {
				rankTeamAvgs[i] = append(rankTeamAvgs[i], 0)
				for _, player := range team {
					rankTeamAvgs[i][j] += player.Entry.GetProperties()["rank_percentile"].(float64)
				}
				rankTeamAvgs[i][j] /= float64(len(team))
			}
		}

		// get the delta between the two teams
		rankSpreadA := math.Abs(rankTeamAvgs[0][0] - rankTeamAvgs[0][1])
		rankSpreadB := math.Abs(rankTeamAvgs[1][0] - rankTeamAvgs[1][1])

		if math.Abs(rankSpreadA) < maximumRankSpread && math.Abs(rankSpreadB) > maximumRankSpread {
			return -1
		} else if math.Abs(rankSpreadA) > maximumRankSpread && math.Abs(rankSpreadB) < maximumRankSpread {
			return 1
		}

		return 0
	})
}

func (m *SkillBasedMatchmaker) sortBySize(predictions []PredictedMatch) {
	// Sort by size, then by prediction of a draw
	slices.SortStableFunc(predictions, func(a, b PredictedMatch) int {
		if len(a.Entrants()) > len(b.Entrants()) {
			return -1
		} else if len(a.Entrants()) < len(b.Entrants()) {
			return 1
		}

		return 0
	})
}

func (m *SkillBasedMatchmaker) sortByDraw(predictions []PredictedMatch) {
	slices.SortStableFunc(predictions, func(a, b PredictedMatch) int {
		if a.Draw > b.Draw {
			return -1
		}
		if a.Draw < b.Draw {
			return 1
		}
		return 0
	})
}

func (m *SkillBasedMatchmaker) createBalancedMatch(groups [][]*RatedEntry, teamSize int) (RatedEntryTeam, RatedEntryTeam) {
	// Split out the solo players

	team1 := make(RatedEntryTeam, 0, teamSize)
	team2 := make(RatedEntryTeam, 0, teamSize)

	// Sort the groups by party size, largest first.

	sort.Slice(groups, func(i, j int) bool {
		// first by party size
		if len(groups[i]) > len(groups[j]) {
			return true
		} else if len(groups[i]) < len(groups[j]) {
			return false
		}

		// Then by strength
		if m.teamStrength(groups[i]) > m.teamStrength(groups[j]) {
			return true
		} else if m.teamStrength(groups[i]) < m.teamStrength(groups[j]) {
			return false
		}

		return false
	})

	// Organize groups onto teams, balancing by strength
	for _, group := range groups {
		if len(team1)+len(group) <= teamSize && (len(team2)+len(group) > teamSize || m.teamStrength(team1) <= m.teamStrength(team2)) {
			team1 = append(team1, group...)
		} else if len(team2)+len(group) <= teamSize {
			team2 = append(team2, group...)
		}
	}

	// Sort the players on the team by their rating
	sort.Slice(team1, func(i, j int) bool {
		return team1[i].Rating.Mu > team1[j].Rating.Mu
	})
	sort.Slice(team2, func(i, j int) bool {
		return team2[i].Rating.Mu > team2[j].Rating.Mu
	})

	// Sort so that team1 (blue) is the stronger team
	if m.teamStrength(team1) < m.teamStrength(team2) {
		team1, team2 = team2, team1
	}

	return team1, team2
}

func (m *SkillBasedMatchmaker) balanceByTicket(candidate []runtime.MatchmakerEntry) RatedMatch {
	// Group based on ticket

	ticketMap := make(map[string][]*RatedEntry)
	for _, e := range candidate {
		ticketMap[e.GetTicket()] = append(ticketMap[e.GetTicket()], NewRatedEntryFromMatchmakerEntry(e))
	}

	byTicket := make([][]*RatedEntry, 0)
	for _, entries := range ticketMap {
		byTicket = append(byTicket, entries)
	}

	team1, team2 := m.createBalancedMatch(byTicket, len(candidate)/2)
	return RatedMatch{team1, team2}
}

func (m *SkillBasedMatchmaker) predictOutcomes(candidates [][]runtime.MatchmakerEntry) []PredictedMatch {
	predictions := make([]PredictedMatch, 0, len(candidates))
	for _, match := range candidates {
		ratedMatch := m.balanceByTicket(match)

		predictions = append(predictions, PredictedMatch{
			Team1: ratedMatch[0],
			Team2: ratedMatch[1],
			Draw:  m.predictDraw(ratedMatch),
		})
	}
	return predictions
}
func (m *SkillBasedMatchmaker) assembleUniqueMatches(ratedMatches []PredictedMatch) [][]runtime.MatchmakerEntry {
	seen := make(map[string]struct{})
	selected := make([][]runtime.MatchmakerEntry, 0, len(ratedMatches))

OuterLoop:
	for _, ratedMatch := range ratedMatches {
		// The players are ordered by their team
		match := make([]runtime.MatchmakerEntry, 0, 8)

		// Ensure no player is in more than one match
		for _, e := range ratedMatch.Entrants() {
			sessionID := e.Entry.GetPresence().GetSessionId()

			// Skip match with players already in a match
			if _, ok := seen[sessionID]; ok {
				continue OuterLoop
			}
			seen[sessionID] = struct{}{}
			match = append(match, e.Entry)
		}

		selected = append(selected, match)
	}
	return selected
}
