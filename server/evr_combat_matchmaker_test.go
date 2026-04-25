package server

import (
	"testing"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
)

func TestCombatMatchmakingRules(t *testing.T) {
	modeCombat := evr.ModeCombatPublic.String()
	modeArena := evr.ModeArenaPublic.String()
	cfg := PredictionConfig{UseSnakeDraftFormation: true}

	runTest := func(name string, totalPlayers int, mode string, isParty bool, expectMatch bool, expectedSize int) {
		t.Run(name, func(t *testing.T) {
			entries := make([]runtime.MatchmakerEntry, totalPlayers)
			partyTicket := "party-1"
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
						"max_team_size":  float64(5),
						"count_multiple": float64(1),
						"timestamp":      float64(123456789),
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
			}

			if expectMatch {
				assert.NotNil(t, prediction, "Should have created a match")
				if prediction != nil {
					assert.Equal(t, int8(expectedSize), prediction.Size, "Match size should match expectation")
				}
			} else {
				assert.Nil(t, prediction, "Should NOT have created a match")
			}
		})
	}

	// Combat Tests
	runTest("Combat 1v1 (Solo)", 2, modeCombat, false, true, 2)
	runTest("Combat 2v2 (Party of 4)", 4, modeCombat, true, true, 4)  // Should split
	runTest("Combat 3v4 (7 players)", 7, modeCombat, false, true, 7)  // 3v4 allowed
	runTest("Combat 1v2 (3 players)", 3, modeCombat, false, false, 0) // 1v2 blocked (teams < 3)
	runTest("Combat 2v3 (5 players)", 5, modeCombat, false, false, 0) // 2v3 blocked (teams < 3)

	// Arena Tests
	runTest("Arena 1v1 (Solo)", 2, modeArena, false, true, 2)
	runTest("Arena Party of 4 Matching Alone", 4, modeArena, true, false, 0) // Should NOT split, thus no match (4v0 rejected)
	runTest("Arena 3v4 (7 players)", 7, modeArena, false, false, 0)          // Uneven blocked
}
