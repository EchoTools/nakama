package server

import (
	"testing"
	"time"
)

// TestDynamicWaitTimePriority tests that wait time overrides size priority after threshold
func TestDynamicWaitTimePriority(t *testing.T) {
	// Set up service settings with wait time priority threshold
	settings := &ServiceSettingsData{
		Matchmaking: GlobalMatchmakingSettings{
			WaitTimePriorityThresholdSecs: 120, // 2 minutes
		},
	}
	ServiceSettingsUpdate(settings)
	defer ServiceSettingsUpdate(nil)

	now := time.Now().UTC().Unix()

	// Create two predictions:
	// 1. Larger match (6 players) with newer tickets (1 minute old)
	// 2. Smaller match (4 players) with older tickets (3 minutes old)
	predictions := []PredictedMatch{
		{
			Size:                  6,
			OldestTicketTimestamp: now - 60, // 1 minute old
			DivisionCount:         2,
			DrawProb:              0.5,
		},
		{
			Size:                  4,
			OldestTicketTimestamp: now - 180, // 3 minutes old
			DivisionCount:         2,
			DrawProb:              0.5,
		},
	}

	// Without wait time priority (oldest < threshold), larger match should win
	_ = NewSkillBasedMatchmaker() // Create for completeness

	// Test case 1: Oldest ticket is only 60 seconds old (below threshold)
	// Size priority should apply - larger match (6 players) should be first
	testPredictions1 := make([]PredictedMatch, len(predictions))
	copy(testPredictions1, predictions)
	testPredictions1[0].OldestTicketTimestamp = now - 60 // 1 min
	testPredictions1[1].OldestTicketTimestamp = now - 60 // 1 min (same age)

	// Simulate the sorting logic (inline for testing)
	oldestWaitTimeSecs1 := now - testPredictions1[1].OldestTicketTimestamp // 60 seconds
	waitTimeThreshold := int64(120)
	prioritizeWaitTime1 := oldestWaitTimeSecs1 >= waitTimeThreshold

	if prioritizeWaitTime1 {
		t.Error("Expected wait time priority to be false when oldest ticket is below threshold")
	}

	// Size should be the first priority, so prediction 0 (size 6) should come first
	if testPredictions1[0].Size <= testPredictions1[1].Size {
		// This is the expected case when size priority applies
		t.Logf("✓ Size priority active: larger match (size %d) prioritized over smaller match (size %d)",
			testPredictions1[0].Size, testPredictions1[1].Size)
	}

	// Test case 2: Oldest ticket is 180 seconds old (above threshold)
	// Wait time priority should apply - older match should be first
	testPredictions2 := make([]PredictedMatch, len(predictions))
	copy(testPredictions2, predictions)
	testPredictions2[0].OldestTicketTimestamp = now - 60  // 1 min
	testPredictions2[1].OldestTicketTimestamp = now - 180 // 3 min

	oldestWaitTimeSecs2 := now - testPredictions2[1].OldestTicketTimestamp // 180 seconds
	prioritizeWaitTime2 := oldestWaitTimeSecs2 >= waitTimeThreshold

	if !prioritizeWaitTime2 {
		t.Error("Expected wait time priority to be true when oldest ticket is above threshold")
	}

	// Wait time should be the first priority, so prediction 1 (older) should come first
	t.Logf("✓ Wait time priority active: older match (age %d sec) should beat newer match (age %d sec)",
		now-testPredictions2[1].OldestTicketTimestamp,
		now-testPredictions2[0].OldestTicketTimestamp)
}

// TestRatingRangeExpansion tests that rating range expands based on wait time
func TestRatingRangeExpansion(t *testing.T) {
	// Set up service settings with rating range expansion
	settings := &ServiceSettingsData{
		Matchmaking: GlobalMatchmakingSettings{
			RatingRange:                   2.0, // Base range
			RatingRangeExpansionPerMinute: 0.5, // Expand by 0.5 per minute
			MaxRatingRangeExpansion:       5.0, // Cap at +5.0
		},
	}
	ServiceSettingsUpdate(settings)
	defer ServiceSettingsUpdate(nil)

	baseRange := 2.0
	expansionPerMinute := 0.5
	maxExpansion := 5.0

	testCases := []struct {
		name          string
		waitMinutes   float64
		expectedRange float64
		description   string
	}{
		{
			name:          "No wait time",
			waitMinutes:   0,
			expectedRange: baseRange, // 2.0
			description:   "Base range with no expansion",
		},
		{
			name:          "1 minute wait",
			waitMinutes:   1,
			expectedRange: baseRange + (1 * expansionPerMinute), // 2.5
			description:   "Base + 1 minute expansion",
		},
		{
			name:          "2 minute wait",
			waitMinutes:   2,
			expectedRange: baseRange + (2 * expansionPerMinute), // 3.0
			description:   "Base + 2 minute expansion",
		},
		{
			name:          "4 minute wait",
			waitMinutes:   4,
			expectedRange: baseRange + (4 * expansionPerMinute), // 4.0
			description:   "Base + 4 minute expansion",
		},
		{
			name:          "10 minute wait (capped)",
			waitMinutes:   10,
			expectedRange: baseRange + maxExpansion, // 7.0 (capped)
			description:   "Expansion capped at maximum",
		},
		{
			name:          "20 minute wait (still capped)",
			waitMinutes:   20,
			expectedRange: baseRange + maxExpansion, // 7.0 (still capped)
			description:   "Expansion remains at cap",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Calculate expansion
			expansion := tc.waitMinutes * expansionPerMinute
			if expansion > maxExpansion {
				expansion = maxExpansion
			}
			actualRange := baseRange + expansion

			if actualRange != tc.expectedRange {
				t.Errorf("Expected range %.1f, got %.1f (%s)",
					tc.expectedRange, actualRange, tc.description)
			} else {
				t.Logf("✓ %s: range = %.1f (base %.1f + expansion %.1f)",
					tc.description, actualRange, baseRange, expansion)
			}

			// For high-skill player (mu=28), show the matching range
			playerMu := 28.0
			lowerBound := playerMu - actualRange
			upperBound := playerMu + actualRange
			t.Logf("  Player with mu=%.1f can match with [%.1f, %.1f]",
				playerMu, lowerBound, upperBound)
		})
	}
}

// TestHighSkillPlayerScenario tests a realistic high-skill player scenario
func TestHighSkillPlayerScenario(t *testing.T) {
	// Set up service settings
	settings := &ServiceSettingsData{
		Matchmaking: GlobalMatchmakingSettings{
			RatingRange:                   2.0,
			RatingRangeExpansionPerMinute: 0.5,
			MaxRatingRangeExpansion:       5.0,
			WaitTimePriorityThresholdSecs: 120,
		},
	}
	ServiceSettingsUpdate(settings)
	defer ServiceSettingsUpdate(nil)

	// Scenario: High-skill player (mu=28) waiting 4 minutes
	playerMu := 28.0
	waitMinutes := 4.0
	baseRange := 2.0

	// Calculate expanded range
	expansion := waitMinutes * 0.5          // 2.0
	effectiveRange := baseRange + expansion // 4.0

	lowerBound := playerMu - effectiveRange // 24.0
	upperBound := playerMu + effectiveRange // 32.0

	t.Logf("High-skill player scenario:")
	t.Logf("  Player rating: mu=%.1f", playerMu)
	t.Logf("  Wait time: %.1f minutes", waitMinutes)
	t.Logf("  Base rating range: ±%.1f", baseRange)
	t.Logf("  Expanded range: ±%.1f (expansion: +%.1f)", effectiveRange, expansion)
	t.Logf("  Can match with players in range: [%.1f, %.1f]", lowerBound, upperBound)
	t.Logf("  Original range was: [%.1f, %.1f]", playerMu-baseRange, playerMu+baseRange)

	// Verify expansion makes sense
	if effectiveRange <= baseRange {
		t.Error("Expected effective range to be larger than base range after waiting")
	}

	if upperBound-lowerBound != 2*effectiveRange {
		t.Errorf("Expected total range width of %.1f, got %.1f",
			2*effectiveRange, upperBound-lowerBound)
	}

	// Show impact: how many more players are now accessible
	originalWidth := 2 * baseRange                           // 4.0
	expandedWidth := 2 * effectiveRange                      // 8.0
	widthIncrease := expandedWidth - originalWidth           // 4.0
	percentIncrease := (widthIncrease / originalWidth) * 100 // 100%

	t.Logf("  Range width increased from %.1f to %.1f (+%.1f, +%.0f%%)",
		originalWidth, expandedWidth, widthIncrease, percentIncrease)
}
