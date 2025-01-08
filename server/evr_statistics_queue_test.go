package server

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/heroiclabs/nakama/v3/server/evr"
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

		whole, fractional := ValueToScore(test.input)

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

func TestStatisticsToEntries(t *testing.T) {
	tests := []struct {
		userID      string
		displayName string
		groupID     string
		mode        evr.Symbol
		prev        evr.Statistics
		update      evr.Statistics
		expected    []*StatisticsQueueEntry
	}{
		{
			userID:      "user1",
			displayName: "User One",
			groupID:     "group1",
			mode:        evr.ModeCombatPublic,
			prev: &evr.CombatStatistics{
				CombatKills: &evr.StatisticIntegerIncrement{
					IntegerStatistic: evr.IntegerStatistic{
						Count: 1,
						Value: 5,
					},
				},
			},
			update: &evr.CombatStatistics{
				CombatKills: &evr.StatisticIntegerIncrement{
					IntegerStatistic: evr.IntegerStatistic{
						Count: 1,
						Value: 10,
					},
				},
			},
			expected: []*StatisticsQueueEntry{
				{
					BoardMeta: LeaderboardMeta{
						GroupID:       "group1",
						Mode:          evr.ModeCombatPublic,
						StatName:      "CombatKills",
						Operator:      LeaderboardOperatorIncrement,
						ResetSchedule: evr.ResetScheduleDaily,
					},
					UserID:      "user1",
					DisplayName: "User One",
					Score:       5,
					Subscore:    0,
					Override:    2,
				},
				{
					BoardMeta: LeaderboardMeta{
						GroupID:       "group1",
						Mode:          evr.ModeCombatPublic,
						StatName:      "CombatKills",
						Operator:      LeaderboardOperatorIncrement,
						ResetSchedule: evr.ResetScheduleWeekly,
					},
					UserID:      "user1",
					DisplayName: "User One",
					Score:       5,
					Subscore:    0,
					Override:    2,
				},
				{
					BoardMeta: LeaderboardMeta{
						GroupID:       "group1",
						Mode:          evr.ModeCombatPublic,
						StatName:      "CombatKills",
						Operator:      LeaderboardOperatorIncrement,
						ResetSchedule: evr.ResetScheduleAllTime,
					},
					UserID:      "user1",
					DisplayName: "User One",
					Score:       5,
					Subscore:    0,
					Override:    2,
				},
			},
		},
	}

	for _, test := range tests {
		entries, err := StatisticsToEntries(test.userID, test.displayName, test.groupID, test.mode, test.prev, test.update)
		if err != nil {
			t.Errorf("StatisticsToEntries returned an error: %v", err)
		}

		if cmp.Diff(entries, test.expected) != "" {
			t.Errorf("StatisticsToEntries returned unexpected entries: %v", cmp.Diff(entries, test.expected))
		}
	}
}
