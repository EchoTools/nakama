package evr

import (
	"testing"
)

// Test the statistics validation in CalculateFields to prevent extreme values
func TestArenaStatisticsCalculateFieldsValidation(t *testing.T) {
	testCases := []struct {
		name              string
		earlyQuitsValue   float64
		gamesPlayed       float64
		expectedArenaTies float64
		expectedEarlyQuitPercentage float64
	}{
		{
			name:              "Normal values",
			earlyQuitsValue:   5,
			gamesPlayed:       100,
			expectedArenaTies: 5,
			expectedEarlyQuitPercentage: 5.0, // 5/100 * 100
		},
		{
			name:              "Problematic large negative value",
			earlyQuitsValue:   -1000000000000000, // The problematic value from the issue
			gamesPlayed:       100,
			expectedArenaTies: 0, // Should be reset to 0
			expectedEarlyQuitPercentage: 0, // Should be reset to 0
		},
		{
			name:              "Negative value",
			earlyQuitsValue:   -5,
			gamesPlayed:       100,
			expectedArenaTies: 0, // Should be reset to 0
			expectedEarlyQuitPercentage: 0, // Should be reset to 0
		},
		{
			name:              "Value greater than games played",
			earlyQuitsValue:   150,
			gamesPlayed:       100,
			expectedArenaTies: 0, // Should be reset to 0
			expectedEarlyQuitPercentage: 0, // Should be reset to 0
		},
		{
			name:              "Extremely large value",
			earlyQuitsValue:   1000001,
			gamesPlayed:       100,
			expectedArenaTies: 0, // Should be reset to 0
			expectedEarlyQuitPercentage: 0, // Should be reset to 0
		},
		{
			name:              "Zero value",
			earlyQuitsValue:   0,
			gamesPlayed:       100,
			expectedArenaTies: 0,
			expectedEarlyQuitPercentage: 0,
		},
		{
			name:              "Edge case: exactly games played",
			earlyQuitsValue:   100,
			gamesPlayed:       100,
			expectedArenaTies: 100,
			expectedEarlyQuitPercentage: 100,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create test statistics
			stats := &ArenaStatistics{
				ArenaWins: &StatisticValue{
					Value: tc.gamesPlayed * 0.5, // Half wins
					Count: 1,
				},
				ArenaLosses: &StatisticValue{
					Value: tc.gamesPlayed * 0.5, // Half losses
					Count: 1,
				},
				EarlyQuits: &StatisticValue{
					Value: tc.earlyQuitsValue,
					Count: 1,
				},
			}

			// Call CalculateFields
			stats.CalculateFields()

			// Check ArenaTies
			if stats.ArenaTies == nil {
				t.Error("ArenaTies should not be nil after CalculateFields")
				return
			}
			if stats.ArenaTies.GetValue() != tc.expectedArenaTies {
				t.Errorf("Expected ArenaTies: %f, got: %f", tc.expectedArenaTies, stats.ArenaTies.GetValue())
			}

			// Check EarlyQuitPercentage
			if stats.EarlyQuitPercentage == nil {
				t.Error("EarlyQuitPercentage should not be nil after CalculateFields")
				return
			}
			if stats.EarlyQuitPercentage.GetValue() != tc.expectedEarlyQuitPercentage {
				t.Errorf("Expected EarlyQuitPercentage: %f, got: %f", 
					tc.expectedEarlyQuitPercentage, stats.EarlyQuitPercentage.GetValue())
			}
		})
	}
}