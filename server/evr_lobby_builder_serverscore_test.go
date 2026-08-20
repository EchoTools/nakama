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

			result, err := calculateServerScore(tt.bluePings, tt.orangePings, ServerScoreDefaultMinRTT, ServerScoreDefaultMaxRTT)

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

// TestCalculateServerScoreHonoursRTTWindow pins the one property the exported
// wrapper's rttMin/rttMax parameters have to have: they must reach the
// arithmetic. Every score below normalises against the window, so widening it
// while holding the pings fixed cannot leave the score where it was.
func TestCalculateServerScoreHonoursRTTWindow(t *testing.T) {
	t.Parallel()

	// Identical input for every case; only the window moves.
	teams := func() [][]int {
		return [][]int{
			{20, 30, 40, 50},
			{25, 35, 45, 55},
		}
	}

	score := func(t *testing.T, rttMin, rttMax int) float64 {
		t.Helper()
		got, err := CalculateServerScore(teams(), rttMin, rttMax, 0)
		if err != nil {
			t.Fatalf("CalculateServerScore(_, %d, %d, 0) unexpected error: %v", rttMin, rttMax, err)
		}
		return got
	}

	t.Run("a wider window scores differently", func(t *testing.T) {
		t.Parallel()

		atDefaults := score(t, ServerScoreDefaultMinRTT, ServerScoreDefaultMaxRTT)
		widened := score(t, 5, 300)

		if atDefaults == widened {
			t.Fatalf("score is %v for both the [%d,%d] and the [5,300] window: rttMin/rttMax are not reaching the calculation",
				atDefaults, ServerScoreDefaultMinRTT, ServerScoreDefaultMaxRTT)
		}
	})

	t.Run("zero means default, not zero", func(t *testing.T) {
		t.Parallel()

		if unset, explicit := score(t, 0, 0), score(t, ServerScoreDefaultMinRTT, ServerScoreDefaultMaxRTT); unset != explicit {
			t.Fatalf("score with an unset window = %v, with the defaults spelled out = %v; the two must agree", unset, explicit)
		}
	})

	t.Run("rttMax bounds what is scorable", func(t *testing.T) {
		t.Parallel()

		// 55ms is present in the input, so a 50ms ceiling must reject it
		// rather than score against a window the pings fall outside of.
		if _, err := CalculateServerScore(teams(), 5, 50, 0); err == nil {
			t.Fatal("CalculateServerScore(_, 5, 50, 0) returned no error for a 55ms ping above the 50ms rttMax")
		}
	})
}
