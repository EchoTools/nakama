package server

import (
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
)

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

	wantedEntries := []*StatisticsQueueEntry{
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "ArenaLosses",
				Operator:      "set",
				ResetSchedule: "daily",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       1000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "ArenaLosses",
				Operator:      "set",
				ResetSchedule: "weekly",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       1000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "ArenaLosses",
				Operator:      "set",
				ResetSchedule: "alltime",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       1000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "ArenaWins",
				Operator:      "set",
				ResetSchedule: "daily",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       10000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "ArenaWins",
				Operator:      "set",
				ResetSchedule: "weekly",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       10000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "ArenaWins",
				Operator:      "set",
				ResetSchedule: "alltime",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       10000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "Goals",
				Operator:      "set",
				ResetSchedule: "daily",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       5000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "Goals",
				Operator:      "set",
				ResetSchedule: "weekly",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       5000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "Goals",
				Operator:      "set",
				ResetSchedule: "alltime",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       5000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "Saves",
				Operator:      "set",
				ResetSchedule: "daily",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       2000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "Saves",
				Operator:      "set",
				ResetSchedule: "weekly",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       2000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "Saves",
				Operator:      "set",
				ResetSchedule: "alltime",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       2000000000,
			Subscore:    0,
			Metadata:    nil,
		},
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

	assert.Equal(t, len(entries), 12, "There should be 12 entries for all stats and reset schedules")
	// Each non-zero stat should produce 3 entries (daily, weekly, alltime)

	for i, entry := range entries {
		assert.Equal(t, wantedEntries[i].BoardMeta, entry.BoardMeta, "BoardMeta mismatch at entry %d", i)
		assert.Equal(t, wantedEntries[i].UserID, entry.UserID, "UserID mismatch at entry %d", i)
		assert.Equal(t, wantedEntries[i].DisplayName, entry.DisplayName, "DisplayName mismatch at entry %d", i)
		assert.Equal(t, wantedEntries[i].Score, entry.Score, "Score mismatch at entry %d", i)
		assert.Equal(t, wantedEntries[i].Subscore, entry.Subscore, "Subscore mismatch at entry %d", i)
		assert.Equal(t, wantedEntries[i].Metadata, entry.Metadata, "Metadata mismatch at entry %d", i)
	}

}
