package server

import (
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCombatMatchmakingRules(t *testing.T) {
	t.Parallel()

	modeCombat := evr.ModeCombatPublic.String()
	modeArena := evr.ModeArenaPublic.String()
	cfg := PredictionConfig{UseSnakeDraftFormation: true}

	runTest := func(name string, totalPlayers int, mode string, isParty bool, ageSeconds int, expectMatch bool, expectedSize int) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			entries := make([]runtime.MatchmakerEntry, totalPlayers)
			partyTicket := "party-1"

			minTeamSize := 1.0

			now := time.Now().UTC().Unix()
			for i := 0; i < totalPlayers; i++ {
				ticket := "solo-" + string(rune(48+i))
				if isParty {
					ticket = partyTicket
				}
				entries[i] = &MatchmakerEntry{
					Ticket: ticket,
					Presence: &MatchmakerPresence{
						UserId:    "u" + string(rune(48+i)),
						SessionId: "s" + string(rune(48+i)),
					},
					Properties: map[string]interface{}{
						"game_mode":      mode,
						"min_team_size":  minTeamSize,
						"max_team_size":  float64(5),
						"count_multiple": float64(2),
						"timestamp":      float64(now - int64(ageSeconds)),
					},
				}
			}

			// 1. Test Ticket Grouping (Party Splitting)
			candidates := groupEntriesSequentially(entries)

			// 2. Test Team Formation (Prediction)
			predictionChan := predictCandidateOutcomesWithConfig(candidates, cfg)

			var prediction *PredictedMatch
			select {
			case p, ok := <-predictionChan:
				if ok {
					prediction = &p
				}
			case <-time.After(5 * time.Second):
				t.Fatal("predictCandidateOutcomesWithConfig did not complete within 5 seconds — possible deadlock")
			}

			if expectMatch {
				require.NotNil(t, prediction, "scenario %q: expected a match to be produced but got nil", name)
				assert.Equal(t, int8(expectedSize), prediction.Size,
					"scenario %q: match size mismatch (expected %d, got %d)", name, expectedSize, prediction.Size)
			} else {
				assert.Nil(t, prediction,
					"scenario %q: expected NO match to be produced, but got one (size=%d)", name, func() int8 {
						if prediction != nil {
							return prediction.Size
						}
						return 0
					}())
			}
		})
	}

	// Combat Tests (Even only, min 1v1, dynamic delay)
	// The 60-second grace gate only applies to UNDERSIZED matches
	// (len < min_team_size*2). With min_team_size=1, a 1v1 (2 players) is
	// full-size, so it is never gated and matches immediately regardless of age
	// (consistent with Arena 1v1 (New) below).
	runTest("Combat 1v1 (New)", 2, modeCombat, false, 10, true, 2)            // full-size 1v1 → not gated
	runTest("Combat 1v1 (Old)", 2, modeCombat, false, 65, true, 2)            // 65s > 60s → gate passes
	runTest("Combat 4v4 (New)", 8, modeCombat, false, 10, true, 8)            // 8 >= 8 → gate bypassed → OK
	runTest("Combat 3v4 (7 players, Old)", 7, modeCombat, false, 65, true, 6) // 65s old → gate passes
	runTest("Combat Party 2v2 (Old)", 4, modeCombat, true, 65, true, 4)       // party ticket split for combat → 4 solos → gate passes

	// Arena Tests (No combat-gate delay; min_team_size=1 so 2 players suffices)
	runTest("Arena 1v1 (New)", 2, modeArena, false, 10, true, 2) // No 60s gate for arena

	// Boundary: exactly 60 seconds old — the gate checks `< 60`, so 60s is allowed.
	runTest("Combat 1v1 (Exactly 60s)", 2, modeCombat, false, 60, true, 2) // 60s == 60s → NOT blocked (gate is strict <)
}

// TestCombatGate_ExactBoundary_60Seconds verifies the boundary condition of the
// combat undersized gate at exactly the 60-second mark. The gate in
// evr_matchmaker_prediction.go reads:
//
//	if time.Now().UTC().Unix()-int64(oldestTimestamp) < 60 { continue }
//
// This means tickets that are exactly 60 seconds old ARE allowed through
// (the condition `< 60` is false when age == 60). This test pins that boundary.
func TestCombatGate_ExactBoundary_60Seconds(t *testing.T) {
	t.Parallel()

	cfg := PredictionConfig{UseSnakeDraftFormation: true}
	now := time.Now().UTC().Unix()

	// Two combat players whose tickets are exactly 60 seconds old.
	entries := []runtime.MatchmakerEntry{
		&MatchmakerEntry{
			Ticket:   "solo-a",
			Presence: &MatchmakerPresence{UserId: "ua", SessionId: "sa"},
			Properties: map[string]interface{}{
				"game_mode":      evr.ModeCombatPublic.String(),
				"min_team_size":  float64(1),
				"max_team_size":  float64(5),
				"count_multiple": float64(2),
				"timestamp":      float64(now - 60), // exactly 60 seconds old
			},
		},
		&MatchmakerEntry{
			Ticket:   "solo-b",
			Presence: &MatchmakerPresence{UserId: "ub", SessionId: "sb"},
			Properties: map[string]interface{}{
				"game_mode":      evr.ModeCombatPublic.String(),
				"min_team_size":  float64(1),
				"max_team_size":  float64(5),
				"count_multiple": float64(2),
				"timestamp":      float64(now - 60),
			},
		},
	}

	candidates := groupEntriesSequentially(entries)
	predChan := predictCandidateOutcomesWithConfig(candidates, cfg)

	var prediction *PredictedMatch
	select {
	case p, ok := <-predChan:
		if ok {
			prediction = &p
		}
	case <-time.After(5 * time.Second):
		t.Fatal("predictCandidateOutcomesWithConfig did not complete within 5 seconds")
	}

	// At exactly 60s the gate condition `age < 60` is false → match should be allowed.
	require.NotNil(t, prediction,
		"combat 1v1 with tickets exactly 60s old should be allowed through the gate (condition is strict <60)")
	assert.Equal(t, int8(2), prediction.Size,
		"expected match size 2 for exactly-at-boundary combat 1v1")
}
