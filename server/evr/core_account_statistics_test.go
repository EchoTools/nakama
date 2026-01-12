package evr

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestStatisticsGroup_MarshalText(t *testing.T) {
	tests := []struct {
		name     string
		group    StatisticsGroup
		expected string
	}{
		{
			name: "Daily Arena",
			group: StatisticsGroup{
				Mode:          ModeArenaPublic,
				ResetSchedule: ResetScheduleDaily,
			},
			expected: "daily_" + time.Now().Format("2006_01_02"),
		},
		{
			name: "Weekly Arena",
			group: StatisticsGroup{
				Mode:          ModeArenaPublic,
				ResetSchedule: ResetScheduleWeekly,
			},
			expected: "weekly_" + mostRecentThursday().Format("2006_01_02"),
		},
		{
			name: "All Time Arena",
			group: StatisticsGroup{
				Mode:          ModeArenaPublic,
				ResetSchedule: ResetScheduleAllTime,
			},
			expected: "arena",
		},
		{
			name: "All Time Combat",
			group: StatisticsGroup{
				Mode:          ModeCombatPublic,
				ResetSchedule: ResetScheduleAllTime,
			},
			expected: "combat",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.group.MarshalText()
			if err != nil {
				t.Errorf("MarshalText() error = %v", err)
				return
			}

			gotStr := string(got)
			if gotStr != tt.expected {
				t.Errorf("MarshalText() got = %v, expected %v", gotStr, tt.expected)
			}
		})
	}
}
func TestStatisticsGroup_UnmarshalText(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected StatisticsGroup
	}{
		{
			name:  "Daily Arena",
			input: "daily_" + time.Now().Format("2006_01_02"),
			expected: StatisticsGroup{
				Mode:          ModeArenaPublic,
				ResetSchedule: ResetScheduleDaily,
			},
		},
		{
			name:  "Weekly Arena",
			input: "weekly_" + mostRecentThursday().Format("2006_01_02"),
			expected: StatisticsGroup{
				Mode:          ModeArenaPublic,
				ResetSchedule: ResetScheduleWeekly,
			},
		},
		{
			name:  "All Time Arena",
			input: "arena",
			expected: StatisticsGroup{
				Mode:          ModeArenaPublic,
				ResetSchedule: ResetScheduleAllTime,
			},
		},
		{
			name:  "All Time Combat",
			input: "combat",
			expected: StatisticsGroup{
				Mode:          ModeCombatPublic,
				ResetSchedule: ResetScheduleAllTime,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var group StatisticsGroup
			err := group.UnmarshalText([]byte(tt.input))
			if err != nil {
				t.Errorf("UnmarshalText() error = %v", err)
				return
			}

			if group != tt.expected {
				t.Errorf("UnmarshalText() got = %v, expected %v", group, tt.expected)
			}
		})
	}
}

func TestFullStatisticsGeneration(t *testing.T) {

	var interfaceStats map[string]any
	err := json.Unmarshal(preshutdownProfileStatistics, &interfaceStats)
	if err != nil {
		t.Errorf("json.Unmarshal() error = %v", err)
		return
	}

	want, err := json.MarshalIndent(interfaceStats, "", "  ")
	if err != nil {
		t.Errorf("json.Marshal() error = %v", err)
		return
	}

	stats := ArenaStatistics{}
	if err := json.Unmarshal(preshutdownProfileStatistics, &stats); err != nil {
		t.Errorf("json.Unmarshal() error = %v", err)
		return
	}

	got, err := json.MarshalIndent(&stats, "", "  ")
	if err != nil {
		t.Errorf("json.Marshal() error = %v", err)
		return
	}

	if cmp.Diff(string(got), string(want)) != "" {
		t.Errorf("MarshalJSON() want/got (-/+) = %v", cmp.Diff(string(got), string(want)))
	}

}

func TestArenaStatistics_MarshalJSON_IncludesOp(t *testing.T) {
	stats := &ArenaStatistics{
		ArenaWins: &StatisticValue{
			Count: 1,
			Value: 10,
		},
	}

	data, err := json.Marshal(stats)
	if err != nil {
		t.Fatalf("Failed to marshal ArenaStatistics: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	wins, ok := result["ArenaWins"].(map[string]interface{})
	if !ok {
		t.Fatalf("ArenaWins not found or invalid format")
	}

	if op, ok := wins["op"].(string); !ok || op != "add" {
		t.Errorf("Expected op='add', got '%v'", wins["op"])
	}

	if cnt, ok := wins["cnt"].(float64); !ok || cnt != 1 {
		t.Errorf("Expected cnt=1, got %v", wins["cnt"])
	}

	if val, ok := wins["val"].(float64); !ok || val != 10 {
		t.Errorf("Expected val=10, got %v", wins["val"])
	}
}

func TestArenaStatistics_EarlyQuitsFoldIntoLosses(t *testing.T) {
	// Test with countEarlyQuitsAsLosses = true
	t.Run("WithEarlyQuitsAsLosses", func(t *testing.T) {
		stats := &ArenaStatistics{
			ArenaWins:   &StatisticValue{Count: 1, Value: 10},
			ArenaLosses: &StatisticValue{Count: 1, Value: 5},
			EarlyQuits:  &StatisticValue{Count: 1, Value: 2},
		}

		stats.CalculateFieldsWithOptions(true)

		if stats.ArenaLosses == nil {
			t.Fatalf("expected ArenaLosses to be set")
		}

		// ArenaLosses should include early quits: 5 + 2 = 7
		if got := stats.ArenaLosses.GetValue(); got != 7 {
			t.Fatalf("expected ArenaLosses to include early quits, got %v", got)
		}

		if stats.GamesPlayed == nil {
			t.Fatalf("expected GamesPlayed to be calculated")
		}

		// GamesPlayed = ArenaWins + ArenaLosses (which includes early quits) = 10 + 7 = 17
		if got := stats.GamesPlayed.GetValue(); got != 17 {
			t.Fatalf("expected GamesPlayed to include early quits as losses, got %v", got)
		}

		const tolerance = 1e-6
		expectedWinPct := 10.0 / 17.0 * 100
		if stats.ArenaWinPercentage == nil {
			t.Fatalf("expected ArenaWinPercentage to be calculated")
		}

		if diff := math.Abs(stats.ArenaWinPercentage.GetValue() - expectedWinPct); diff > tolerance {
			t.Fatalf("unexpected ArenaWinPercentage: got %v, want %v", stats.ArenaWinPercentage.GetValue(), expectedWinPct)
		}

		expectedEarlyQuitPct := 2.0 / 17.0 * 100
		if stats.EarlyQuitPercentage == nil {
			t.Fatalf("expected EarlyQuitPercentage to be calculated")
		}

		if diff := math.Abs(stats.EarlyQuitPercentage.GetValue() - expectedEarlyQuitPct); diff > tolerance {
			t.Fatalf("unexpected EarlyQuitPercentage: got %v, want %v", stats.EarlyQuitPercentage.GetValue(), expectedEarlyQuitPct)
		}

		if stats.ArenaTies == nil || stats.ArenaTies.GetValue() != 2 {
			t.Fatalf("expected ArenaTies to reflect EarlyQuits value (2), got %v", stats.ArenaTies)
		}
	})

	// Test with countEarlyQuitsAsLosses = false (default behavior)
	t.Run("WithoutEarlyQuitsAsLosses", func(t *testing.T) {
		stats := &ArenaStatistics{
			ArenaWins:   &StatisticValue{Count: 1, Value: 10},
			ArenaLosses: &StatisticValue{Count: 1, Value: 5},
			EarlyQuits:  &StatisticValue{Count: 1, Value: 2},
		}

		stats.CalculateFields() // Same as CalculateFieldsWithOptions(false)

		if stats.ArenaLosses == nil {
			t.Fatalf("expected ArenaLosses to be set")
		}

		// ArenaLosses should NOT include early quits: still 5
		if got := stats.ArenaLosses.GetValue(); got != 5 {
			t.Fatalf("expected ArenaLosses to remain unchanged (5), got %v", got)
		}

		if stats.GamesPlayed == nil {
			t.Fatalf("expected GamesPlayed to be calculated")
		}

		// GamesPlayed = ArenaWins + ArenaLosses = 10 + 5 = 15
		if got := stats.GamesPlayed.GetValue(); got != 15 {
			t.Fatalf("expected GamesPlayed = 15, got %v", got)
		}

		const tolerance = 1e-6
		expectedWinPct := 10.0 / 15.0 * 100
		if stats.ArenaWinPercentage == nil {
			t.Fatalf("expected ArenaWinPercentage to be calculated")
		}

		if diff := math.Abs(stats.ArenaWinPercentage.GetValue() - expectedWinPct); diff > tolerance {
			t.Fatalf("unexpected ArenaWinPercentage: got %v, want %v", stats.ArenaWinPercentage.GetValue(), expectedWinPct)
		}

		// ArenaTies should still sync with EarlyQuits
		if stats.ArenaTies == nil || stats.ArenaTies.GetValue() != 2 {
			t.Fatalf("expected ArenaTies to reflect EarlyQuits value (2), got %v", stats.ArenaTies)
		}

		// EarlyQuitPercentage should be calculated based on original gamesPlayed (15)
		expectedEarlyQuitPct := 2.0 / 15.0 * 100
		if stats.EarlyQuitPercentage == nil {
			t.Fatalf("expected EarlyQuitPercentage to be calculated")
		}

		if diff := math.Abs(stats.EarlyQuitPercentage.GetValue() - expectedEarlyQuitPct); diff > tolerance {
			t.Fatalf("unexpected EarlyQuitPercentage: got %v, want %v", stats.EarlyQuitPercentage.GetValue(), expectedEarlyQuitPct)
		}
	})

	// Test with EarlyQuits initially nil to verify initialization logic
	t.Run("WithNilEarlyQuits", func(t *testing.T) {
		stats := &ArenaStatistics{
			ArenaWins:   &StatisticValue{Count: 1, Value: 10},
			ArenaLosses: &StatisticValue{Count: 1, Value: 5},
			// EarlyQuits is nil
		}

		stats.CalculateFields()

		// EarlyQuits should be initialized to 0
		if stats.EarlyQuits == nil {
			t.Fatalf("expected EarlyQuits to be initialized")
		}
		if stats.EarlyQuits.GetValue() != 0 {
			t.Fatalf("expected EarlyQuits to be 0, got %v", stats.EarlyQuits.GetValue())
		}

		// ArenaTies should also be initialized to 0 (synced with EarlyQuits)
		if stats.ArenaTies == nil {
			t.Fatalf("expected ArenaTies to be initialized")
		}
		if stats.ArenaTies.GetValue() != 0 {
			t.Fatalf("expected ArenaTies to be 0, got %v", stats.ArenaTies.GetValue())
		}

		// GamesPlayed should be 15 (no early quits added)
		if stats.GamesPlayed == nil || stats.GamesPlayed.GetValue() != 15 {
			t.Fatalf("expected GamesPlayed = 15, got %v", stats.GamesPlayed)
		}

		// ArenaLosses should remain unchanged
		if stats.ArenaLosses.GetValue() != 5 {
			t.Fatalf("expected ArenaLosses to remain 5, got %v", stats.ArenaLosses.GetValue())
		}
	})
}

var preshutdownProfileStatistics = []byte(`
{
	"ArenaLosses": {
		"cnt": 1,
		"op": "add",
		"val": 313
	},
	"ArenaMVPPercentage": {
		"cnt": 1,
		"op": "rep",
		"val": 8.762887
	},
	"ArenaMVPs": {
		"cnt": 1,
		"op": "add",
		"val": 51
	},
	"ArenaTies": {
		"cnt": 1,
		"op": "add",
		"val": 0
	},
	"ArenaWinPercentage": {
		"cnt": 1,
		"op": "rep",
		"val": 46.219933
	},
	"ArenaWins": {
		"cnt": 1,
		"op": "add",
		"val": 269
	},
	"Assists": {
		"cnt": 1,
		"op": "add",
		"val": 139
	},
	"AssistsPerGame": {
		"cnt": 515,
		"op": "avg",
		"val": 107.96878
	},
	"AveragePointsPerGame": {
		"cnt": 522,
		"op": "avg",
		"val": 624.45679
	},
	"AveragePossessionTimePerGame": {
		"cnt": 566,
		"op": "avg",
		"val": 14895.59
	},
	"AverageTopSpeedPerGame": {
		"cnt": 573,
		"op": "avg",
		"val": 24027.426
	},
	"BlockPercentage": {
		"cnt": 1,
		"op": "rep",
		"val": 0.088626295
	},
	"Blocks": {
		"cnt": 1,
		"op": "add",
		"val": 3
	},
	"BounceGoals": {
		"cnt": 1,
		"op": "add",
		"val": 32
	},
	"Catches": {
		"cnt": 1,
		"op": "add",
		"val": 752
	},
	"Clears": {
		"cnt": 1,
		"op": "add",
		"val": 958
	},
	"CurrentArenaMVPStreak": {
		"cnt": 1,
		"op": "add",
		"val": 30
	},
	"CurrentArenaWinStreak": {
		"cnt": 1,
		"op": "add",
		"val": 157
	},
	"Goals": {
		"cnt": 1,
		"op": "add",
		"val": 260
	},
	"GoalSavePercentage": {
		"cnt": 1,
		"op": "rep",
		"val": 8.0425386
	},
	"GoalScorePercentage": {
		"cnt": 1,
		"op": "rep",
		"val": 40.498444
	},
	"GoalsPerGame": {
		"cnt": 522,
		"op": "avg",
		"val": 249.85077
	},
	"HatTricks": {
		"cnt": 1,
		"op": "add",
		"val": 11
	},
	"HighestArenaMVPStreak": {
		"cnt": 1,
		"op": "max",
		"val": 30
	},
	"HighestArenaWinStreak": {
		"cnt": 1,
		"op": "max",
		"val": 157
	},
	"HighestPoints": {
		"cnt": 1,
		"op": "max",
		"val": 12
	},
	"HighestSaves": {
		"cnt": 1,
		"op": "max",
		"val": 3
	},
	"HighestStuns": {
		"cnt": 1,
		"op": "max",
		"val": 27
	},
	"Interceptions": {
		"cnt": 1,
		"op": "add",
		"val": 471
	},
	"JoustsWon": {
		"cnt": 1,
		"op": "add",
		"val": 0
	},
	"Level": {
		"cnt": 1,
		"op": "add",
		"val": 50
	},
	"OnePointGoals": {
		"cnt": 1,
		"op": "add",
		"val": 0
	},
	"Passes": {
		"cnt": 1,
		"op": "add",
		"val": 662
	},
	"Points": {
		"cnt": 1,
		"op": "add",
		"val": 630
	},
	"PossessionTime": {
		"cnt": 1,
		"op": "add",
		"val": 15164.576
	},
	"PunchesReceived": {
		"cnt": 1,
		"op": "add",
		"val": 3385
	},
	"Saves": {
		"cnt": 1,
		"op": "add",
		"val": 242
	},
	"SavesPerGame": {
		"cnt": 521,
		"op": "avg",
		"val": 235.14339
	},
	"ShotsOnGoal": {
		"cnt": 1,
		"op": "add",
		"val": 642
	},
	"ShotsOnGoalAgainst": {
		"cnt": 1,
		"op": "add",
		"val": 3009
	},
	"Steals": {
		"cnt": 1,
		"op": "add",
		"val": 234
	},
	"StunPercentage": {
		"cnt": 1,
		"op": "rep",
		"val": 0
	},
	"Stuns": {
		"cnt": 1,
		"op": "add",
		"val": 4632
	},
	"StunsPerGame": {
		"cnt": 573,
		"op": "avg",
		"val": 4961.7314
	},
	"ThreePointGoals": {
		"cnt": 1,
		"op": "add",
		"val": 110
	},
	"TopSpeedsTotal": {
		"cnt": 1,
		"op": "add",
		"val": 22009.957
	},
	"TwoPointGoals": {
		"cnt": 1,
		"op": "add",
		"val": 150
	},
	"XP": {
		"cnt": 1,
		"op": "add",
		"val": 199
	}
}
`)
