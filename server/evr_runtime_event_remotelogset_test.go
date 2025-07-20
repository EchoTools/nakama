package server

import (
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
)

type dummyStats struct {
	Goals   int64   `json:"goals" op:"add"`
	Saves   uint64  `json:"saves" op:"add"`
	Rating  float64 `json:"rating" op:"avg"`
	ZeroVal int64   `json:"zero_val" op:"add"`
}

func TestTypeStatsToScoreMap_AllFieldsSet(t *testing.T) {

	// ArenaStatistics fields must match dummyStats for this test to work
	// so we use evr.MatchTypeStats as dummyStats for the actual code
	// but here we test the logic with a similar struct

	// Use evr.ArenaStatistics for real test
	arenaStats := evr.ArenaStatistics{
		ArenaWins:   &evr.StatisticValue{Value: 10},
		ArenaLosses: &evr.StatisticValue{Value: 1},
		Goals:       &evr.StatisticValue{Value: 5},
		Saves:       &evr.StatisticValue{Value: 2},
		Level:       &evr.StatisticValue{Value: 4},
	}

	matchTypeStats := evr.MatchTypeStats{
		ArenaWins:   int64(arenaStats.ArenaWins.Value),
		ArenaLosses: int64(arenaStats.ArenaLosses.Value),
		Goals:       int64(arenaStats.Goals.Value),
		Saves:       int64(arenaStats.Saves.Value),
	}

	userID := "user123"
	displayName := "TestUser"
	groupID := "group456"
	mode := evr.ModeArenaPublic

	entries, err := typeStatsToScoreMap(userID, displayName, groupID, mode, matchTypeStats)
	assert.NoError(t, err)
	assert.NotEmpty(t, entries)

	// Each non-zero stat should produce 3 entries (daily, weekly, alltime)
	expectedStats := []string{"goals", "saves", "rating"}
	for _, stat := range expectedStats {
		count := 0
		for _, entry := range entries {
			if entry.BoardMeta.StatName == stat {
				count++
				assert.Equal(t, userID, entry.UserID)
				assert.Equal(t, displayName, entry.DisplayName)
				assert.Equal(t, groupID, entry.BoardMeta.GroupID)
				assert.Equal(t, mode, entry.BoardMeta.Mode)
				assert.True(t, entry.Score > 0)
			}
		}
		assert.Equal(t, 3, count, "Each stat should have 3 entries for reset schedules")
	}
}
