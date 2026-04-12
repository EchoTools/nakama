package server

import (
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

func TestComputeLevelFromXP(t *testing.T) {
	table := DefaultXPProgressionTable()

	tests := []struct {
		name            string
		totalXP         float64
		expectedLevel   int
		expectedXP      float64 // remainder toward next level
	}{
		{
			name:          "zero XP is level 1",
			totalXP:       0,
			expectedLevel: 1,
			expectedXP:    0,
		},
		{
			name:          "499 XP is level 1 with 499 remainder",
			totalXP:       499,
			expectedLevel: 1,
			expectedXP:    499,
		},
		{
			name:          "500 XP is level 2 with 0 remainder",
			totalXP:       500,
			expectedLevel: 2,
			expectedXP:    0,
		},
		{
			name:          "501 XP is level 2 with 1 remainder",
			totalXP:       501,
			expectedLevel: 2,
			expectedXP:    1,
		},
		{
			name:          "1500 XP is level 3 (500+1000)",
			totalXP:       1500,
			expectedLevel: 3,
			expectedXP:    0,
		},
		{
			name:          "level 10 boundary (500+1000+1500+2000+2500+3000+3500+4000+4500 = 22500)",
			totalXP:       22500,
			expectedLevel: 10,
			expectedXP:    0,
		},
		{
			name:          "level 11 starts at 22500, needs 5000",
			totalXP:       22500 + 4999,
			expectedLevel: 10,
			expectedXP:    4999,
		},
		{
			name:          "level 11 exact",
			totalXP:       22500 + 5000,
			expectedLevel: 11,
			expectedXP:    0,
		},
		{
			// Levels 1-9: 500+1000+1500+2000+2500+3000+3500+4000+4500 = 21500
			// Levels 10-19: 5000*10 = 50000 → cumulative 71500
			// Levels 20-29: 7500*10 = 75000 → cumulative 146500
			// Level 27 needs: 21500 + 50000 + 7500*7 = 21500 + 50000 + 52500 = 124000
			// XP at 130950: 130950 - 124000 = 6950 into level 27, next level needs 7500
			// Wait, let me recalculate:
			// Level 1→2: 500 (cum: 500)
			// Level 2→3: 1000 (cum: 1500)
			// Level 3→4: 1500 (cum: 3000)
			// Level 4→5: 2000 (cum: 5000)
			// Level 5→6: 2500 (cum: 7500)
			// Level 6→7: 3000 (cum: 10500)
			// Level 7→8: 3500 (cum: 14000)
			// Level 8→9: 4000 (cum: 18000)
			// Level 9→10: 4500 (cum: 22500)
			// Level 10→11: 5000 (cum: 27500)
			// Level 11→12: 5000 (cum: 32500)
			// Level 12→13: 5000 (cum: 37500)
			// Level 13→14: 5000 (cum: 42500)
			// Level 14→15: 5000 (cum: 47500)
			// Level 15→16: 5000 (cum: 52500)
			// Level 16→17: 5000 (cum: 57500)
			// Level 17→18: 5000 (cum: 62500)
			// Level 18→19: 5000 (cum: 67500)
			// Level 19→20: 5000 (cum: 72500)
			// Level 20→21: 7500 (cum: 80000)
			// Level 21→22: 7500 (cum: 87500)
			// Level 22→23: 7500 (cum: 95000)
			// Level 23→24: 7500 (cum: 102500)
			// Level 24→25: 7500 (cum: 110000)
			// Level 25→26: 7500 (cum: 117500)
			// Level 26→27: 7500 (cum: 125000)
			// 130950 - 125000 = 5950 remainder, level 27
			name:          "130950 XP is level 27 with 5950 remainder",
			totalXP:       130950,
			expectedLevel: 27,
			expectedXP:    5950,
		},
		{
			// Max level boundary: sum of all 49 entries
			// Levels 1-9: 500+1000+1500+2000+2500+3000+3500+4000+4500 = 22500
			// Levels 10-19: 5000*10 = 50000
			// Levels 20-29: 7500*10 = 75000
			// Levels 30-39: 10000*10 = 100000
			// Levels 40-49: 15000*10 = 150000
			// Total: 22500 + 50000 + 75000 + 100000 + 150000 = 397500
			name:          "exactly max XP is level 50 with 0 remainder",
			totalXP:       397500,
			expectedLevel: 50,
			expectedXP:    0,
		},
		{
			name:          "beyond max XP is still level 50 with 0 remainder",
			totalXP:       500000,
			expectedLevel: 50,
			expectedXP:    0,
		},
		{
			name:          "negative XP treated as level 1 with 0 remainder",
			totalXP:       -100,
			expectedLevel: 1,
			expectedXP:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			level, remainder := table.ComputeLevel(tt.totalXP)
			if level != tt.expectedLevel {
				t.Errorf("ComputeLevel(%v) level = %d, want %d", tt.totalXP, level, tt.expectedLevel)
			}
			if remainder != tt.expectedXP {
				t.Errorf("ComputeLevel(%v) remainder = %v, want %v", tt.totalXP, remainder, tt.expectedXP)
			}
		})
	}
}

func TestDefaultXPProgressionTable(t *testing.T) {
	table := DefaultXPProgressionTable()

	if table.MaxLevel != 50 {
		t.Errorf("MaxLevel = %d, want 50", table.MaxLevel)
	}

	if len(table.XPPerLevel) != 49 {
		t.Errorf("XPPerLevel has %d entries, want 49", len(table.XPPerLevel))
	}

	// Verify first few entries
	expected := []float64{500, 1000, 1500, 2000, 2500, 3000, 3500, 4000, 4500}
	for i, want := range expected {
		if table.XPPerLevel[i] != want {
			t.Errorf("XPPerLevel[%d] = %v, want %v", i, table.XPPerLevel[i], want)
		}
	}

	// Levels 10-19 should all be 5000
	for i := 9; i < 19; i++ {
		if table.XPPerLevel[i] != 5000 {
			t.Errorf("XPPerLevel[%d] = %v, want 5000", i, table.XPPerLevel[i])
		}
	}

	// Levels 20-29 should all be 7500
	for i := 19; i < 29; i++ {
		if table.XPPerLevel[i] != 7500 {
			t.Errorf("XPPerLevel[%d] = %v, want 7500", i, table.XPPerLevel[i])
		}
	}

	// Levels 30-39 should all be 10000
	for i := 29; i < 39; i++ {
		if table.XPPerLevel[i] != 10000 {
			t.Errorf("XPPerLevel[%d] = %v, want 10000", i, table.XPPerLevel[i])
		}
	}

	// Levels 40-49 should all be 15000
	for i := 39; i < 49; i++ {
		if table.XPPerLevel[i] != 15000 {
			t.Errorf("XPPerLevel[%d] = %v, want 15000", i, table.XPPerLevel[i])
		}
	}
}

func TestConsumeXPIntoLevel(t *testing.T) {
	t.Run("arena stats XP consumed into level and remainder", func(t *testing.T) {
		stats := evr.PlayerStatistics{
			evr.StatisticsGroup{Mode: evr.ModeArenaPublic, ResetSchedule: evr.ResetScheduleAllTime}: &evr.ArenaStatistics{
				Level: &evr.StatisticValue{Count: 1, Value: 1},
				XP:    &evr.StatisticValue{Count: 50, Value: 130950},
			},
		}

		consumeXPIntoLevel(stats)

		arena := stats[evr.StatisticsGroup{Mode: evr.ModeArenaPublic, ResetSchedule: evr.ResetScheduleAllTime}].(*evr.ArenaStatistics)
		if arena.Level.Value != 27 {
			t.Errorf("arena Level = %v, want 27", arena.Level.Value)
		}
		if arena.XP.Value != 5950 {
			t.Errorf("arena XP remainder = %v, want 5950", arena.XP.Value)
		}
		// XP count should be preserved
		if arena.XP.Count != 50 {
			t.Errorf("arena XP count = %v, want 50", arena.XP.Count)
		}
	})

	t.Run("combat stats XP consumed into level and remainder", func(t *testing.T) {
		stats := evr.PlayerStatistics{
			evr.StatisticsGroup{Mode: evr.ModeCombatPublic, ResetSchedule: evr.ResetScheduleAllTime}: &evr.CombatStatistics{
				Level: &evr.StatisticValue{Count: 1, Value: 1},
				XP:    &evr.StatisticValue{Count: 10, Value: 500},
			},
		}

		consumeXPIntoLevel(stats)

		combat := stats[evr.StatisticsGroup{Mode: evr.ModeCombatPublic, ResetSchedule: evr.ResetScheduleAllTime}].(*evr.CombatStatistics)
		if combat.Level.Value != 2 {
			t.Errorf("combat Level = %v, want 2", combat.Level.Value)
		}
		if combat.XP.Value != 0 {
			t.Errorf("combat XP remainder = %v, want 0", combat.XP.Value)
		}
	})

	t.Run("nil XP is not modified", func(t *testing.T) {
		stats := evr.PlayerStatistics{
			evr.StatisticsGroup{Mode: evr.ModeArenaPublic, ResetSchedule: evr.ResetScheduleAllTime}: &evr.ArenaStatistics{
				Level: &evr.StatisticValue{Count: 1, Value: 1},
			},
		}

		// Should not panic
		consumeXPIntoLevel(stats)

		arena := stats[evr.StatisticsGroup{Mode: evr.ModeArenaPublic, ResetSchedule: evr.ResetScheduleAllTime}].(*evr.ArenaStatistics)
		if arena.Level.Value != 1 {
			t.Errorf("arena Level = %v, want 1 (unchanged)", arena.Level.Value)
		}
	})

	t.Run("empty stats does not panic", func(t *testing.T) {
		stats := evr.PlayerStatistics{}
		consumeXPIntoLevel(stats)
	})

	t.Run("max level player gets level 50 with 0 remainder", func(t *testing.T) {
		stats := evr.PlayerStatistics{
			evr.StatisticsGroup{Mode: evr.ModeArenaPublic, ResetSchedule: evr.ResetScheduleAllTime}: &evr.ArenaStatistics{
				Level: &evr.StatisticValue{Count: 1, Value: 1},
				XP:    &evr.StatisticValue{Count: 100, Value: 500000},
			},
		}

		consumeXPIntoLevel(stats)

		arena := stats[evr.StatisticsGroup{Mode: evr.ModeArenaPublic, ResetSchedule: evr.ResetScheduleAllTime}].(*evr.ArenaStatistics)
		if arena.Level.Value != 50 {
			t.Errorf("arena Level = %v, want 50", arena.Level.Value)
		}
		if arena.XP.Value != 0 {
			t.Errorf("arena XP remainder = %v, want 0", arena.XP.Value)
		}
	})
}
