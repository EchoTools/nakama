package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"math"
	"os"
	"slices"
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
	if len(candidates) == 0 || len(candidates[0]) == 0 {
		logger.Error("No candidates found. Matchmaker cannot run.")
		return nil
	}

	startTime := time.Now()
	defer func() {
		if nk == nil {
			return
		}
		nk.MetricsGaugeSet("matchmaker_evr_candidate_count", nil, float64(len(candidates)))

		// Divide the time by the number of candidates
		nk.MetricsTimerRecord("matchmaker_evr_per_candidate", nil, time.Since(startTime)/time.Duration(len(candidates)))
	}()

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
		matches       [][]runtime.MatchmakerEntry
		filterCounts  map[string]int
		originalCount = len(candidates)
	)

	candidates, matches, filterCounts = m.processPotentialMatches(candidates)

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

func (m *SkillBasedMatchmaker) processPotentialMatches(candidates [][]runtime.MatchmakerEntry) ([][]runtime.MatchmakerEntry, [][]runtime.MatchmakerEntry, map[string]int) {

	// Write the candidates to a json filed called /tmp/candidates.json
	filterCounts := make(map[string]int)

	// Filter out duplicates
	filterCounts["duplicates"] = m.filterDuplicates(candidates)

	// Filter out odd-sized teams
	filterCounts["odd_sized"] = m.filterOddSizedMatches(candidates)

	// Filter out players who are too far away from each other
	filterCounts["max_rtt"] = m.filterWithinMaxRTT(candidates)

	// Create a list of balanced matches with predictions

	predictions := m.predictOutcomes(candidates)

	slices.SortStableFunc(predictions, func(a, b PredictedMatch) int {
		if a.Size != b.Size {
			return b.Size - a.Size
		}

		// Sort by the draw difference
		return int((b.Draw - a.Draw) * 10000)
	})

	madeMatches := m.assembleUniqueMatches(predictions)

	return candidates, madeMatches, filterCounts
}

func (m *SkillBasedMatchmaker) filterDuplicates(candidates [][]runtime.MatchmakerEntry) int {

	var (
		seen      = make(map[string]struct{}) // Map to track seen candidate combinations
		count     = 0
		ticketIDs = make([]string, 0, 10)
	)

	for i, candidate := range candidates {
		if candidate == nil {
			continue
		}
		ticketIDs = ticketIDs[:0]

		for _, e := range candidate {
			ticketIDs = append(ticketIDs, e.GetTicket())
		}
		slices.Sort(ticketIDs)
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

func (m *SkillBasedMatchmaker) filterOddSizedMatches(candidates [][]runtime.MatchmakerEntry) int {
	oddSizedCount := 0
	for i := range candidates {
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

func (m *SkillBasedMatchmaker) predictOutcomes(candidates [][]runtime.MatchmakerEntry) []PredictedMatch {

	// count the number of valid candidates
	var validCandidates int
	for _, c := range candidates {
		if c != nil {
			validCandidates++
		}
	}

	type player struct {
		entry   runtime.MatchmakerEntry
		rating  types.Rating
		ordinal float64
	}

	var (
		predictions                  = make([]PredictedMatch, 0, validCandidates)
		playersBySessionIDbyTicketID = make(map[string]map[string]player, 0)
		ticketIDs                    = make([]string, 0, 8)
		ticketStrengths              = make(map[string]float64, 10)
	)

	for _, c := range candidates {
		if c == nil {
			continue
		}

		candidateTicketSet := make(map[string]struct{}, len(c))
		teamSize := len(c) / 2

		for _, e := range c {
			tID := e.GetTicket()

			if _, ok := candidateTicketSet[tID]; !ok {
				ticketIDs = append(ticketIDs, tID)
			}
			candidateTicketSet[tID] = struct{}{}

			if _, ok := playersBySessionIDbyTicketID[tID]; !ok {
				playersBySessionIDbyTicketID[tID] = make(map[string]player, 0)
			}

			// Add the player once
			if _, ok := playersBySessionIDbyTicketID[tID][e.GetPresence().GetSessionId()]; !ok {

				// Extract the player's rating
				r := types.Rating{Mu: 25.0, Sigma: 8.333}
				if mu, ok := e.GetProperties()["rating_mu"].(float64); ok {
					if sigma, ok := e.GetProperties()["rating_sigma"].(float64); ok {
						r.Mu = mu
						r.Sigma = sigma
					}
				}
				playersBySessionIDbyTicketID[tID][e.GetPresence().GetSessionId()] = player{
					entry:   e,
					rating:  r,
					ordinal: rating.Ordinal(r),
				}
			}
		}

		// Calculate the strength of each ticket
		ticketIDs = ticketIDs[:0]
		for tID := range candidateTicketSet {

			// Add the ticket to the list
			ticketIDs = append(ticketIDs, tID)

			// Calculate the strength of the ticket (once)
			if _, ok := ticketStrengths[tID]; !ok {
				count := 0
				for _, p := range playersBySessionIDbyTicketID[tID] {
					ticketStrengths[tID] += p.rating.Mu
					count++
				}
				if count == 0 {
					ticketStrengths[tID] = 0
				} else {
					ticketStrengths[tID] /= float64(count)
				}
			}
		}

		// Sort the ticketIDs by size, largest first, then by strength (descending)
		slices.SortStableFunc(ticketIDs, func(a, b string) int {
			if a == b {
				return 0
			}
			if len(playersBySessionIDbyTicketID[a]) != len(playersBySessionIDbyTicketID[b]) {
				return len(playersBySessionIDbyTicketID[b]) - len(playersBySessionIDbyTicketID[a])
			}
			return int(ticketStrengths[b] - ticketStrengths[a])
		})

		type team struct {
			sessionIDs map[string]struct{}
			ratings    []types.Rating
			strength   float64
			length     int
			ordinal    float64
		}

		teams := [2]team{
			{
				sessionIDs: make(map[string]struct{}, teamSize),
				ratings:    make([]types.Rating, 0, teamSize),
				strength:   0,
				length:     0,
			},
			{
				sessionIDs: make(map[string]struct{}, teamSize),
				ratings:    make([]types.Rating, 0, teamSize),
				strength:   0,
				length:     0,
			},
		}

		// Assign players to teams
		for _, tID := range ticketIDs {
			teamIdx := 0
			ticketLength := len(playersBySessionIDbyTicketID[tID])
			if teams[0].length+ticketLength >= teamSize && (teams[1].length+ticketLength < teamSize || teams[0].strength > teams[1].strength) {
				teamIdx = 1
			}

			for _, p := range playersBySessionIDbyTicketID[tID] {
				teams[teamIdx].sessionIDs[p.entry.GetPresence().GetSessionId()] = struct{}{}
				teams[teamIdx].ratings = append(teams[teamIdx].ratings, p.rating)
				teams[teamIdx].strength += p.rating.Mu
				teams[teamIdx].length += 1
			}

		}

		// Sort so that team1 (blue) is the stronger team
		if teams[0].strength < teams[1].strength {
			teams[0], teams[1] = teams[1], teams[0]
		}
		for i := 0; i < 2; i++ {
			teams[i].ordinal = rating.TeamOrdinal(types.TeamRating{Team: teams[i].ratings})
		}
		// Sort the entries by the team they are on
		slices.SortStableFunc(c, func(a, b runtime.MatchmakerEntry) int {
			if _, ok := teams[0].sessionIDs[a.GetPresence().GetSessionId()]; ok {
				return -1
			}
			return 1
		})

		predictions = append(predictions, PredictedMatch{
			Candidate:    c,
			Draw:         rating.PredictDraw([]types.Team{teams[0].ratings, teams[1].ratings}, nil),
			Size:         len(c),
			TeamOrdinalA: teams[0].strength,
			TeamOrdinalB: teams[1].strength,
			OrdinalDelta: math.Abs(teams[0].ordinal - teams[1].ordinal),
		})
	}

	return predictions
}

func (m *SkillBasedMatchmaker) assembleUniqueMatches(sortedCandidates []PredictedMatch) [][]runtime.MatchmakerEntry {

	matches := make([][]runtime.MatchmakerEntry, 0, len(sortedCandidates))

	matchedPlayers := make(map[string]struct{}, 0)

OuterLoop:
	for _, r := range sortedCandidates {

		// Check if any players in the match have already been matched
		for _, e := range r.Candidate {
			if _, ok := matchedPlayers[e.GetPresence().GetSessionId()]; ok {
				continue OuterLoop
			}
		}

		for _, e := range r.Candidate {
			matchedPlayers[e.GetPresence().GetSessionId()] = struct{}{}
		}

		matches = append(matches, r.Candidate)
	}

	return matches
}
