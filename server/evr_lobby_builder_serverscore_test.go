package server

import (
	"testing"
)

func TestCalculateServerScore(t *testing.T) {
	tests := []struct {
		name        string
		bluePings   []int
		orangePings []int
		expected    float64
		wantErr     string // non-empty means the call must fail with this message
	}{
		{
			name:        "Nil blue pings",
			bluePings:   nil,
			orangePings: []int{20, 30, 40, 50},
			wantErr:     "nil pings",
		},
		{
			name:        "Nil orange pings",
			bluePings:   []int{20, 30, 40, 50},
			orangePings: nil,
			wantErr:     "nil pings",
		},
		{
			name:        "Less than 4 players per team",
			bluePings:   []int{20, 30, 40},
			orangePings: []int{20, 30, 40},
			wantErr:     "number of players per team is less than 4",
		},
		{
			name:        "More than 5 players per team",
			bluePings:   []int{20, 30, 40, 50, 60, 70},
			orangePings: []int{20, 30, 40, 50, 60, 70},
			wantErr:     "number of players per team is greater than 5",
		},
		{
			name:        "Different number of players in teams",
			bluePings:   []int{20, 30, 40, 50},
			orangePings: []int{20, 30, 40, 50, 60},
			wantErr:     "number of players in blue team does not match number of players in orange team",
		},
		{
			name:        "Ping too high",
			bluePings:   []int{20, 30, 40, 50},
			orangePings: []int{20, 30, 40, 160},
			wantErr:     "ping exceeds maximum allowed value",
		},
		{
			name:        "Valid input",
			bluePings:   []int{20, 30, 40, 50},
			orangePings: []int{20, 30, 40, 50},
			expected:    96.45675735802385,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := calculateServerScore(tt.bluePings, tt.orangePings)

			// A rejected input must be reported as an error, not as a zero
			// score: on the error paths result is 0 and so is an unset
			// tt.expected, so comparing only the score cannot tell a correct
			// rejection from no rejection at all.
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("calculateServerScore() error = nil, want %q", tt.wantErr)
				}
				if err.Error() != tt.wantErr {
					t.Fatalf("calculateServerScore() error = %q, want %q", err, tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("calculateServerScore() unexpected error: %v", err)
			}
			if result != tt.expected {
				t.Errorf("calculateServerScore() = %v, want %v", result, tt.expected)
			}
		})
	}
}
