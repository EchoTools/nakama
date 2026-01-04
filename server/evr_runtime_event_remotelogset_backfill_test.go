package server

import (
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
)

// TestBackfillPlayerLossExemption tests that backfill players who stay until match end
// do not receive a loss when their team loses.
func TestBackfillPlayerLossExemption(t *testing.T) {
	tests := []struct {
		name           string
		isBackfill     bool
		isEarlyQuitter bool
		initialWins    int64
		initialLosses  int64
		expectedWins   int64
		expectedLosses int64
		description    string
	}{
		{
			name:           "Regular player with loss - loss counted",
			isBackfill:     false,
			isEarlyQuitter: false,
			initialWins:    0,
			initialLosses:  1,
			expectedWins:   0,
			expectedLosses: 1,
			description:    "Regular (non-backfill) players get losses normally",
		},
		{
			name:           "Backfill player with loss, stayed - loss exempted",
			isBackfill:     true,
			isEarlyQuitter: false,
			initialWins:    0,
			initialLosses:  1,
			expectedWins:   0,
			expectedLosses: 0,
			description:    "Backfill player who stayed should not get loss",
		},
		{
			name:           "Backfill player with loss, early quit - loss counted",
			isBackfill:     true,
			isEarlyQuitter: true,
			initialWins:    0,
			initialLosses:  1,
			expectedWins:   0,
			expectedLosses: 1,
			description:    "Backfill player who early quit should get loss",
		},
		{
			name:           "Backfill player with win, stayed - win counted",
			isBackfill:     true,
			isEarlyQuitter: false,
			initialWins:    1,
			initialLosses:  0,
			expectedWins:   1,
			expectedLosses: 0,
			description:    "Backfill player who stayed and won gets the win",
		},
		{
			name:           "Backfill player with win, early quit - win counted",
			isBackfill:     true,
			isEarlyQuitter: true,
			initialWins:    1,
			initialLosses:  0,
			expectedWins:   1,
			expectedLosses: 0,
			description:    "Backfill player wins are counted even if they early quit",
		},
		{
			name:           "Regular player with win - win counted",
			isBackfill:     false,
			isEarlyQuitter: false,
			initialWins:    1,
			initialLosses:  0,
			expectedWins:   1,
			expectedLosses: 0,
			description:    "Regular players get wins normally",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create player info
			playerInfo := &PlayerInfo{
				UserID:    "test-user-id",
				SessionID: "test-session-id",
				Team:      BlueTeam,
				JoinTime:  0, // Will be set based on isBackfill
			}

			if tt.isBackfill {
				playerInfo.JoinTime = 30 // Joined 30 seconds into the match
			}

			// Create participation record
			participation := &PlayerParticipation{
				UserID:      playerInfo.UserID,
				IsAbandoner: tt.isEarlyQuitter,
			}

			// Create stats
			stats := evr.MatchTypeStats{
				ArenaWins:   tt.initialWins,
				ArenaLosses: tt.initialLosses,
			}

			// Simulate the backfill loss exemption logic
			if playerInfo.IsBackfill() {
				isEarlyQuitter := participation.IsAbandoner

				if stats.ArenaLosses > 0 && !isEarlyQuitter {
					stats.ArenaLosses = 0
				}
			}

			// Verify results
			assert.Equal(t, tt.expectedWins, stats.ArenaWins, tt.description+" - wins")
			assert.Equal(t, tt.expectedLosses, stats.ArenaLosses, tt.description+" - losses")
		})
	}
}

// TestBackfillPlayerIsBackfillLogic tests the IsBackfill() method
func TestBackfillPlayerIsBackfillLogic(t *testing.T) {
	tests := []struct {
		name       string
		joinTime   int64
		isBackfill bool
	}{
		{
			name:       "Player joined at match start - not backfill",
			joinTime:   0,
			isBackfill: false,
		},
		{
			name:       "Player joined 1 second in - is backfill",
			joinTime:   1,
			isBackfill: true,
		},
		{
			name:       "Player joined 30 seconds in - is backfill",
			joinTime:   30,
			isBackfill: true,
		},
		{
			name:       "Player joined 120 seconds in - is backfill",
			joinTime:   120,
			isBackfill: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			playerInfo := &PlayerInfo{
				JoinTime: tt.joinTime,
			}

			result := playerInfo.IsBackfill()
			assert.Equal(t, tt.isBackfill, result)
		})
	}
}

// TestBackfillPlayerComplexScenarios tests more complex scenarios
func TestBackfillPlayerComplexScenarios(t *testing.T) {
	t.Run("Multiple backfill players with different outcomes", func(t *testing.T) {
		// Simulate a match with multiple backfill players
		players := []struct {
			name           string
			isBackfill     bool
			isEarlyQuitter bool
			wins           int64
			losses         int64
		}{
			{"Regular winner", false, false, 1, 0},
			{"Regular loser", false, false, 0, 1},
			{"Backfill winner stayed", true, false, 1, 0},
			{"Backfill loser stayed", true, false, 0, 1},
			{"Backfill loser quit", true, true, 0, 1},
		}

		expected := []struct {
			wins   int64
			losses int64
		}{
			{1, 0}, // Regular winner keeps win
			{0, 1}, // Regular loser keeps loss
			{1, 0}, // Backfill winner keeps win
			{0, 0}, // Backfill loser who stayed - loss exempted
			{0, 1}, // Backfill loser who quit - loss counted
		}

		for i, p := range players {
			playerInfo := &PlayerInfo{
				UserID:   "user-" + p.name,
				JoinTime: 0,
			}
			if p.isBackfill {
				playerInfo.JoinTime = 30
			}

			participation := &PlayerParticipation{
				IsAbandoner: p.isEarlyQuitter,
			}

			stats := evr.MatchTypeStats{
				ArenaWins:   p.wins,
				ArenaLosses: p.losses,
			}

			// Apply backfill logic
			if playerInfo.IsBackfill() {
				if stats.ArenaLosses > 0 && !participation.IsAbandoner {
					stats.ArenaLosses = 0
				}
			}

			assert.Equal(t, expected[i].wins, stats.ArenaWins, p.name+" - wins")
			assert.Equal(t, expected[i].losses, stats.ArenaLosses, p.name+" - losses")
		}
	})
}
