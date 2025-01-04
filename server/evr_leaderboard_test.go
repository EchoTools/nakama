package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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
		result := ScoreToValue(test.score, test.subscore)

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
