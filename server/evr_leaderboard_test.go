package server

import (
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
)

func TestCreatePlayedStat(t *testing.T) {

	tests := []struct {
		name     string
		stats    map[string]evr.StatUpdate
		expected float64
	}{
		{
			name: "Both ArenaWins and ArenaLosses present",
			stats: map[string]evr.StatUpdate{
				"ArenaWins":   {Operator: "add", Value: 5},
				"ArenaLosses": {Operator: "add", Value: 3},
			},
			expected: 8,
		},
		{
			name: "Only ArenaWins present",
			stats: map[string]evr.StatUpdate{
				"ArenaWins": {Operator: "add", Value: 5},
			},
			expected: 5,
		},
		{
			name: "Only ArenaLosses present",
			stats: map[string]evr.StatUpdate{
				"ArenaLosses": {Operator: "add", Value: 3},
			},
			expected: 3,
		},
		{
			name:     "Neither ArenaWins nor ArenaLosses present",
			stats:    map[string]evr.StatUpdate{},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := &LeaderboardRegistry{}
			registry.createGamesPlayedStat(tt.stats, "Arena")

			assert.Equal(t, tt.expected, tt.stats["GamesPlayed"].Value)
			if tt.expected != 0 {
				assert.Equal(t, "add", tt.stats["GamesPlayed"].Operator)
			}
		})
	}
}

func TestValueToScore(t *testing.T) {
	tests := []struct {
		input              float64
		expectedWhole      int64
		expectedFractional int64
	}{
		{6.6134934, 6, 6134934},
		{8.1665173, 8, 1665173},
		{650, 650, 0},
		{6.25, 6, 25},
	}

	for _, test := range tests {

		whole, fractional := (&LeaderboardRegistry{}).valueToScore(test.input)

		if whole != test.expectedWhole || fractional != test.expectedFractional {
			t.Errorf("splitFloat64(%f) = (%d, %d); expected (%d, %d)", test.input, whole, fractional, test.expectedWhole, test.expectedFractional)
		}
	}
}
func TestScoreToValue(t *testing.T) {
	tests := []struct {
		score    int64
		subscore int64
		expected float64
	}{
		{6, 6134934, 6.6134934},
		{8, 1665173, 8.1665173},
		{650, 0, 650.0},
		{6, 25, 6.25},
	}

	for _, test := range tests {
		result := (&LeaderboardRegistry{}).scoreToValue(test.score, test.subscore)

		if result != test.expected {
			t.Errorf("ScoreToValue(%d, %d) = %f; expected %f", test.score, test.subscore, result, test.expected)
		}
	}
}
func TestLeaderboardMetaFromStat(t *testing.T) {
	tests := []struct {
		name          string
		statName      string
		group         string
		operator      string
		expectedOrder string
		expectedOp    string
		expectedReset string
	}{
		{

			name:          "Daily add operator",
			statName:      "ArenaWins",
			group:         "daily_group",
			operator:      "add",
			expectedOrder: "desc",
			expectedOp:    "incr",
			expectedReset: "0 0 * * *",
		},
		{
			name:          "Weekly max operator",
			statName:      "ArenaWins",
			group:         "weekly_group",
			operator:      "max",
			expectedOrder: "desc",
			expectedOp:    "best",
			expectedReset: "0 0 * * 1",
		},
		{

			name:          "Invalid operator",
			statName:      "ArenaWins",
			group:         "daily_group",
			operator:      "",
			expectedOrder: "desc",
			expectedOp:    "set",
			expectedReset: "0 0 * * *",
		},
		{

			name:          "Losses stat name",
			statName:      "ArenaLosses",
			group:         "daily_group",
			operator:      "add",
			expectedOrder: "asc",
			expectedOp:    "incr",
			expectedReset: "0 0 * * *",
		},
		{

			name:          "No periodicity",
			statName:      "ArenaWins",
			group:         "group",
			operator:      "rep",
			expectedOrder: "desc",
			expectedOp:    "set",
			expectedReset: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := &LeaderboardRegistry{}
			operator, sortOrder, resetSchedule := registry.leaderboardConfig(tt.statName, tt.operator, tt.group)

			assert.Equal(t, tt.expectedOp, operator)
			assert.Equal(t, tt.expectedOrder, sortOrder)
			assert.Equal(t, tt.expectedReset, resetSchedule)

		})
	}
}
