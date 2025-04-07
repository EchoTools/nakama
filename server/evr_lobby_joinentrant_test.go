package server

import (
	"testing"
	"time"
)

func TestFormatDuration(t *testing.T) {
	testCases := []struct {
		duration time.Duration
		expected string
	}{
		{0 * time.Second, "0s"},
		{1 * time.Second, "1s"},
		{30 * time.Second, "30s"},
		{60 * time.Second, "1m"},
		{90 * time.Second, "1m"},
		{60 * time.Minute, "1h"},
		{90 * time.Minute, "1h"},
		{24 * time.Hour, "1d"},
		{36 * time.Hour, "1d12h"},
		{24*time.Hour + 60*time.Minute, "1d1h"},
		{24*time.Hour + 30*time.Minute, "1d"},
		{24*time.Hour + 1*time.Second, "1d"},
		{2*time.Hour + 30*time.Minute + 15*time.Second, "2h30m"},
		{2*time.Hour + 15*time.Second, "2h"},
		{1*time.Minute + 15*time.Second, "1m"},
		{1*time.Hour + 15*time.Minute, "1h15m"},
		{1*time.Hour + 2*time.Minute + 3*time.Second, "1h2m3s"},
		{25*time.Hour + 2*time.Minute + 3*time.Second, "1d1h2m3s"},
	}

	for _, tc := range testCases {
		t.Run(tc.duration.String(), func(t *testing.T) {
			actual := formatDuration(tc.duration, true)
			if actual != tc.expected {
				t.Errorf("Expected %s, but got %s", tc.expected, actual)
			}
		})
	}
}
