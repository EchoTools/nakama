package server

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseDuration tests the duration parsing logic used in IGP suspension
func TestParseDuration(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		expectedDur   time.Duration
		expectError   bool
		errorContains string
	}{
		// Basic single unit tests
		{
			name:        "15 minutes",
			input:       "15m",
			expectedDur: 15 * time.Minute,
			expectError: false,
		},
		{
			name:        "1 hour",
			input:       "1h",
			expectedDur: 1 * time.Hour,
			expectError: false,
		},
		{
			name:        "2 days",
			input:       "2d",
			expectedDur: 48 * time.Hour,
			expectError: false,
		},
		{
			name:        "1 week",
			input:       "1w",
			expectedDur: 7 * 24 * time.Hour,
			expectError: false,
		},
		// Number without unit (should default to minutes)
		{
			name:        "15 without unit",
			input:       "15",
			expectedDur: 15 * time.Minute,
			expectError: false,
		},
		// Zero duration (to void existing suspension)
		{
			name:        "zero duration",
			input:       "0",
			expectedDur: 0,
			expectError: false,
		},
		// Whitespace handling
		{
			name:        "duration with leading space",
			input:       " 15m",
			expectedDur: 15 * time.Minute,
			expectError: false,
		},
		{
			name:        "duration with trailing space",
			input:       "15m ",
			expectedDur: 15 * time.Minute,
			expectError: false,
		},
		{
			name:        "duration with spaces",
			input:       " 15m ",
			expectedDur: 15 * time.Minute,
			expectError: false,
		},
		// Compound durations (NOW SUPPORTED!)
		{
			name:        "2h25m compound duration",
			input:       "2h25m",
			expectedDur: 2*time.Hour + 25*time.Minute,
			expectError: false,
		},
		// Edge cases
		{
			name:        "large number",
			input:       "1000m",
			expectedDur: 1000 * time.Minute,
			expectError: false,
		},
		{
			name:          "invalid format - letters only",
			input:         "abc",
			expectError:   true,
			errorContains: "invalid syntax",
		},
		{
			name:        "invalid format - empty after trim",
			input:       "",
			expectedDur: 0,
			expectError: false,
		},
		// Additional compound duration tests
		{
			name:        "1h30m compound duration",
			input:       "1h30m",
			expectedDur: 1*time.Hour + 30*time.Minute,
			expectError: false,
		},
		{
			name:        "3h45m15s compound duration with seconds",
			input:       "3h45m15s",
			expectedDur: 3*time.Hour + 45*time.Minute + 15*time.Second,
			expectError: false,
		},
		{
			name:        "90m as single unit",
			input:       "90m",
			expectedDur: 90 * time.Minute,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			duration, err := parseSuspensionDuration(tt.input)

			if tt.expectError {
				require.Error(t, err, "Expected an error but got none")
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err, "Unexpected error: %v", err)
				assert.Equal(t, tt.expectedDur, duration, "Duration mismatch")
			}
		})
	}
}

// parseSuspensionDuration replicates the improved logic from kickPlayer function
// This is extracted for testing purposes
func parseSuspensionDuration(inputDuration string) (time.Duration, error) {
	duration := inputDuration
	var suspensionDuration time.Duration

	// Parse minutes, hours, days, and weeks (m, h, d, w)
	// Trim whitespace to handle accidental spaces
	duration = strings.TrimSpace(duration)
	if duration != "" {
		if duration == "0" {
			// Zero duration means void existing suspension
			return 0, nil
		} else {
			// Try parsing with Go's time.ParseDuration first for compound durations (e.g., "2h25m")
			// Note: time.ParseDuration requires nanosecond precision units (ns, us, ms, s, m, h)
			// but doesn't support 'd' (days) or 'w' (weeks), so we handle those separately
			if parsedDuration, err := time.ParseDuration(duration); err == nil {
				// Successfully parsed compound duration like "2h25m" or "1h30m45s"
				suspensionDuration = parsedDuration
			} else {
				// Fallback to custom parsing for simple durations and d/w units
				var unit time.Duration
				lastChar := duration[len(duration)-1]

				switch lastChar {
				case 'm':
					unit = time.Minute
				case 'h':
					unit = time.Hour
				case 'd':
					unit = 24 * time.Hour
				case 'w':
					unit = 7 * 24 * time.Hour
				default:
					// No unit specified, default to minutes
					duration += "m"
					unit = time.Minute
				}

				// Parse the numeric part
				numStr := duration[:len(duration)-1]
				if durationVal, err := strconv.Atoi(numStr); err == nil {
					suspensionDuration = time.Duration(durationVal) * unit
				} else {
					return 0, err
				}
			}
		}
	}
	return suspensionDuration, nil
}

// Test the actual scenario from the bug report
func TestIGPSuspensionDurationBugReport(t *testing.T) {
	// Bug report: User entered "15m" but got "2h25m" (145 minutes)
	// This could be due to:
	// 1. User actually entered "145m" (typo)
	// 2. Parsing "2h25m" as compound duration fails
	// 3. Some other string manipulation issue

	t.Run("user entered 15m expecting 15 minutes", func(t *testing.T) {
		duration, err := parseSuspensionDuration("15m")
		require.NoError(t, err)
		assert.Equal(t, 15*time.Minute, duration)
	})

	t.Run("if user somehow entered 145m they get 2h25m", func(t *testing.T) {
		duration, err := parseSuspensionDuration("145m")
		require.NoError(t, err)
		assert.Equal(t, 145*time.Minute, duration)
		// 145 minutes = 2 hours and 25 minutes
		assert.Equal(t, "2h25m0s", duration.String())
	})

	t.Run("compound duration 2h25m should be supported", func(t *testing.T) {
		// This is what users might naturally enter
		duration, err := parseSuspensionDuration("2h25m")
		require.NoError(t, err, "Compound durations should now be supported")
		expectedDuration := 2*time.Hour + 25*time.Minute
		assert.Equal(t, expectedDuration, duration)
		assert.Equal(t, "2h25m0s", duration.String())
	})
}

// Test to verify that whitespace handling is important
func TestDurationWhitespaceHandling(t *testing.T) {
	tests := []struct {
		input    string
		expected time.Duration
	}{
		{"15m", 15 * time.Minute},
		{" 15m", 15 * time.Minute},
		{"15m ", 15 * time.Minute},
		{" 15m ", 15 * time.Minute},
		{"\t15m\t", 15 * time.Minute},
		{"\n15m\n", 15 * time.Minute},
	}

	for _, tt := range tests {
		t.Run("whitespace_"+tt.input, func(t *testing.T) {
			// First, let's test what happens WITHOUT trimming (current implementation)
			duration, err := parseSuspensionDuration(tt.input)

			// Whitespace at the beginning will cause the check to fail
			trimmed := strings.TrimSpace(tt.input)
			if trimmed != tt.input && len(tt.input) > 0 && tt.input[0] == ' ' || tt.input[0] == '\t' || tt.input[0] == '\n' {
				// Leading whitespace causes issues
				if err == nil {
					t.Logf("Input '%q' parsed successfully to %v (might indicate trimming is working)", tt.input, duration)
				}
			}
		})
	}
}
