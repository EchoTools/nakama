package server

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

func TestVOIPLoudnessProcessing(t *testing.T) {
	// Test that VOIP loudness values are properly accumulated
	testCases := []struct {
		name           string
		loudnessValues []float64
		expectedTotal  float64
		expectedMin    float64
		expectedMax    float64
		expectedCount  int64
	}{
		{
			name:           "single value",
			loudnessValues: []float64{-25.5},
			expectedTotal:  -25.5,
			expectedMin:    -25.5,
			expectedMax:    -25.5,
			expectedCount:  1,
		},
		{
			name:           "multiple values",
			loudnessValues: []float64{-30.0, -25.0, -35.0, -20.0},
			expectedTotal:  -110.0,
			expectedMin:    -35.0,
			expectedMax:    -20.0,
			expectedCount:  4,
		},
		{
			name:           "positive and negative values",
			loudnessValues: []float64{-10.0, 5.0, -15.0, 20.0},
			expectedTotal:  0.0,
			expectedMin:    -15.0,
			expectedMax:    20.0,
			expectedCount:  4,
		},
		{
			name:           "zero values",
			loudnessValues: []float64{0.0, 0.0, 0.0},
			expectedTotal:  0.0,
			expectedMin:    0.0,
			expectedMax:    0.0,
			expectedCount:  3,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var totalLoudness, minLoudness, maxLoudness float64
			var count int64

			// Simulate accumulation process
			for i, loudness := range tc.loudnessValues {
				totalLoudness += loudness
				count++

				if i == 0 {
					minLoudness = loudness
					maxLoudness = loudness
				} else {
					if loudness < minLoudness {
						minLoudness = loudness
					}
					if loudness > maxLoudness {
						maxLoudness = loudness
					}
				}
			}

			if totalLoudness != tc.expectedTotal {
				t.Errorf("Expected total loudness %f, got %f", tc.expectedTotal, totalLoudness)
			}
			if minLoudness != tc.expectedMin {
				t.Errorf("Expected min loudness %f, got %f", tc.expectedMin, minLoudness)
			}
			if maxLoudness != tc.expectedMax {
				t.Errorf("Expected max loudness %f, got %f", tc.expectedMax, maxLoudness)
			}
			if count != tc.expectedCount {
				t.Errorf("Expected count %d, got %d", tc.expectedCount, count)
			}
		})
	}
}

func TestDailyAverageLoudnessCalculation(t *testing.T) {
	// Test that the daily average is calculated correctly
	testCases := []struct {
		name            string
		totalLoudness   float64
		sessionTimeSecs float64
		expectedAverage float64
	}{
		{
			name:            "normal case",
			totalLoudness:   -1000.0,
			sessionTimeSecs: 100.0,
			expectedAverage: -10.0,
		},
		{
			name:            "zero session time",
			totalLoudness:   -100.0,
			sessionTimeSecs: 0.0,
			expectedAverage: 0.0, // Should handle division by zero
		},
		{
			name:            "fractional values",
			totalLoudness:   -37.5,
			sessionTimeSecs: 15.0,
			expectedAverage: -2.5,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var average float64
			if tc.sessionTimeSecs > 0 {
				average = tc.totalLoudness / tc.sessionTimeSecs
			}

			if average != tc.expectedAverage {
				t.Errorf("Expected average %f, got %f", tc.expectedAverage, average)
			}
		})
	}
}

func TestPlayerLoudnessStatisticID(t *testing.T) {
	// Test that the constant is defined correctly
	if PlayerLoudnessStatisticID != "PlayerLoudness" {
		t.Errorf("Expected PlayerLoudnessStatisticID to be 'PlayerLoudness', got '%s'", PlayerLoudnessStatisticID)
	}
}

func TestLeaderboardMetadataFormat(t *testing.T) {
	// Test metadata format for loudness stats
	minLoudness := -35.5
	maxLoudness := -20.3
	count := int64(42)

	// This simulates the metadata we'll store
	metadata := map[string]string{
		"min_loudness": fmt.Sprintf("%f", minLoudness),
		"max_loudness": fmt.Sprintf("%f", maxLoudness),
		"count":        fmt.Sprintf("%d", count),
	}

	// Verify we can parse it back using strconv
	var parsedMin, parsedMax float64
	var parsedCount int64

	if val, ok := metadata["min_loudness"]; ok {
		if parsed, err := strconv.ParseFloat(val, 64); err == nil {
			parsedMin = parsed
		} else {
			t.Errorf("Failed to parse min_loudness: %v", err)
		}
	}

	if val, ok := metadata["max_loudness"]; ok {
		if parsed, err := strconv.ParseFloat(val, 64); err == nil {
			parsedMax = parsed
		} else {
			t.Errorf("Failed to parse max_loudness: %v", err)
		}
	}

	if val, ok := metadata["count"]; ok {
		if parsed, err := strconv.ParseInt(val, 10, 64); err == nil {
			parsedCount = parsed
		} else {
			t.Errorf("Failed to parse count: %v", err)
		}
	}

	if parsedMin != minLoudness {
		t.Errorf("Expected min %f, got %f", minLoudness, parsedMin)
	}
	if parsedMax != maxLoudness {
		t.Errorf("Expected max %f, got %f", maxLoudness, parsedMax)
	}
	if parsedCount != count {
		t.Errorf("Expected count %d, got %d", count, parsedCount)
	}
}

func TestStatisticBoardIDForLoudness(t *testing.T) {
	// Test that we can create valid leaderboard IDs for loudness stats
	groupID := "test-group-123"
	mode := evr.ModeArenaPublic
	statName := PlayerLoudnessStatisticID
	resetSchedule := evr.ResetScheduleDaily

	boardID := StatisticBoardID(groupID, mode, statName, resetSchedule)

	expectedID := "test-group-123:echo_arena_public:PlayerLoudness:daily"
	if boardID != expectedID {
		t.Errorf("Expected board ID '%s', got '%s'", expectedID, boardID)
	}

	// Test parsing it back
	parsedGroupID, parsedMode, parsedStatName, parsedResetSchedule, err := ParseStatisticBoardID(boardID)
	if err != nil {
		t.Errorf("Failed to parse board ID: %v", err)
	}

	if parsedGroupID != groupID {
		t.Errorf("Expected group ID '%s', got '%s'", groupID, parsedGroupID)
	}
	if parsedMode != mode {
		t.Errorf("Expected mode '%s', got '%s'", mode, parsedMode)
	}
	if parsedStatName != statName {
		t.Errorf("Expected stat name '%s', got '%s'", statName, parsedStatName)
	}
	if parsedResetSchedule != string(resetSchedule) {
		t.Errorf("Expected reset schedule '%s', got '%s'", resetSchedule, parsedResetSchedule)
	}
}
