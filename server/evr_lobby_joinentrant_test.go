package server

import (
	"testing"
	"time"
)

func TestFormatDuration(t *testing.T) {
	testCases := []struct {
		duration     time.Duration
		expected     string
		roundMinutes bool
	}{
		{0 * time.Second, "0s", false},
		{1 * time.Second, "1s", false},
		{30 * time.Second, "30s", false},
		{60 * time.Second, "1m", false},
		{90 * time.Second, "1m30s", false},
		{60 * time.Minute, "1h", false},
		{90 * time.Minute, "1h30m", false},
		{24 * time.Hour, "1d", false},
		{36 * time.Hour, "1d12h", false},
		{24*time.Hour + 60*time.Minute, "1d1h", false},
		{24*time.Hour + 30*time.Minute, "1d1h", false},
		{24*time.Hour + 1*time.Second, "1d", false},
		{2*time.Hour + 30*time.Minute + 15*time.Second, "2h30m", false},
		{2*time.Hour + 15*time.Second, "2h", false},
		{1*time.Minute + 15*time.Second, "1m15s", false},
		{1*time.Hour + 15*time.Minute, "1h15m", false},
		{1*time.Hour + 2*time.Minute + 3*time.Second, "1h2m", true},
		{25*time.Hour + 2*time.Minute + 3*time.Second, "1d1h", true},
		{2*time.Hour + 59*time.Minute + 59*time.Second, "3h", true},
		{-1 * (2*time.Hour + 59*time.Minute + 59*time.Second), "-3h", true},
	}

	for _, tc := range testCases {
		t.Run(tc.duration.String(), func(t *testing.T) {
			actual := formatDuration(tc.duration)
			if actual != tc.expected {
				t.Errorf("Expected %s, but got %s", tc.expected, actual)
			}
		})
	}
}
