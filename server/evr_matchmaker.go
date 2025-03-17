package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
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

	// Filter out players who are too far away from each other
	filterCounts["max_rtt"] = m.filterWithinMaxRTT(candidates)

	// Create a list of balanced matches with predictions

	predictions := predictCandidateOutcomes(candidates)

	f, err := os.Create("/tmp/predictions.json")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	json.NewEncoder(f).Encode(predictions)

	sort.SliceStable(predictions, func(i, j int) bool {
		if predictions[i].Size != predictions[j].Size {
			return predictions[i].Size > predictions[j].Size
		}

		return predictions[i].OrdinalDelta < predictions[j].OrdinalDelta
	})

	madeMatches := m.assembleUniqueMatches(predictions)

	return candidates, madeMatches, filterCounts
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
