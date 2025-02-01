package server

import (
	"encoding/json"
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
						Operator:      OperatorIncrement,
						ResetSchedule: evr.ResetScheduleDaily,
					},
					UserID:      "user1",
					DisplayName: "User One",
					Score:       5,
					Subscore:    0,
				},
				{
					BoardMeta: LeaderboardMeta{
						GroupID:       "group1",
						Mode:          evr.ModeCombatPublic,
						StatName:      "CombatKills",
						Operator:      OperatorIncrement,
						ResetSchedule: evr.ResetScheduleWeekly,
					},
					UserID:      "user1",
					DisplayName: "User One",
					Score:       5,
					Subscore:    0,
				},
				{
					BoardMeta: LeaderboardMeta{
						GroupID:       "group1",
						Mode:          evr.ModeCombatPublic,
						StatName:      "CombatKills",
						Operator:      OperatorIncrement,
						ResetSchedule: evr.ResetScheduleAllTime,
					},
					UserID:      "user1",
					DisplayName: "User One",
					Score:       5,
					Subscore:    0,
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

func TestStatisticsJSONToEntries(t *testing.T) {

	jsonData := `
	{
		"sessionid": "628893F0-C384-4BC0-A9A0-619B96948C6A",
		"matchtype": -3791849610740453517,
		"update": {
			"stats": {
				"arena": {
					"Goals": {
						"op": "add",
						"val": 2
					},
					"AverageTopSpeedPerGame": {
						"op": "rep",
						"val": 5.3596926
					},
					"TopSpeedsTotal": {
						"op": "add",
						"val": 10.719385
					},
					"HighestArenaWinStreak": {
						"op": "max",
						"val": 2
					},
					"ArenaWinPercentage": {
						"op": "rep",
						"val": 100.0
					},
					"ArenaWins": {
						"op": "add",
						"val": 2
					},
					"GoalsPerGame": {
						"op": "rep",
						"val": 1.0
					},
					"Points": {
						"op": "add",
						"val": 4
					},
					"TwoPointGoals": {
						"op": "add",
						"val": 2
					},
					"ShotsOnGoal": {
						"op": "add",
						"val": 2
					},
					"HighestPoints": {
						"op": "max",
						"val": 2
					},
					"GoalScorePercentage": {
						"op": "rep",
						"val": 100.0
					},
					"AveragePossessionTimePerGame": {
						"op": "rep",
						"val": 10.387978
					},
					"PossessionTime": {
						"op": "add",
						"val": 20.775955
					},
					"AveragePointsPerGame": {
						"op": "rep",
						"val": 2.0
					},
					"ArenaMVPPercentage": {
						"op": "rep",
						"val": 50.0
					},
					"ArenaMVPs": {
						"op": "add",
						"val": 1
					},
					"CurrentArenaWinStreak": {
						"op": "add",
						"val": 2
					},
					"CurrentArenaMVPStreak": {
						"op": "add",
						"val": 2
					},
					"HighestArenaMVPStreak": {
						"op": "max",
						"val": 2
					},
					"Level": {
						"op": "add",
						"val": 2
					},
					"XP": {
						"op": "add",
						"val": 1900
					}
				},
				"daily_2025_01_18": {
					"Goals": {
						"op": "add",
						"val": 2
					},
					"AverageTopSpeedPerGame": {
						"op": "rep",
						"val": 5.3596926
					},
					"Level": {
						"op": "add",
						"val": 1
					},
					"XP": {
						"op": "add",
						"val": 2400
					},
					"TopSpeedsTotal": {
						"op": "add",
						"val": 10.719385
					},
					"HighestArenaWinStreak": {
						"op": "max",
						"val": 2
					},
					"ArenaWinPercentage": {
						"op": "rep",
						"val": 100.0
					},
					"ArenaWins": {
						"op": "add",
						"val": 2
					},
					"GoalsPerGame": {
						"op": "rep",
						"val": 1.0
					},
					"Points": {
						"op": "add",
						"val": 4
					},
					"TwoPointGoals": {
						"op": "add",
						"val": 2
					},
					"ShotsOnGoal": {
						"op": "add",
						"val": 2
					},
					"HighestPoints": {
						"op": "max",
						"val": 2
					},
					"GoalScorePercentage": {
						"op": "rep",
						"val": 100.0
					},
					"AveragePossessionTimePerGame": {
						"op": "rep",
						"val": 10.387978
					},
					"PossessionTime": {
						"op": "add",
						"val": 20.775955
					},
					"AveragePointsPerGame": {
						"op": "rep",
						"val": 2.0
					},
					"ArenaMVPPercentage": {
						"op": "rep",
						"val": 50.0
					},
					"ArenaMVPs": {
						"op": "add",
						"val": 1
					},
					"CurrentArenaWinStreak": {
						"op": "add",
						"val": 2
					},
					"CurrentArenaMVPStreak": {
						"op": "add",
						"val": 2
					},
					"HighestArenaMVPStreak": {
						"op": "max",
						"val": 2
					}
				},
				"weekly_2025_01_13": {
					"XP": {
						"op": "add",
						"val": 1450
					},
					"TopSpeedsTotal": {
						"op": "add",
						"val": 5.0818496
					},
					"HighestArenaWinStreak": {
						"op": "max",
						"val": 2
					},
					"ArenaWinPercentage": {
						"op": "rep",
						"val": 100.0
					},
					"ArenaWins": {
						"op": "add",
						"val": 1
					},
					"GoalsPerGame": {
						"op": "rep",
						"val": 1.0
					},
					"Points": {
						"op": "add",
						"val": 2
					},
					"TwoPointGoals": {
						"op": "add",
						"val": 1
					},
					"Goals": {
						"op": "add",
						"val": 1
					},
					"ShotsOnGoal": {
						"op": "add",
						"val": 1
					},
					"HighestPoints": {
						"op": "max",
						"val": 2
					},
					"GoalScorePercentage": {
						"op": "rep",
						"val": 100.0
					},
					"AveragePossessionTimePerGame": {
						"op": "rep",
						"val": 10.387978
					},
					"PossessionTime": {
						"op": "add",
						"val": 7.9363985
					},
					"AverageTopSpeedPerGame": {
						"op": "rep",
						"val": 5.3596926
					},
					"AveragePointsPerGame": {
						"op": "rep",
						"val": 2.0
					},
					"ArenaMVPPercentage": {
						"op": "rep",
						"val": 50.0
					},
					"ArenaMVPs": {
						"op": "add",
						"val": 1
					},
					"CurrentArenaWinStreak": {
						"op": "add",
						"val": 1
					},
					"CurrentArenaMVPStreak": {
						"op": "add",
						"val": 1
					},
					"HighestArenaMVPStreak": {
						"op": "max",
						"val": 2
					}
				}
			}
		}
	}`

	payload := evr.UpdatePayload{}
	if err := json.Unmarshal([]byte(jsonData), &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}

	entries, err := StatisticsToEntries("test_user", "Test User", "test_group", evr.ModeArenaPublic, nil, payload.Update.Statistics.Arena)
	if err != nil {
		t.Fatalf("StatisticsToEntries failed: %v", err)
	}

	// Print or validate entries
	t.Logf("Generated entries: %+v", entries)

}
