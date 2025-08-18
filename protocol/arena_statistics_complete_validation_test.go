package evr

import (
	"testing"
)

// Test additional ArenaStatistics validation beyond the original EarlyQuits issue
func TestArenaStatisticsCompleteValidation(t *testing.T) {
	testCases := []struct {
		name                     string
		arenaWins               float64
		arenaLosses             float64
		assists                 float64
		points                  float64
		possessionTime          float64
		topSpeedsTotal          float64
		goals                   float64
		saves                   float64
		shotsOnGoal             float64
		shotsOnGoalAgainst      float64
		stuns                   float64
		blocks                  float64
		punchesReceived         float64
		skillMu                 float64
		skillSigma              float64
		expectedWinPct          float64
		expectGoalScorePct      bool
		expectGoalSavePct       bool
		expectStunPct           bool
		expectBlockPct          bool
	}{
		{
			name:               "Normal values",
			arenaWins:         60,
			arenaLosses:       40,
			assists:           200,
			points:            5000,
			possessionTime:    3600,
			topSpeedsTotal:    2500,
			goals:            80,
			saves:            50,
			shotsOnGoal:      100,
			shotsOnGoalAgainst: 75,
			stuns:            30,
			blocks:           20,
			punchesReceived:  60,
			skillMu:          28.5,
			skillSigma:       7.2,
			expectedWinPct:   60.0, // 60/100 * 100
			expectGoalScorePct: true,
			expectGoalSavePct:  true,
			expectStunPct:      true,
			expectBlockPct:     true,
		},
		{
			name:               "Problematic large negative values",
			arenaWins:         -1000000000000000, // Problematic value
			arenaLosses:       -500000000000000,  // Problematic value
			assists:           -1000000000000000,
			points:            -1000000000000000,
			possessionTime:    -1000000000000000,
			topSpeedsTotal:    -1000000000000000,
			goals:            -1000000000000000,
			saves:            -1000000000000000,
			shotsOnGoal:      -1000000000000000,
			shotsOnGoalAgainst: -1000000000000000,
			stuns:            -1000000000000000,
			blocks:           -1000000000000000,
			punchesReceived:  -1000000000000000,
			skillMu:          -1000000000000000,
			skillSigma:       -1000000000000000,
			expectedWinPct:   0, // Should be reset
			expectGoalScorePct: false,
			expectGoalSavePct:  false,
			expectStunPct:      false,
			expectBlockPct:     false,
		},
		{
			name:               "Extremely large values",
			arenaWins:         1000001, // Over 1e6 limit
			arenaLosses:       1000001,
			assists:           1000001,
			points:            1000000001, // Over 1e9 limit
			possessionTime:    100000001,  // Over 1e8 limit
			topSpeedsTotal:    100000001,
			goals:            1000001,
			saves:            1000001,
			shotsOnGoal:      1000001,
			shotsOnGoalAgainst: 1000001,
			stuns:            1000001,
			blocks:           1000001,
			punchesReceived:  1000001,
			skillMu:          150.0, // Over 100 limit
			skillSigma:       150.0,
			expectedWinPct:   0, // Should be reset
			expectGoalScorePct: false,
			expectGoalSavePct:  false,
			expectStunPct:      false,
			expectBlockPct:     false,
		},
		{
			name:               "Impossible ratios (more goals than shots)",
			arenaWins:         50,
			arenaLosses:       50,
			assists:           100,
			points:            2000,
			possessionTime:    1800,
			topSpeedsTotal:    2000,
			goals:            80, // More than shotsOnGoal
			saves:            50, // More than shotsOnGoalAgainst  
			shotsOnGoal:      60, // Less than goals
			shotsOnGoalAgainst: 40, // Less than saves
			stuns:            30, // More than punchesReceived
			blocks:           20,
			punchesReceived:  25, // Less than stuns
			skillMu:          25.0,
			skillSigma:       8.0,
			expectedWinPct:   50.0,
			expectGoalScorePct: false, // Should not be set due to invalid ratio
			expectGoalSavePct:  false, // Should not be set due to invalid ratio
			expectStunPct:      false, // Should not be set due to invalid ratio
			expectBlockPct:     true,  // This one should be valid
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create test statistics
			stats := &ArenaStatistics{
				ArenaWins: &StatisticValue{
					Value: tc.arenaWins,
					Count: 1,
				},
				ArenaLosses: &StatisticValue{
					Value: tc.arenaLosses,
					Count: 1,
				},
				Assists: &StatisticValue{
					Value: tc.assists,
					Count: 1,
				},
				Points: &StatisticValue{
					Value: tc.points,
					Count: 1,
				},
				PossessionTime: &StatisticValue{
					Value: tc.possessionTime,
					Count: 1,
				},
				TopSpeedsTotal: &StatisticValue{
					Value: tc.topSpeedsTotal,
					Count: 1,
				},
				Goals: &StatisticValue{
					Value: tc.goals,
					Count: 1,
				},
				Saves: &StatisticValue{
					Value: tc.saves,
					Count: 1,
				},
				ShotsOnGoal: &StatisticValue{
					Value: tc.shotsOnGoal,
					Count: 1,
				},
				ShotsOnGoalAgainst: &StatisticValue{
					Value: tc.shotsOnGoalAgainst,
					Count: 1,
				},
				Stuns: &StatisticValue{
					Value: tc.stuns,
					Count: 1,
				},
				Blocks: &StatisticValue{
					Value: tc.blocks,
					Count: 1,
				},
				PunchesReceived: &StatisticValue{
					Value: tc.punchesReceived,
					Count: 1,
				},
				SkillRatingMu: &StatisticValue{
					Value: tc.skillMu,
					Count: 1,
				},
				SkillRatingSigma: &StatisticValue{
					Value: tc.skillSigma,
					Count: 1,
				},
			}

			// Call CalculateFields
			stats.CalculateFields()

			// Check ArenaWinPercentage
			if stats.ArenaWinPercentage != nil {
				winPct := stats.ArenaWinPercentage.GetValue()
				if winPct < 0 || winPct > 100 {
					t.Errorf("Win percentage out of bounds: %f (should be 0-100)", winPct)
				}
				
				// Check expected value with tolerance
				if tc.expectedWinPct > 0 && (winPct < tc.expectedWinPct-1 || winPct > tc.expectedWinPct+1) {
					t.Errorf("Expected win percentage around %f, got %f", tc.expectedWinPct, winPct)
				}
			}

			// Check GoalScorePercentage
			if tc.expectGoalScorePct {
				if stats.GoalScorePercentage == nil {
					t.Error("Expected GoalScorePercentage to be set but it was nil")
				} else {
					pct := stats.GoalScorePercentage.GetValue()
					if pct < 0 || pct > 100 {
						t.Errorf("Goal score percentage out of bounds: %f", pct)
					}
				}
			} else {
				if stats.GoalScorePercentage != nil && stats.GoalScorePercentage.GetValue() != 0 {
					t.Errorf("Expected GoalScorePercentage to not be set, but got %f", 
						stats.GoalScorePercentage.GetValue())
				}
			}

			// Check GoalSavePercentage
			if tc.expectGoalSavePct {
				if stats.GoalSavePercentage == nil {
					t.Error("Expected GoalSavePercentage to be set but it was nil")
				} else {
					pct := stats.GoalSavePercentage.GetValue()
					if pct < 0 || pct > 100 {
						t.Errorf("Goal save percentage out of bounds: %f", pct)
					}
				}
			} else {
				if stats.GoalSavePercentage != nil && stats.GoalSavePercentage.GetValue() != 0 {
					t.Errorf("Expected GoalSavePercentage to not be set, but got %f", 
						stats.GoalSavePercentage.GetValue())
				}
			}

			// Check StunPercentage
			if tc.expectStunPct {
				if stats.StunPercentage == nil {
					t.Error("Expected StunPercentage to be set but it was nil")
				} else {
					pct := stats.StunPercentage.GetValue()
					if pct < 0 || pct > 100 {
						t.Errorf("Stun percentage out of bounds: %f", pct)
					}
				}
			} else {
				if stats.StunPercentage != nil && stats.StunPercentage.GetValue() != 0 {
					t.Errorf("Expected StunPercentage to not be set, but got %f", 
						stats.StunPercentage.GetValue())
				}
			}

			// Check BlockPercentage
			if tc.expectBlockPct {
				if stats.BlockPercentage == nil {
					t.Error("Expected BlockPercentage to be set but it was nil")
				} else {
					pct := stats.BlockPercentage.GetValue()
					if pct < 0 || pct > 100 {
						t.Errorf("Block percentage out of bounds: %f", pct)
					}
				}
			} else {
				if stats.BlockPercentage != nil && stats.BlockPercentage.GetValue() != 0 {
					t.Errorf("Expected BlockPercentage to not be set, but got %f", 
						stats.BlockPercentage.GetValue())
				}
			}

			// Check all per-game stats are reasonable (not negative, not impossibly large)
			perGameStats := []*StatisticValue{
				stats.AssistsPerGame,
				stats.AveragePointsPerGame,
				stats.AveragePossessionTimePerGame,
				stats.AverageTopSpeedPerGame,
				stats.GoalsPerGame,
				stats.SavesPerGame,
				stats.StunsPerGame,
			}

			for i, stat := range perGameStats {
				if stat != nil {
					val := stat.GetValue()
					if val < 0 {
						t.Errorf("Per-game statistic %d is negative: %f", i, val)
					}
					if val > 1e6 { // Very large per-game values are suspicious
						t.Errorf("Per-game statistic %d is suspiciously large: %f", i, val)
					}
				}
			}

			// Check SkillRatingOrdinal is reasonable
			if stats.SkillRatingOrdinal != nil {
				ordinal := stats.SkillRatingOrdinal.GetValue()
				if ordinal < 0 || ordinal > 100 {
					t.Errorf("Skill rating ordinal out of reasonable bounds: %f", ordinal)
				}
			}
		})
	}
}