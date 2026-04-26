package server

import (
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
)

func TestCombatMatchmakingRules(t *testing.T) {
	modeCombat := evr.ModeCombatPublic.String()
	modeArena := evr.ModeArenaPublic.String()
	cfg := PredictionConfig{UseSnakeDraftFormation: true}

	runTest := func(name string, totalPlayers int, mode string, isParty bool, ageSeconds int, expectMatch bool, expectedSize int) {
		t.Run(name, func(t *testing.T) {
			entries := make([]runtime.MatchmakerEntry, totalPlayers)
			partyTicket := "party-1"
			
			minTeamSize := 1.0
			if mode == modeCombat {
				minTeamSize = 1.0
			}

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

	// Combat Tests (Even only, min 1v1, dynamic delay)
	runTest("Combat 1v1 (New)", 2, modeCombat, false, 10, false, 0)   // 2 < 8 -> BLOCKED
	runTest("Combat 1v1 (Old)", 2, modeCombat, false, 65, true, 2)    // 65s old -> OK
	runTest("Combat 4v4 (New)", 8, modeCombat, false, 10, true, 8)    // 8 >= 8 -> Bypass -> OK
	runTest("Combat 3v4 (7 players, Old)", 7, modeCombat, false, 65, true, 6)  // OK

	// Arena Tests (No delay)
	runTest("Arena 1v1 (New)", 2, modeArena, false, 10, true, 2)     // Should NOT have delay
}
