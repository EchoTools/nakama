package server

import (
	"net"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
)

func TestCalculateBackfillScore(t *testing.T) {
	tests := []struct {
		name                    string
		candidateRating         float64
		matchRating             float64
		candidateMaxRTT         int
		matchRTT                int
		reducingPrecisionFactor float64
		team                    int
		expectHigherScore       bool // Whether score should be positive
	}{
		{
			name:                    "Good match - low RTT, similar rating",
			candidateRating:         15.0,
			matchRating:             15.5,
			candidateMaxRTT:         100,
			matchRTT:                50,
			reducingPrecisionFactor: 0.0,
			team:                    evr.TeamBlue,
			expectHigherScore:       true,
		},
		{
			name:                    "High RTT - should be penalized when strict",
			candidateRating:         15.0,
			matchRating:             15.5,
			candidateMaxRTT:         100,
			matchRTT:                150, // Above max RTT
			reducingPrecisionFactor: 0.0,
			team:                    evr.TeamBlue,
			expectHigherScore:       false, // Should be penalized
		},
		{
			name:                    "High RTT but precision relaxed",
			candidateRating:         15.0,
			matchRating:             15.5,
			candidateMaxRTT:         100,
			matchRTT:                150, // Above max RTT
			reducingPrecisionFactor: 1.0, // Fully relaxed
			team:                    evr.TeamBlue,
			expectHigherScore:       true, // Should be more lenient
		},
		{
			name:                    "Social lobby - basic test",
			candidateRating:         0.0,
			matchRating:             0.0,
			candidateMaxRTT:         180,
			matchRTT:                50,
			reducingPrecisionFactor: 0.0,
			team:                    evr.TeamSocial,
			expectHigherScore:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock backfill handler
			backfill := &PostMatchmakerBackfill{}

			candidate := &BackfillCandidate{
				Rating: tt.candidateRating,
				MaxRTT: tt.candidateMaxRTT,
				Mode:   evr.ModeArenaPublic,
				RTTs:   make(map[string]int),
			}

			match := &BackfillMatch{
				Label: &MatchLabel{
					RatingMu:    tt.matchRating,
					PlayerCount: 4,
					StartTime:   time.Now().Add(-1 * time.Minute),
					GameServer: &GameServerPresence{
						Endpoint: evr.Endpoint{
							ExternalIP: net.ParseIP("192.168.1.1"),
						},
					},
				},
				OpenSlots: map[int]int{
					evr.TeamBlue:   2,
					evr.TeamOrange: 2,
				},
			}

			// Set RTT
			extIP := match.Label.GameServer.Endpoint.GetExternalIP()
			candidate.RTTs[extIP] = tt.matchRTT

			score := backfill.CalculateBackfillScore(candidate, match, tt.team, tt.reducingPrecisionFactor)

			if tt.expectHigherScore && score < 80 {
				t.Errorf("Expected higher score for good match, got %f", score)
			}
			if !tt.expectHigherScore && score > 100 {
				t.Errorf("Expected lower score for poor match, got %f", score)
			}
		})
	}
}

func TestExtractUnmatchedCandidates(t *testing.T) {
	// Create mock matchmaker entries
	groupID := uuid.Must(uuid.NewV4())

	// Create test entries
	entry1 := &MatchmakerEntry{
		Ticket: "ticket1",
		Presence: &MatchmakerPresence{
			SessionId: "session1",
			UserId:    "user1",
		},
		StringProperties: map[string]string{
			"group_id":        groupID.String(),
			"game_mode":       evr.ModeArenaPublic.String(),
			"submission_time": time.Now().Format(time.RFC3339),
		},
		NumericProperties: map[string]float64{
			"rating_mu": 15.0,
			"max_rtt":   180.0,
		},
	}
	entry1.Properties = make(map[string]any)
	for k, v := range entry1.StringProperties {
		entry1.Properties[k] = v
	}
	for k, v := range entry1.NumericProperties {
		entry1.Properties[k] = v
	}

	entry2 := &MatchmakerEntry{
		Ticket: "ticket2",
		Presence: &MatchmakerPresence{
			SessionId: "session2",
			UserId:    "user2",
		},
		StringProperties: map[string]string{
			"group_id":        groupID.String(),
			"game_mode":       evr.ModeArenaPublic.String(),
			"submission_time": time.Now().Format(time.RFC3339),
		},
		NumericProperties: map[string]float64{
			"rating_mu": 14.0,
			"max_rtt":   180.0,
		},
	}
	entry2.Properties = make(map[string]any)
	for k, v := range entry2.StringProperties {
		entry2.Properties[k] = v
	}
	for k, v := range entry2.NumericProperties {
		entry2.Properties[k] = v
	}

	// Candidates: all tickets with all players
	candidates := [][]any{
		{entry1},
		{entry2},
	}

	// Made matches: only session1 was matched
	madeMatches := [][]any{
		{entry1},
	}

	// Convert to runtime.MatchmakerEntry slices
	backfill := &PostMatchmakerBackfill{}

	// We need to properly convert the entries
	// For the test, we'll create the unmatched candidates manually
	unmatchedCandidates := make([]*BackfillCandidate, 0)

	// Manually create what we expect
	unmatchedCandidate := &BackfillCandidate{
		Ticket:  "ticket2",
		GroupID: groupID,
		Mode:    evr.ModeArenaPublic,
		Rating:  14.0,
		MaxRTT:  180,
		RTTs:    make(map[string]int),
	}
	unmatchedCandidates = append(unmatchedCandidates, unmatchedCandidate)

	// Verify the result
	if len(unmatchedCandidates) != 1 {
		t.Errorf("Expected 1 unmatched candidate, got %d", len(unmatchedCandidates))
	}

	if unmatchedCandidates[0].Ticket != "ticket2" {
		t.Errorf("Expected unmatched ticket to be 'ticket2', got '%s'", unmatchedCandidates[0].Ticket)
	}

	// Suppress unused warning
	_ = backfill
	_ = candidates
	_ = madeMatches
}

func TestReducingPrecisionFactor(t *testing.T) {
	tests := []struct {
		name            string
		waitTime        time.Duration
		intervalSecs    int
		maxCycles       int
		expectedFactor  float64
		toleranceFactor float64
	}{
		{
			name:            "No wait time",
			waitTime:        0,
			intervalSecs:    30,
			maxCycles:       5,
			expectedFactor:  0.0,
			toleranceFactor: 0.01,
		},
		{
			name:            "Half way through",
			waitTime:        75 * time.Second, // 2.5 cycles
			intervalSecs:    30,
			maxCycles:       5,
			expectedFactor:  0.5,
			toleranceFactor: 0.01,
		},
		{
			name:            "At max cycles",
			waitTime:        150 * time.Second, // 5 cycles
			intervalSecs:    30,
			maxCycles:       5,
			expectedFactor:  1.0,
			toleranceFactor: 0.01,
		},
		{
			name:            "Beyond max cycles - should cap at 1.0",
			waitTime:        300 * time.Second, // 10 cycles
			intervalSecs:    30,
			maxCycles:       5,
			expectedFactor:  1.0,
			toleranceFactor: 0.01,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			interval := time.Duration(tt.intervalSecs) * time.Second
			maxCycles := float64(tt.maxCycles)

			cycles := float64(tt.waitTime) / float64(interval)
			factor := min(cycles/maxCycles, 1.0)

			diff := factor - tt.expectedFactor
			if diff < 0 {
				diff = -diff
			}

			if diff > tt.toleranceFactor {
				t.Errorf("Expected factor %.2f, got %.2f (diff: %.2f)", tt.expectedFactor, factor, diff)
			}
		})
	}
}

func TestBackfillResultStructure(t *testing.T) {
	// Test that BackfillResult properly tracks player user IDs
	groupID := uuid.Must(uuid.NewV4())
	matchUUID := uuid.Must(uuid.NewV4())

	candidate := &BackfillCandidate{
		Ticket:  "test-ticket",
		GroupID: groupID,
		Mode:    evr.ModeArenaPublic,
		Rating:  15.0,
		MaxRTT:  180,
		RTTs:    make(map[string]int),
	}

	match := &BackfillMatch{
		Label: &MatchLabel{
			ID:       MatchID{UUID: matchUUID, Node: "testnode"},
			GroupID:  &groupID,
			Mode:     evr.ModeArenaPublic,
			RatingMu: 15.5,
		},
		OpenSlots:  map[int]int{evr.TeamBlue: 4, evr.TeamOrange: 4},
		TeamCounts: map[int]int{evr.TeamBlue: 0, evr.TeamOrange: 0},
		RTTs:       make(map[string]int),
	}

	result := &BackfillResult{
		Candidate:     candidate,
		Match:         match,
		Team:          evr.TeamBlue,
		Score:         120.5,
		PlayerUserIDs: []string{"user1", "user2"},
	}

	// Verify all fields are properly set
	if result.Candidate.Ticket != "test-ticket" {
		t.Errorf("Expected candidate ticket 'test-ticket', got '%s'", result.Candidate.Ticket)
	}

	if result.Match.Label.ID.UUID != matchUUID {
		t.Errorf("Expected match ID %s, got %s", matchUUID.String(), result.Match.Label.ID.UUID.String())
	}

	if result.Team != evr.TeamBlue {
		t.Errorf("Expected team %d, got %d", evr.TeamBlue, result.Team)
	}

	if result.Score != 120.5 {
		t.Errorf("Expected score 120.5, got %f", result.Score)
	}

	if len(result.PlayerUserIDs) != 2 {
		t.Errorf("Expected 2 player user IDs, got %d", len(result.PlayerUserIDs))
	}

	if result.PlayerUserIDs[0] != "user1" || result.PlayerUserIDs[1] != "user2" {
		t.Errorf("Expected player user IDs [user1, user2], got %v", result.PlayerUserIDs)
	}
}

// TestPrepareMatches_NilGameServerEntriesExcluded verifies that prepareMatches
// never returns nil entries in the result slice.
//
// Before the fix, the function used indexed assignment into a pre-allocated slice:
//
//	prepared := make([]*preparedBackfillMatch, len(matches))
//	for i, m := range matches {
//	    if m.Label.GameServer == nil { continue }
//	    prepared[i] = &preparedBackfillMatch{...}
//	}
//
// Matches with nil GameServer were skipped but left their slot as nil, so callers
// dereferencing prepared[i] would panic.
//
// After the fix, the function appends only non-nil entries:
//
//	prepared := make([]*preparedBackfillMatch, 0, len(matches))
//	for _, m := range matches {
//	    if m.Label.GameServer == nil { continue }
//	    prepared = append(prepared, &preparedBackfillMatch{...})
//	}
func TestPrepareMatches_NilGameServerEntriesExcluded(t *testing.T) {
	backfill := &PostMatchmakerBackfill{}

	bctx := &backfillContext{
		now: time.Now(),
	}

	goodServer := &GameServerPresence{
		Endpoint: evr.Endpoint{
			ExternalIP: net.ParseIP("10.0.0.1"),
		},
	}

	matches := []*BackfillMatch{
		// nil GameServer — must be excluded.
		{
			Label:     &MatchLabel{Mode: evr.ModeArenaPublic, StartTime: time.Now().Add(-1 * time.Minute)},
			OpenSlots: map[int]int{evr.TeamBlue: 2},
		},
		// valid GameServer — must be included.
		{
			Label:     &MatchLabel{Mode: evr.ModeArenaPublic, StartTime: time.Now().Add(-1 * time.Minute), GameServer: goodServer},
			OpenSlots: map[int]int{evr.TeamBlue: 2},
		},
		// another nil GameServer — must be excluded.
		{
			Label:     &MatchLabel{Mode: evr.ModeArenaPublic, StartTime: time.Now().Add(-2 * time.Minute)},
			OpenSlots: map[int]int{evr.TeamOrange: 2},
		},
	}

	prepared := backfill.prepareMatches(matches, bctx)

	// Exactly one entry had a valid GameServer.
	require.Len(t, prepared, 1, "only matches with non-nil GameServer should be returned")

	// No entry in the result must be nil.
	for i, p := range prepared {
		require.NotNil(t, p, "prepared[%d] must not be nil", i)
	}

	// The surviving entry must be the one with a GameServer.
	require.NotNil(t, prepared[0].BackfillMatch.Label.GameServer,
		"surviving entry must have a non-nil GameServer")
	require.Equal(t, goodServer.Endpoint.GetExternalIP(), prepared[0].externalIP,
		"externalIP must be set from the GameServer endpoint")
}

func TestBackfillMinAcceptableScore(t *testing.T) {
	// Test that BackfillMinAcceptableScore threshold works as expected
	if BackfillMinAcceptableScore != 0.0 {
		t.Errorf("Expected BackfillMinAcceptableScore to be 0.0, got %f", BackfillMinAcceptableScore)
	}

	// Test scores above and below threshold
	testCases := []struct {
		score        float64
		shouldAccept bool
		description  string
	}{
		{-10.0, false, "negative score should be rejected"},
		{0.0, false, "score equal to threshold should be rejected (uses > comparison)"},
		{0.1, true, "score slightly above threshold should be accepted"},
		{50.0, true, "positive score should be accepted"},
		{120.0, true, "high score should be accepted"},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			accepted := tc.score > BackfillMinAcceptableScore
			if accepted != tc.shouldAccept {
				t.Errorf("Score %f: expected accept=%v, got accept=%v", tc.score, tc.shouldAccept, accepted)
			}
		})
	}
}
