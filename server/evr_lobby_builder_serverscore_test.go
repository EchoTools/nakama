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
		wantError   bool
	}{
		{
			name:        "Nil blue pings",
			bluePings:   nil,
			orangePings: []int{20, 30, 40, 50},
			wantError:   true,
		},
		{
			name:        "Nil orange pings",
			bluePings:   []int{20, 30, 40, 50},
			orangePings: nil,
			wantError:   true,
		},
		{
			name:        "Less than 4 players per team",
			bluePings:   []int{20, 30, 40},
			orangePings: []int{20, 30, 40},
			wantError:   true,
		},
		{
			name:        "More than 5 players per team",
			bluePings:   []int{20, 30, 40, 50, 60, 70},
			orangePings: []int{20, 30, 40, 50, 60, 70},
			wantError:   true,
		},
		{
			name:        "Different number of players in teams",
			bluePings:   []int{20, 30, 40, 50},
			orangePings: []int{20, 30, 40, 50, 60},
			wantError:   true,
		},
		{
			name:        "Ping too high",
			bluePings:   []int{20, 30, 40, 50},
			orangePings: []int{20, 30, 40, 160},
			wantError:   true,
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
			result, err := calculateServerScore(tt.bluePings, tt.orangePings)
			if err != nil {
				if err.Error() != "nil pings" {
				}
			}
			if result != tt.expected {
				t.Errorf("calculateServerScore() = %v, want %v", result, tt.expected)
			}
		})
	}
}
