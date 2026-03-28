package server

import (
	"sort"
	"strings"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/intinig/go-openskill/types"
)

func (m *SkillBasedMatchmaker) processPotentialMatches(logger runtime.Logger, entries []runtime.MatchmakerEntry) ([][]runtime.MatchmakerEntry, [][]runtime.MatchmakerEntry, map[string]int, []PredictedMatch) {
	candidates := groupEntriesSequentially(entries)

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

	// Determine if wait time should override size priority
	now := time.Now().UTC().Unix()
	oldestWaitTimeSecs := now - oldestTicketTimestamp
	waitTimeThreshold := int64(120) // Default: 120 seconds (2 minutes)
	if settings := ServiceSettings(); settings != nil && settings.Matchmaking.WaitTimePriorityThresholdSecs > 0 {
		waitTimeThreshold = int64(settings.Matchmaking.WaitTimePriorityThresholdSecs)
	}
	prioritizeWaitTime := oldestWaitTimeSecs >= waitTimeThreshold

	sort.SliceStable(predictions, func(i, j int) bool {
		if prioritizeWaitTime {
			// When wait time threshold exceeded, prioritize wait time over size
			// First priority: Oldest ticket gets priority
			if predictions[i].OldestTicketTimestamp != predictions[j].OldestTicketTimestamp {
				return predictions[i].OldestTicketTimestamp < predictions[j].OldestTicketTimestamp
			}

			// Second priority: Match size (larger matches preferred)
			if predictions[i].Size != predictions[j].Size {
				return predictions[i].Size > predictions[j].Size
			}
		} else {
			// Normal priority: size first, then wait time
			// First priority: Match size (larger matches preferred)
			if predictions[i].Size != predictions[j].Size {
				return predictions[i].Size > predictions[j].Size
			}

			// Second priority: Oldest ticket gets priority
			if predictions[i].OldestTicketTimestamp != predictions[j].OldestTicketTimestamp {
				return predictions[i].OldestTicketTimestamp < predictions[j].OldestTicketTimestamp
			}
		}

		// Third priority: Division diversity (fewer divisions preferred for more balanced matches)
		if predictions[i].DivisionCount != predictions[j].DivisionCount {
			return predictions[i].DivisionCount < predictions[j].DivisionCount
		}

		// Final tiebreaker: Match draw probability (higher draw probability = more evenly matched)
		return predictions[i].DrawProb > predictions[j].DrawProb
	})

	var madeMatches [][]runtime.MatchmakerEntry

	settings := ServiceSettings()
	useReservations := settings != nil && settings.Matchmaking.EnableTicketReservation

	if useReservations {
		starving, reserved := m.buildReservations(logger, candidates, predictions, &settings.Matchmaking)
		madeMatches = m.assembleMatchesWithReservations(logger, predictions, starving, reserved)
		filterCounts["reserved_players"] = len(reserved)
		filterCounts["starving_tickets"] = len(starving)
	} else {
		madeMatches = m.assembleUniqueMatches(predictions)
	}

	return candidates, madeMatches, filterCounts, predictions
}

func groupEntriesSequentially(entries []runtime.MatchmakerEntry) [][]runtime.MatchmakerEntry {
	if len(entries) == 0 {
		return nil
	}

	maxCount := 8
	countMultiple := 2

	if v, ok := entries[0].GetProperties()["max_team_size"].(float64); ok && int(v) > 0 {
		maxCount = int(v) * 2
	}
	if v, ok := entries[0].GetProperties()["count_multiple"].(float64); ok && int(v) > 0 {
		countMultiple = int(v)
	}

	if maxCount <= 0 {
		maxCount = 8
	}
	if countMultiple <= 0 {
		countMultiple = 2
	}

	// Group entries by ticket to keep parties atomic. Each ticket represents
	// a party (or solo player) and must never be split across candidates.
	type ticketGroup struct {
		ticket  string
		entries []runtime.MatchmakerEntry
	}

	ticketOrder := make([]string, 0)
	ticketMap := make(map[string]*ticketGroup)
	for _, entry := range entries {
		ticket := entry.GetTicket()
		if tg, ok := ticketMap[ticket]; ok {
			tg.entries = append(tg.entries, entry)
		} else {
			ticketOrder = append(ticketOrder, ticket)
			ticketMap[ticket] = &ticketGroup{ticket: ticket, entries: []runtime.MatchmakerEntry{entry}}
		}
	}

	// Sort ticket groups largest-first so parties are placed before solos.
	// Without this, sequential packing in CreatedAt order can starve parties
	// whose tickets were replaced (and thus have newer timestamps) — older
	// solos fill the first candidate, leaving the party in an undersized
	// remainder that gets rejected.
	sort.SliceStable(ticketOrder, func(i, j int) bool {
		return len(ticketMap[ticketOrder[i]].entries) > len(ticketMap[ticketOrder[j]].entries)
	})

	// Pack ticket groups into candidates, never splitting a ticket across candidates.
	candidates := make([][]runtime.MatchmakerEntry, 0, (len(entries)+maxCount-1)/maxCount)
	current := make([]runtime.MatchmakerEntry, 0, maxCount)

	for _, ticket := range ticketOrder {
		tg := ticketMap[ticket]

		// If this ticket alone exceeds maxCount, it can't fit in any candidate — skip it.
		if len(tg.entries) > maxCount {
			continue
		}

		// If adding this ticket would exceed maxCount, flush the current candidate.
		if len(current)+len(tg.entries) > maxCount {
			// Trim to count_multiple boundary before flushing.
			if rem := len(current) % countMultiple; rem != 0 {
				current = current[:len(current)-rem]
			}
			if len(current) > 0 {
				candidate := make([]runtime.MatchmakerEntry, len(current))
				copy(candidate, current)
				candidates = append(candidates, candidate)
			}
			current = current[:0]
		}

		current = append(current, tg.entries...)
	}

	// Flush remaining entries.
	if rem := len(current) % countMultiple; rem != 0 {
		current = current[:len(current)-rem]
	}
	if len(current) > 0 {
		candidate := make([]runtime.MatchmakerEntry, len(current))
		copy(candidate, current)
		candidates = append(candidates, candidate)
	}

	return candidates
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
		if isUndersizedMatch(r.Candidate) {
			continue OuterLoop
		}

		for _, e := range r.Candidate {
			matchedPlayers[e.GetPresence().GetSessionId()] = struct{}{}
		}

		matches = append(matches, r.Candidate)
	}

	return matches
}

func isUndersizedMatch(candidate []runtime.MatchmakerEntry) bool {
	if len(candidate) == 0 {
		return true
	}

	minTeamSize := 4.0
	if v, ok := candidate[0].GetProperties()["min_team_size"].(float64); ok && v > 0 {
		minTeamSize = v
	}

	minMatchSize := int(minTeamSize) * 2
	if len(candidate) >= minMatchSize {
		return false
	}

	failsafeTimeout := 0.0
	if v, ok := candidate[0].GetProperties()["failsafe_timeout"].(float64); ok {
		failsafeTimeout = v
	}
	if failsafeTimeout <= 0 {
		return false
	}

	now := float64(time.Now().UTC().Unix())
	oldestTimestamp := now
	for _, entry := range candidate {
		if ts, ok := entry.GetProperties()["timestamp"].(float64); ok && ts < oldestTimestamp {
			oldestTimestamp = ts
		}
	}

	if now-oldestTimestamp >= failsafeTimeout {
		return false
	}

	return true
}
