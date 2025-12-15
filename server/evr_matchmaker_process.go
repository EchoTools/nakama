package server

import (
	"sort"
	"strings"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/intinig/go-openskill/types"
)

// Process potential matches from candidates, applying filters and predictions
func (m *SkillBasedMatchmaker) processPotentialMatches(candidates [][]runtime.MatchmakerEntry) ([][]runtime.MatchmakerEntry, [][]runtime.MatchmakerEntry, map[string]int) {

	filterCounts := make(map[string]int)

	// Filter out players who are too far away from each other
	filterCounts["max_rtt"] = m.filterWithinMaxRTT(candidates)

	config := PredictionConfig{}
	if settings := ServiceSettings(); settings != nil {
		mu := settings.SkillRating.Defaults.Mu
		sigma := settings.SkillRating.Defaults.Sigma
		z := settings.SkillRating.Defaults.Z
		config.PartyBoostPercent = settings.Matchmaking.PartySkillBoostPercent
		config.EnableRosterVariants = settings.Matchmaking.EnableRosterVariants
		config.UseSnakeDraftFormation = settings.Matchmaking.UseSnakeDraftTeamFormation
		config.OpenSkillOptions = &types.OpenSkillOptions{
			Mu:    &mu,
			Sigma: &sigma,
			Z:     &z,
		}
	}

	// predict the outcome of the matches
	oldestTicket := ""
	oldestTicketTimestamp := time.Now().UTC().Unix()
	predictions := make([]PredictedMatch, 0, len(candidates))
	for c := range predictCandidateOutcomesWithConfig(candidates, config) {
		predictions = append(predictions, c)
		if oldestTicket == "" || c.OldestTicketTimestamp < oldestTicketTimestamp {
			oldestTicket = c.Candidate[0].GetTicket()
			oldestTicketTimestamp = c.OldestTicketTimestamp
		}
	}

	sort.SliceStable(predictions, func(i, j int) bool {
		// First priority: Match size (larger matches preferred)
		if predictions[i].Size != predictions[j].Size {
			return predictions[i].Size > predictions[j].Size
		}

		// Second priority: Oldest ticket gets priority
		// Sort by oldest ticket timestamp (smaller timestamp = older = higher priority)
		if predictions[i].OldestTicketTimestamp != predictions[j].OldestTicketTimestamp {
			return predictions[i].OldestTicketTimestamp < predictions[j].OldestTicketTimestamp
		}

		// Third priority: Division diversity (fewer divisions preferred for more balanced matches)
		if predictions[i].DivisionCount != predictions[j].DivisionCount {
			return predictions[i].DivisionCount < predictions[j].DivisionCount
		}

		// Final tiebreaker: Match draw probability (higher draw probability = more evenly matched)
		return predictions[i].DrawProb > predictions[j].DrawProb
	})

	madeMatches := m.assembleUniqueMatches(predictions)

	return candidates, madeMatches, filterCounts
}

// Filter out candidates where players do not have a common server within the max RTT
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

// Assemble unique matches from sorted predicted candidates
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
