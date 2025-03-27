package server

import (
	"fmt"
	"testing"
)

func TestFloat64ToScore(t *testing.T) {
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
			input:         float64(1 << 32),
			expectedScore: 0,
			expectedError: fmt.Errorf("value too large: %f", float64(1<<32)),
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
			expectedScore: 0,
			expectedError: nil,
		},
		{
			name:          "max positive value",
			input:         float64(1<<32) - 1,
			expectedScore: 4294967294000000000,
			expectedError: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			score, err := Float64ToScore(tc.input)

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
