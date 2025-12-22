package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
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

// EncodeEndpointID creates a short, obfuscated identifier from an IP address
// This is used to avoid exposing raw IP addresses in matchmaking ticket properties
func EncodeEndpointID(ip string) string {
	hash := sha256.Sum256([]byte(ip))
	// Use base32 encoding (alphanumeric, case-insensitive) and take first 8 chars
	encoded := base32.StdEncoding.EncodeToString(hash[:])
	return strings.ToLower(encoded[:8])
}

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
		nk.MetricsTimerRecord("matchmaker_process_duration", nil, time.Since(startTime))
		// Divide the time by the number of candidates
		nk.MetricsTimerRecord("matchmaker_per_candidate_duration", nil, time.Since(startTime)/time.Duration(len(candidates)))
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

	nk.MetricsCounterAdd("matchmaker_candidate_count", nil, int64(len(candidates)))
	nk.MetricsCounterAdd("matchmaker_match_count", nil, int64(len(matches)))
	nk.MetricsCounterAdd("matchmaker_ticket_count", nil, int64(len(ticketSet)))
	nk.MetricsCounterAdd("matchmaker_unmatched_player_count", nil, int64(len(unmatchedPlayers)))
	nk.MetricsCounterAdd("matchmaker_matched_player_count", nil, int64(len(matchedPlayers)))

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
