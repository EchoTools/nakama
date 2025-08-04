package server

import (
	"fmt"
	"math"
	"testing"
)

func TestFloat64ToScoreLegacy(t *testing.T) {
	testCases := []struct {
		name          string
		input         float64
		expectedScore int64
		expectedError error
	}{
		{
			name:          "positive value",
			input:         1.5,
			expectedScore: 1500000000,
			expectedError: nil,
		},
		{
			name:          "zero value",
			input:         0,
			expectedScore: 0,
			expectedError: nil,
		},
		{
			name:          "large value",
			input:         float64(1<<32) + 1,
			expectedScore: 0,
			expectedError: fmt.Errorf("value too large: %f", float64(1<<32)+1),
		},
		{
			name:          "negative value",
			input:         -1.5,
			expectedScore: 0,
			expectedError: fmt.Errorf("negative value: %f", -1.5),
		},
		{
			name:          "small positive value",
			input:         0.000000001,
			expectedScore: 1,
			expectedError: nil,
		},
		{
			name:          "max positive value",
			input:         float64(1<<32) - 1,
			expectedScore: 4294967295000000000,
			expectedError: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			score, err := Float64ToScoreLegacy(tc.input)

			if tc.expectedError != nil {
				if err == nil || err.Error() != tc.expectedError.Error() {
					t.Fatalf("Expected error: %v, got: %v", tc.expectedError, err)
				}
			} else {
				if err != nil {
					t.Fatalf("Unexpected error: %v", err)
				}
			}

			if score != tc.expectedScore {
				t.Errorf("Expected score: %d, got: %d", tc.expectedScore, score)
			}
		})
	}
}

// Test the new dual-value Float64ToScore function
func TestFloat64ToScore(t *testing.T) {
	testCases := []struct {
		name           string
		input          float64
		expectedScore  int64
		expectedSub    int64
		expectedError  error
	}{
		{
			name:          "zero value",
			input:         0.0,
			expectedScore: 1000000000000000, // 1e15
			expectedSub:   0,
			expectedError: nil,
		},
		{
			name:          "positive integer",
			input:         1.0,
			expectedScore: 1000000000000001, // 1e15 + 1
			expectedSub:   0,
			expectedError: nil,
		},
		{
			name:          "negative integer",
			input:         -1.0,
			expectedScore: 999999999999998, // 1e15 - 1 - 1
			expectedSub:   0,
			expectedError: nil,
		},
		{
			name:          "positive float",
			input:         2.5,
			expectedScore: 1000000000000002, // 1e15 + 2
			expectedSub:   500000000,        // 0.5 * 1e9
			expectedError: nil,
		},
		{
			name:          "negative float",
			input:         -2.5,
			expectedScore: 999999999999997, // 1e15 - 1 - 2
			expectedSub:   499999999,       // (1.0 - 0.5) * (1e9 - 1)
			expectedError: nil,
		},
		{
			name:          "small positive",
			input:         0.1,
			expectedScore: 1000000000000000, // 1e15 
			expectedSub:   100000000,        // 0.1 * 1e9
			expectedError: nil,
		},
		{
			name:          "small negative",
			input:         -0.1,
			expectedScore: 999999999999999, // 1e15 - 1 - 0 (since intPart = 0 for -0.1)
			expectedSub:   899999999,       // (1.0 - 0.1) * (1e9 - 1)
			expectedError: nil,
		},
		{
			name:          "infinity",
			input:         math.Inf(1),
			expectedScore: 0,
			expectedSub:   0,
			expectedError: fmt.Errorf("invalid value: +Inf"),
		},
		{
			name:          "negative infinity",
			input:         math.Inf(-1),
			expectedScore: 0,
			expectedSub:   0,
			expectedError: fmt.Errorf("invalid value: -Inf"),
		},
		{
			name:          "NaN",
			input:         math.NaN(),
			expectedScore: 0,
			expectedSub:   0,
			expectedError: fmt.Errorf("invalid value: NaN"),
		},
		{
			name:          "too large positive",
			input:         1e16,
			expectedScore: 0,
			expectedSub:   0,
			expectedError: fmt.Errorf("value out of range: %f", 1e16),
		},
		{
			name:          "too large negative",
			input:         -1e16,
			expectedScore: 0,
			expectedSub:   0,
			expectedError: fmt.Errorf("value out of range: %f", -1e16),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			score, subscore, err := Float64ToScore(tc.input)

			if tc.expectedError != nil {
				if err == nil || err.Error() != tc.expectedError.Error() {
					t.Fatalf("Expected error: %v, got: %v", tc.expectedError, err)
				}
			} else {
				if err != nil {
					t.Fatalf("Unexpected error: %v", err)
				}
			}

			if score != tc.expectedScore {
				t.Errorf("Expected score: %d, got: %d", tc.expectedScore, score)
			}
			if subscore != tc.expectedSub {
				t.Errorf("Expected subscore: %d, got: %d", tc.expectedSub, subscore)
			}
		})
	}
}

// Test the new ScoreToFloat64 function
func TestScoreToFloat64(t *testing.T) {
	testCases := []struct {
		name          string
		score         int64
		subscore      int64
		expected      float64
		expectedError error
	}{
		{
			name:          "zero value",
			score:         1000000000000000, // 1e15
			subscore:      0,
			expected:      0.0,
			expectedError: nil,
		},
		{
			name:          "positive integer",
			score:         1000000000000001, // 1e15 + 1
			subscore:      0,
			expected:      1.0,
			expectedError: nil,
		},
		{
			name:          "negative integer",
			score:         999999999999998, // 1e15 - 1 - 1
			subscore:      0,
			expected:      -1.0,
			expectedError: nil,
		},
		{
			name:          "positive float",
			score:         1000000000000002, // 1e15 + 2
			subscore:      500000000,        // 0.5 * 1e9
			expected:      2.5,
			expectedError: nil,
		},
		{
			name:          "negative float",
			score:         999999999999997, // 1e15 - 1 - 2
			subscore:      499999999,       // (1.0 - 0.5) * (1e9 - 1)
			expected:      -2.5,
			expectedError: nil,
		},
		{
			name:          "negative score",
			score:         -1,
			subscore:      0,
			expected:      0.0,
			expectedError: fmt.Errorf("invalid score: -1 (must be non-negative)"),
		},
		{
			name:          "negative subscore",
			score:         1000000000000000,
			subscore:      -1,
			expected:      0.0,
			expectedError: fmt.Errorf("invalid subscore: -1"),
		},
		{
			name:          "subscore too large",
			score:         1000000000000000,
			subscore:      1000000000, // 1e9
			expected:      0.0,
			expectedError: fmt.Errorf("invalid subscore: 1000000000"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := ScoreToFloat64(tc.score, tc.subscore)

			if tc.expectedError != nil {
				if err == nil || err.Error() != tc.expectedError.Error() {
					t.Fatalf("Expected error: %v, got: %v", tc.expectedError, err)
				}
			} else {
				if err != nil {
					t.Fatalf("Unexpected error: %v", err)
				}
			}

			// Use a small tolerance for floating point comparison
			if math.Abs(result-tc.expected) > 1e-8 {
				t.Errorf("Expected result: %f, got: %f", tc.expected, result)
			}
		})
	}
}

// Test round-trip conversion to ensure accuracy
func TestFloat64ToScoreRoundTrip(t *testing.T) {
	testValues := []float64{
		-1000.5, -100.25, -10.75, -2.5, -1.3, -0.9, -0.1, -0.001,
		0.0,
		0.001, 0.1, 0.9, 1.3, 2.5, 10.75, 100.25, 1000.5,
	}

	for _, original := range testValues {
		t.Run(fmt.Sprintf("value_%f", original), func(t *testing.T) {
			// Encode
			score, subscore, err := Float64ToScore(original)
			if err != nil {
				t.Fatalf("Failed to encode %f: %v", original, err)
			}

			// Decode
			decoded, err := ScoreToFloat64(score, subscore)
			if err != nil {
				t.Fatalf("Failed to decode (%d, %d): %v", score, subscore, err)
			}

			// Check accuracy (allow for small floating point errors)
			diff := math.Abs(decoded - original)
			if diff > 1e-8 {
				t.Errorf("Round-trip failed for %f: got %f, diff %e", original, decoded, diff)
			}
		})
	}
}

// Test the specific issue: zero value encoding should not be treated as skip condition
func TestZeroValueNotSkipCondition(t *testing.T) {
	// Test that 0.0 encodes to (1e15, 0), not (0, 0)
	score, subscore, err := Float64ToScore(0.0)
	if err != nil {
		t.Fatalf("Failed to encode 0.0: %v", err)
	}

	// Verify that the encoded value for 0.0 is NOT (0, 0)
	if score == 0 && subscore == 0 {
		t.Errorf("Zero value incorrectly encoded as (0, 0), should be (1e15, 0)")
	}

	// Verify the correct encoding
	expectedScore := int64(1e15)
	expectedSubscore := int64(0)
	if score != expectedScore || subscore != expectedSubscore {
		t.Errorf("Zero value incorrectly encoded as (%d, %d), should be (%d, %d)", 
			score, subscore, expectedScore, expectedSubscore)
	}

	// Verify it decodes back to 0.0
	decoded, err := ScoreToFloat64(score, subscore)
	if err != nil {
		t.Fatalf("Failed to decode zero value: %v", err)
	}
	if decoded != 0.0 {
		t.Errorf("Zero value decoded incorrectly: got %f, expected 0.0", decoded)
	}
}
