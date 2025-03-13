package server

import (
	"context"
	"database/sql"
	"math"
	"slices"
	"sort"
	"strings"
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
	latestCandidates *atomic.Value // [][]runtime.MatchmakerEntry
	latestMatches    *atomic.Value // [][]runtime.MatchmakerEntry
}

func (s *SkillBasedMatchmaker) StoreLatestResult(candidates, madeMatches [][]runtime.MatchmakerEntry) {

	s.latestCandidates.Store(candidates)
	s.latestMatches.Store(madeMatches)

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
		latestCandidates: &atomic.Value{},
		latestMatches:    &atomic.Value{},
	}

	sbmm.latestCandidates.Store([][]runtime.MatchmakerEntry{})
	sbmm.latestMatches.Store([][]runtime.MatchmakerEntry{})
	return &sbmm
}

// Function to be used as a matchmaker function in Nakama (RegisterMatchmakerOverride)
func (m *SkillBasedMatchmaker) EvrMatchmakerFn(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, candidates [][]runtime.MatchmakerEntry) [][]runtime.MatchmakerEntry {

	startTime := time.Now()
	defer func() {
		nk.MetricsGaugeSet("matchmaker_evr_candidate_count", nil, float64(len(candidates)))

		// Divide the time by the number of candidates
		nk.MetricsTimerRecord("matchmaker_evr_per_candidate", nil, time.Since(startTime)/time.Duration(len(candidates)))
	}()

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

	var (
		matches        [][]runtime.MatchmakerEntry
		filterCounts   map[string]int
		originalCount  = len(candidates)
		globalSettings = ServiceSettings()
	)

	candidates, matches, filterCounts = m.processPotentialMatches(candidates, globalSettings)

	// Extract all players from the candidates
	playerSet := make(map[string]struct{}, 0)
	ticketSet := make(map[string]struct{}, len(candidates))
	for _, c := range candidates {
		for _, e := range c {
			ticketSet[e.GetTicket()] = struct{}{}
			playerSet[e.GetPresence().GetUserId()] = struct{}{}
		}
	}

	// Extract all players from the matches
	matchedPlayerSet := make(map[string]struct{}, 0)
	for _, c := range matches {
		for _, e := range c {
			matchedPlayerSet[e.GetPresence().GetUserId()] = struct{}{}
		}
	}

	matchedPlayers := lo.Keys(matchedPlayerSet)

	// Create a list of excluded players
	unmatchedPlayers := lo.FilterMap(lo.Keys(playerSet), func(p string, _ int) (string, bool) {
		_, ok := matchedPlayerSet[p]
		return p, !ok
	})

	logger.WithFields(map[string]interface{}{
		"mode":                 modestr,
		"num_player_total":     len(playerSet),
		"num_tickets":          len(ticketSet),
		"num_players_matched":  len(matchedPlayers),
		"num_match_candidates": originalCount,
		"num_matches_made":     len(matches),
		"filter_counts":        filterCounts,
		"matched_players":      matchedPlayerSet,
		"unmatched_players":    unmatchedPlayers,
		"duration":             time.Since(startTime),
	}).Info("Skill-based matchmaker completed.")

	if candidates != nil && matches != nil && len(candidates) > 0 {
		m.StoreLatestResult(candidates, matches)
	}

	return matches
}

func (m *SkillBasedMatchmaker) processPotentialMatches(candidates [][]runtime.MatchmakerEntry, globalSettings *GlobalSettingsData) ([][]runtime.MatchmakerEntry, [][]runtime.MatchmakerEntry, map[string]int) {

	// Write the candidates to a json filed called /tmp/candidates.json
	filterCounts := make(map[string]int)

	// Filter out duplicates
	filterCounts["duplicates"] = m.filterDuplicates(candidates)

	// Filter out odd-sized teams
	filterCounts["odd_sized_teams"] = m.filterOddSizedTeams(candidates)

	// Filter out players who are too far away from each other
	filterCounts["max_rtt"] = m.filterWithinMaxRTT(candidates)

	// Create a list of balanced matches with predictions
	predictions := m.predictOutcomes(candidates, globalSettings.Matchmaking.RankPercentile.Default)

	// Sort the predictions
	m.sortPredictions(predictions, globalSettings.Matchmaking.RankPercentile.MaxDelta, false)

	madeMatches := m.assembleUniqueMatches(predictions)

	return candidates, madeMatches, filterCounts
}

func (m *SkillBasedMatchmaker) filterDuplicates(candidates [][]runtime.MatchmakerEntry) int {
	seen := make(map[string]struct{}) // Map to track seen candidate combinations
	count := 0

	for i, candidate := range candidates {
		if candidate == nil {
			continue
		}

		// Create a key for the candidate based on the ticket IDs
		ticketIDs := make([]string, 0, len(candidate))
		for _, e := range candidate {
			ticketIDs = append(ticketIDs, e.GetTicket())
		}
		sort.Strings(ticketIDs)
		key := strings.Join(ticketIDs, "")

		// Check if the candidate has been seen before
		if _, ok := seen[key]; ok {
			count++
			candidates[i] = nil
			continue
		}

		// Mark the candidate as seen
		seen[key] = struct{}{}
	}

	return count // Return the count of duplicates and the filtered candidates
}

func (m *SkillBasedMatchmaker) filterOddSizedTeams(candidates [][]runtime.MatchmakerEntry) int {
	oddSizedCount := 0
	for i := 0; i < len(candidates); i++ {
		if candidates[i] == nil {
			continue
		}
		if len(candidates[i])%2 != 0 {
			oddSizedCount++
			candidates[i] = nil
			continue
		}

	}
	return oddSizedCount
}

// Ensure that everyone in the match is within their max_rtt of a common server
func (m *SkillBasedMatchmaker) filterWithinMaxRTT(candidates [][]runtime.MatchmakerEntry) int {

	var filteredCount int
OuterLoop:
	for i, candidate := range candidates {

		if candidate == nil {
			continue
		}

		var ok bool
		var maxRTT float64

		reachablePlayers := make(map[string]int)

		for _, entry := range candidate {

			if maxRTT, ok = entry.GetProperties()["max_rtt"].(float64); !ok || maxRTT <= 0 {
				maxRTT = 500.0
			}

			for k, v := range entry.GetProperties() {

				if !strings.HasPrefix(k, RTTPropertyPrefix) {
					continue
				}

				if v.(float64) > maxRTT {
					// Server is too far away from this player
					continue
				}

				reachablePlayers[k]++

				if reachablePlayers[k] == len(candidate) {
					continue OuterLoop
				}
			}
		}
		// Players have no common server
		candidates[i] = nil
		filteredCount++
	}

	return filteredCount
}

func (m *SkillBasedMatchmaker) createBalancedMatch(parties []*MatchmakingTicket, teamSize int) [2][]*MatchmakingTicket {

	// Sort the groups by party size, largest first, then by strength (descending)
	slices.SortStableFunc(parties, func(a, b *MatchmakingTicket) int {
		if a.Length != b.Length {
			return b.Length - a.Length
		}
		return int(b.Strength - a.Strength)
	})

	teamA := make([]*MatchmakingTicket, 0, teamSize)
	teamB := make([]*MatchmakingTicket, 0, teamSize)

	var lenA, lenB int
	// Organize groups onto teams, balancing by strength
	var strengthA, strengthB float64
	for _, p := range parties {
		if lenA+p.Length <= teamSize && (lenB+p.Length > teamSize || strengthA <= strengthB) {
			teamA = append(teamA, p)
			strengthA += p.Strength
			lenA += p.Length
		} else if lenB+p.Length <= teamSize {
			teamB = append(teamB, p)
			strengthB += p.Strength
			lenB += p.Length
		}
	}

	// Sort so that team1 (blue) is the stronger team
	if strengthA < strengthB {
		teamA, teamB = teamB, teamA
	}

	return [2][]*MatchmakingTicket{teamA, teamB}
}

func (m *SkillBasedMatchmaker) averageRankPercentile(team []*MatchmakingTicket) float64 {
	var sum float64
	var count int
	for _, party := range team {
		sum += party.RankPercentileAverage
		count += party.Length
	}
	return sum / float64(count)
}

type MatchmakingPartyMember struct {
	Entry          *MatchmakerEntry
	Rating         types.Rating
	RankPercentile float64
}

type MatchmakingTicket struct {
	Members               []MatchmakingPartyMember
	Strength              float64
	Length                int
	Timestamp             float64
	RankPercentileAverage float64
}

type MatchmakingTeam struct {
	Parties   []*MatchmakingTicket
	Strength  float64
	Timestamp float64
}

func (m *SkillBasedMatchmaker) predictOutcomes(candidates [][]runtime.MatchmakerEntry, defaultRankPercentile float64) []PredictedMatch {

	// count the number of valid candidates
	var validCandidates int
	for _, c := range candidates {
		if c != nil {
			validCandidates++
		}
	}

	// Create a mapping of all players by their ticket IDs
	playersByTicket := make(map[string]map[string]MatchmakingPartyMember, 32)

	// Create a list of all tickets in each candidate
	candidateTickets := make([]map[string]struct{}, 0, len(candidates))

	for _, c := range candidates {
		if c == nil {
			continue
		}
		ticketSet := make(map[string]struct{}, len(c))
		for _, e := range c {
			tID := e.GetTicket()

			ticketSet[tID] = struct{}{}
			ticket, ok := playersByTicket[tID]
			if !ok {
				ticket = make(map[string]MatchmakingPartyMember, 4)
				playersByTicket[tID] = ticket
			}

			if _, ok := ticket[e.GetPresence().GetSessionId()]; ok {
				continue
			}

			ticket[e.GetPresence().GetSessionId()] = MatchmakingPartyMember{
				Entry: e.(*MatchmakerEntry),
				Rating: types.Rating{
					Mu:    e.GetProperties()["rating_mu"].(float64),
					Sigma: e.GetProperties()["rating_sigma"].(float64),
				},
				RankPercentile: e.GetProperties()["rank_percentile"].(float64),
			}
		}

		candidateTickets = append(candidateTickets, ticketSet)
	}

	// Create a party for each ticket
	partyByTicketID := make(map[string]*MatchmakingTicket, len(playersByTicket))

	for ticketID, players := range playersByTicket {
		party := &MatchmakingTicket{
			Members:   make([]MatchmakingPartyMember, 0, len(players)),
			Length:    len(players),
			Timestamp: float64(time.Now().Unix()),
		}
		for _, p := range players {
			party.Members = append(party.Members, p)
			party.Strength += p.Rating.Mu

			// If the rank percentile is 0, set it to the default
			if p.RankPercentile == 0 {
				p.RankPercentile = defaultRankPercentile
			}
			party.RankPercentileAverage += p.RankPercentile

			// Update the party timestamp if this player has a lower timestamp
			if party.Timestamp > p.Entry.Properties["timestamp"].(float64) {
				party.Timestamp = p.Entry.Properties["timestamp"].(float64)
			}
		}
		partyByTicketID[ticketID] = party
	}

	// Create a list of all possible 4v4 matches, with predictions
	predictions := make([]PredictedMatch, 0, validCandidates)

	for _, tickets := range candidateTickets {

		parties := make([]*MatchmakingTicket, 0, len(tickets))
		size := 0
		for t := range tickets {
			p, ok := partyByTicketID[t]
			if !ok {
				continue
			}
			parties = append(parties, p)
			size += p.Length
		}

		teams := m.createBalancedMatch(parties, size/2)
		if len(teams[0]) != len(teams[1]) {
			continue
		}
		// Calculate the draw probability
		ratingsByTeam := make([]types.Team, 2)
		for i, team := range teams {
			ratingsByTeam[i] = make(types.Team, 0, size/2)
			for _, p := range team {
				for _, e := range p.Members {
					ratingsByTeam[i] = append(ratingsByTeam[i], e.Rating)
				}
			}
		}

		var (
			percentileA = m.averageRankPercentile(teams[0])
			percentileB = m.averageRankPercentile(teams[1])
		)

		predictions = append(predictions, PredictedMatch{
			TeamA:              teams[0],
			TeamB:              teams[1],
			Draw:               rating.PredictDraw(ratingsByTeam, nil),
			Size:               size,
			RankDelta:          math.Abs(percentileA - percentileB),
			AvgRankPercentileA: percentileA,
			AvgRankPercentileB: percentileB,
		})
	}

	return predictions
}

func (m *SkillBasedMatchmaker) assembleUniqueMatches(sortedCandidates []PredictedMatch) [][]runtime.MatchmakerEntry {

	matches := make([][]runtime.MatchmakerEntry, 0, len(sortedCandidates))

	matchedPlayers := make(map[string]struct{}, 0)

OuterLoop:
	for _, r := range sortedCandidates {

		match := make([]runtime.MatchmakerEntry, 0, r.Size)

		// Check if any players in the match have already been matched
		for _, e := range r.Entrants() {
			if _, ok := matchedPlayers[e.Presence.SessionId]; ok {
				continue OuterLoop
			}
		}

		for _, e := range r.Entrants() {
			match = append(match, e)
			matchedPlayers[e.Presence.SessionId] = struct{}{}
		}

		matches = append(matches, match)
	}

	return matches
}
