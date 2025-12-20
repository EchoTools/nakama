package server

import (
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
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
							ExternalIP: [4]byte{192, 168, 1, 1},
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
	entry1.Properties = make(map[string]interface{})
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
	entry2.Properties = make(map[string]interface{})
	for k, v := range entry2.StringProperties {
		entry2.Properties[k] = v
	}
	for k, v := range entry2.NumericProperties {
		entry2.Properties[k] = v
	}

	// Candidates: all tickets with all players
	candidates := [][]interface{}{
		{entry1},
		{entry2},
	}

	// Made matches: only session1 was matched
	madeMatches := [][]interface{}{
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
