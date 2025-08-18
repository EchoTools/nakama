package evr

import (
	"testing"
)

// Test CombatStatistics validation in CalculateFields to prevent extreme values
func TestCombatStatisticsCalculateFieldsValidation(t *testing.T) {
	testCases := []struct {
		name             string
		combatWins       float64
		combatLosses     float64
		combatAssists    float64
		payloadWins      float64
		payloadGames     float64
		captureWins      float64
		captureGames     float64
		skillMu          float64
		skillSigma       float64
		expectedValid    bool
		expectedWinPct   float64
	}{
		{
			name:          "Normal values",
			combatWins:    50,
			combatLosses:  30,
			combatAssists: 200,
			payloadWins:   15,
			payloadGames:  25,
			captureWins:   10,
			captureGames:  20,
			skillMu:       25.0,
			skillSigma:    8.333,
			expectedValid: true,
			expectedWinPct: 62.5, // 50/80 * 100
		},
		{
			name:          "Problematic large negative wins",
			combatWins:    -1000000000000000, // The problematic value
			combatLosses:  50,
			combatAssists: 100,
			payloadWins:   -50000000000000000, // Another problematic value
			payloadGames:  25,
			captureWins:   10,
			captureGames:  20,
			skillMu:       25.0,
			skillSigma:    8.333,
			expectedValid: true,
			expectedWinPct: 0, // Should be reset to 0
		},
		{
			name:          "Extremely large values",
			combatWins:    1000001, // Over 1e6 limit
			combatLosses:  1000001,
			combatAssists: 1000001,
			payloadWins:   1000001,
			payloadGames:  1000001,
			captureWins:   1000001,
			captureGames:  1000001,
			skillMu:       150.0, // Over 100 limit
			skillSigma:    150.0,
			expectedValid: true,
			expectedWinPct: 0, // Should be reset to 0
		},
		{
			name:          "Wins greater than games played",
			combatWins:    100,
			combatLosses:  20,
			combatAssists: 50,
			payloadWins:   30, // Greater than payloadGames
			payloadGames:  25,
			captureWins:   25, // Greater than captureGames
			captureGames:  20,
			skillMu:       25.0,
			skillSigma:    8.333,
			expectedValid: true,
			expectedWinPct: 83.33, // 100/120 * 100 (rounded)
		},
		{
			name:          "Negative skill ratings",
			combatWins:    50,
			combatLosses:  30,
			combatAssists: 100,
			payloadWins:   15,
			payloadGames:  25,
			captureWins:   10,
			captureGames:  20,
			skillMu:       -10.0, // Negative
			skillSigma:    -5.0,  // Negative
			expectedValid: true,
			expectedWinPct: 62.5,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create test statistics
			stats := &CombatStatistics{
				CombatWins: &StatisticValue{
					Value: tc.combatWins,
					Count: 1,
				},
				CombatLosses: &StatisticValue{
					Value: tc.combatLosses,
					Count: 1,
				},
				CombatAssists: &StatisticValue{
					Value: tc.combatAssists,
					Count: 1,
				},
				CombatPayloadWins: &StatisticValue{
					Value: tc.payloadWins,
					Count: 1,
				},
				CombatPayloadGamesPlayed: &StatisticValue{
					Value: tc.payloadGames,
					Count: 1,
				},
				CombatPointCaptureWins: &StatisticValue{
					Value: tc.captureWins,
					Count: 1,
				},
				CombatPointCaptureGamesPlayed: &StatisticValue{
					Value: tc.captureGames,
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

			// Initialize percentage fields to check they get set properly
			stats.CombatWinPercentage = &StatisticValue{}
			stats.CombatAverageEliminationDeathRatio = &StatisticValue{}
			stats.CombatPayloadWinPercentage = &StatisticValue{}
			stats.CombatPointCaptureWinPercentage = &StatisticValue{}

			// Call CalculateFields
			stats.CalculateFields()

			// Verify GamesPlayed is calculated
			if stats.GamesPlayed == nil {
				t.Error("GamesPlayed should not be nil after CalculateFields")
				return
			}

			// Check that win percentage is within reasonable bounds
			if stats.CombatWinPercentage != nil {
				winPct := stats.CombatWinPercentage.Value
				if winPct < 0 || winPct > 100 {
					t.Errorf("Win percentage out of bounds: %f (should be 0-100)", winPct)
				}
				
				// For specific expected values (allowing some floating point tolerance)
				if tc.expectedWinPct > 0 && (winPct < tc.expectedWinPct-1 || winPct > tc.expectedWinPct+1) {
					t.Errorf("Expected win percentage around %f, got %f", tc.expectedWinPct, winPct)
				}
			}

			// Check that payload and capture percentages are within bounds
			if stats.CombatPayloadWinPercentage != nil {
				payloadPct := stats.CombatPayloadWinPercentage.Value
				if payloadPct < 0 || payloadPct > 100 {
					t.Errorf("Payload win percentage out of bounds: %f", payloadPct)
				}
			}

			if stats.CombatPointCaptureWinPercentage != nil {
				capturePct := stats.CombatPointCaptureWinPercentage.Value
				if capturePct < 0 || capturePct > 100 {
					t.Errorf("Capture win percentage out of bounds: %f", capturePct)
				}
			}

			// Check skill rating ordinal is reasonable
			if stats.SkillRatingOrdinal != nil {
				ordinal := stats.SkillRatingOrdinal.Value
				if ordinal < 0 || ordinal > 100 {
					t.Errorf("Skill rating ordinal out of reasonable bounds: %f", ordinal)
				}
			}
		})
	}
}

// Test that CombatStatistics handles zero values gracefully
func TestCombatStatisticsZeroValues(t *testing.T) {
	stats := &CombatStatistics{
		CombatWins: &StatisticValue{Value: 0, Count: 1},
		CombatLosses: &StatisticValue{Value: 0, Count: 1},
	}

	// Should not panic and should produce reasonable results
	stats.CalculateFields()

	if stats.GamesPlayed == nil || stats.GamesPlayed.Value != 0 {
		t.Error("Expected GamesPlayed to be 0 for zero wins/losses")
	}
}