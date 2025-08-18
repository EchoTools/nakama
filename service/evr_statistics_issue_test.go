package server

import (
	"testing"
	"math"
)

// Test the specific fix for the profile statistics issue
func TestFloat64ToScoreProfileStatisticsIssue(t *testing.T) {
	// Test the specific problematic values from the issue
	problematicValues := []struct {
		value         float64
		shouldReject  bool
		description   string
	}{
		{
			value:        -1000000000000000, // Exactly -1e15
			shouldReject: true,
			description:  "ArenaTies problematic value",
		},
		{
			value:        -50000000000000000, // -5e16
			shouldReject: true,
			description:  "EarlyQuitPercentage problematic value",
		},
		{
			value:        -999999999999999, // Maximum negative value we can encode
			shouldReject: false,
			description:  "Maximum valid negative value",
		},
		{
			value:        0,
			shouldReject: false,
			description:  "Zero value",
		},
		{
			value:        1,
			shouldReject: false,
			description:  "Small positive value",
		},
	}

	for _, tc := range problematicValues {
		t.Run(tc.description, func(t *testing.T) {
			score, subscore, err := Float64ToScore(tc.value)

			if tc.shouldReject {
				if err == nil {
					t.Errorf("Expected encoding of %f to be rejected, but got score=%d, subscore=%d", 
						tc.value, score, subscore)
				}
				return
			}

			// Should not be rejected
			if err != nil {
				t.Errorf("Expected encoding of %f to succeed, but got error: %v", tc.value, err)
				return
			}

			// Verify score is non-negative
			if score < 0 {
				t.Errorf("Encoding of %f produced negative score: %d", tc.value, score)
				return
			}

			// Test round-trip
			decoded, err := ScoreToFloat64(score, subscore)
			if err != nil {
				t.Errorf("Failed to decode (%d, %d): %v", score, subscore, err)
				return
			}

			if math.Abs(decoded-tc.value) > 1e-8 {
				t.Errorf("Round-trip failed for %f: got %f, diff %e", tc.value, decoded, 
					math.Abs(decoded-tc.value))
			}
		})
	}
}

// Test that no valid input can produce a negative score
func TestFloat64ToScoreNeverNegative(t *testing.T) {
	// Test a wide range of values
	testValues := []float64{
		-999999999999999, -100000, -1000, -100, -10, -1, -0.1, 
		0, 0.1, 1, 10, 100, 1000, 100000, 999999999999999,
	}

	for _, val := range testValues {
		t.Run("value_"+string(rune(int(val))), func(t *testing.T) {
			score, subscore, err := Float64ToScore(val)
			if err != nil {
				// Value out of range is OK
				return
			}

			if score < 0 {
				t.Errorf("Float64ToScore(%f) produced negative score: %d", val, score)
			}

			if subscore < 0 {
				t.Errorf("Float64ToScore(%f) produced negative subscore: %d", val, subscore)
			}
		})
	}
}