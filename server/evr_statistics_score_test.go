package server

import (
	"math"
	"sort"
	"testing"
)

func TestFloat64ToScoreAndBack(t *testing.T) {
	testCases := []struct {
		name  string
		input float64
	}{
		// Basic cases
		{"zero", 0.0},
		{"positive integer", 5.0},
		{"negative integer", -3.0},
		{"positive float", 2.5},
		{"negative float", -1.7},

		// Edge cases near zero
		{"small positive", 0.000001},
		{"small negative", -0.000001},
		{"very small positive", 1e-8},
		{"very small negative", -1e-8},

		// Larger values
		{"large positive", 1000000.0},
		{"large negative", -999999.0},
		{"large positive float", 1234567.89},
		{"large negative float", -987654.321},

		// Precision edge cases
		{"max fractional precision", 0.999999999},
		{"negative max fractional", -0.999999999},
		{"just under 1", 0.9999},
		{"just over -1", -0.9999},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Encode
			score, subscore, err := Float64ToScore(tc.input)
			if err != nil {
				t.Fatalf("Failed to encode %f: %v", tc.input, err)
			}

			// Decode
			decoded, err := ScoreToFloat64(score, subscore)
			if err != nil {
				t.Fatalf("Failed to decode (%d, %d): %v", score, subscore, err)
			}

			// Check round-trip accuracy (allow for small floating point errors)
			diff := math.Abs(decoded - tc.input)
			if diff > 1e-8 {
				t.Errorf("Round-trip failed for %f: got %f, diff %e", tc.input, decoded, diff)
			}
		})
	}
}

func TestFloat64ToScoreErrorCases(t *testing.T) {
	testCases := []struct {
		name  string
		input float64
	}{
		{"positive infinity", math.Inf(1)},
		{"negative infinity", math.Inf(-1)},
		{"NaN", math.NaN()},
		{"very large positive", 1e20},
		{"very large negative", -1e20},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Float64ToScore(tc.input)
			if err == nil {
				t.Errorf("Expected error for input %f, but got none", tc.input)
			}
		})
	}
}

func TestLeaderboardSortingCorrectness(t *testing.T) {
	// Test values that should sort in ascending order
	testValues := []float64{
		-1000.5,
		-100.25,
		-10.75,
		-2.5,
		-1.3,
		-1.1,
		-1.0,
		-0.9,
		-0.1,
		-0.001,
		0.0,
		0.001,
		0.1,
		0.9,
		1.3,
		2.5,
		10.75,
		100.25,
		1000.5,
	}

	// Encode all values
	type scorePair struct {
		score    int64
		subscore int64
		original float64
	}

	encoded := make([]scorePair, len(testValues))
	for i, v := range testValues {
		score, subscore, err := Float64ToScore(v)
		if err != nil {
			t.Fatalf("Failed to encode %f: %v", v, err)
		}
		encoded[i] = scorePair{score, subscore, v}
	}

	// Sort by encoded values (ascending, like leaderboard does)
	sort.Slice(encoded, func(i, j int) bool {
		if encoded[i].score != encoded[j].score {
			return encoded[i].score < encoded[j].score
		}
		return encoded[i].subscore < encoded[j].subscore
	})

	// Verify the sorted order matches the original float64 order
	for i, pair := range encoded {
		expectedValue := testValues[i]
		if math.Abs(pair.original-expectedValue) > 1e-10 {
			t.Errorf("Sort order incorrect at position %d: expected %f, got %f",
				i, expectedValue, pair.original)
		}
	}
}

func TestLeaderboardSortingDescending(t *testing.T) {
	// Test values for descending sort order
	testValues := []float64{
		-5.5, -2.3, -0.1, 0.0, 0.1, 2.3, 5.5, 10.0,
	}

	// Encode all values
	type scorePair struct {
		score    int64
		subscore int64
		original float64
	}

	encoded := make([]scorePair, len(testValues))
	for i, v := range testValues {
		score, subscore, err := Float64ToScore(v)
		if err != nil {
			t.Fatalf("Failed to encode %f: %v", v, err)
		}
		encoded[i] = scorePair{score, subscore, v}
	}

	// Sort by encoded values (descending, like leaderboard does)
	sort.Slice(encoded, func(i, j int) bool {
		if encoded[i].score != encoded[j].score {
			return encoded[i].score > encoded[j].score
		}
		return encoded[i].subscore > encoded[j].subscore
	})

	// Create expected descending order
	expectedOrder := make([]float64, len(testValues))
	copy(expectedOrder, testValues)
	sort.Slice(expectedOrder, func(i, j int) bool {
		return expectedOrder[i] > expectedOrder[j]
	})

	// Verify the sorted order matches the expected descending float64 order
	for i, pair := range encoded {
		expectedValue := expectedOrder[i]
		if math.Abs(pair.original-expectedValue) > 1e-10 {
			t.Errorf("Descending sort order incorrect at position %d: expected %f, got %f",
				i, expectedValue, pair.original)
		}
	}
}

func TestScoreToFloat64ErrorCases(t *testing.T) {
	// Test invalid subscore values (should be 0 <= subscore < 1e9)
	// and invalid score values (should be non-negative)
	testCases := []struct {
		name     string
		score    int64
		subscore int64
	}{
		{"negative score", -1, 0},
		{"negative subscore", 1, -1},
		{"subscore too large", 1, 1000000001},
		{"way too large subscore", 0, 2000000000},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ScoreToFloat64(tc.score, tc.subscore)
			if err == nil {
				t.Errorf("Expected error for score=%d, subscore=%d, but got none", tc.score, tc.subscore)
			}
		})
	}
}

func TestEncodingConsistency(t *testing.T) {
	// Test that similar values encode to similar scores
	testCases := []struct {
		v1, v2        float64
		name          string
		shouldBeClose bool
	}{
		{1.0, 1.1, "close positive values", true},
		{-1.0, -1.1, "close negative values", true},
		{0.0, 0.1, "close to zero", true},
		{1.0, -1.0, "opposite signs", false},
		{100.0, 100.1, "large close values", true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			score1, _, err1 := Float64ToScore(tc.v1)
			score2, _, err2 := Float64ToScore(tc.v2)

			if err1 != nil || err2 != nil {
				t.Fatalf("Encoding failed: %v, %v", err1, err2)
			}

			scoreDiff := score1 - score2
			if tc.shouldBeClose {
				// For close values, scores should be close (within 1)
				if scoreDiff < -1 || scoreDiff > 1 {
					t.Errorf("Scores not close for %f vs %f: %d vs %d",
						tc.v1, tc.v2, score1, score2)
				}
			}
		})
	}
}

func TestNegativeIntegerSorting(t *testing.T) {
	// This test specifically targets the bug where -1.0 was sorting as "smaller" than -1.1
	// because of incorrect subscore handling for exact negative integers.

	v1 := -1.1
	v2 := -1.0

	s1, ss1, err := Float64ToScore(v1)
	if err != nil {
		t.Fatalf("Failed to encode %f: %v", v1, err)
	}

	s2, ss2, err := Float64ToScore(v2)
	if err != nil {
		t.Fatalf("Failed to encode %f: %v", v2, err)
	}

	// In ascending sort, -1.1 should come before -1.0
	// So (s1, ss1) should be "less than" (s2, ss2)

	isLess := false
	if s1 < s2 {
		isLess = true
	} else if s1 == s2 && ss1 < ss2 {
		isLess = true
	}

	if !isLess {
		t.Errorf("Sorting error: %f should be less than %f, but encoded values are (%d, %d) and (%d, %d)",
			v1, v2, s1, ss1, s2, ss2)
	}
}

func TestScoreToFloat64Corrupted(t *testing.T) {
	// Test cases for corrupted scores (multiple increments of offset)
	testCases := []struct {
		name     string
		score    int64
		subscore int64
		expected float64
	}{
		{"corrupted 12", 12000000000000012, 0, 12.0},
		{"corrupted 1", 2000000000000001, 0, 1.0},
		{"corrupted large", 5000000000000123, 0, 123.0},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			decoded, err := ScoreToFloat64(tc.score, tc.subscore)
			if err != nil {
				t.Fatalf("Failed to decode (%d, %d): %v", tc.score, tc.subscore, err)
			}

			if math.Abs(decoded-tc.expected) > 1e-8 {
				t.Errorf("Correction failed for %d: got %f, expected %f", tc.score, decoded, tc.expected)
			}
		})
	}
}
