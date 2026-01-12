package server

import (
	"testing"
	"time"
)

// Test DecrementPenaltyOnly behavior
func TestEarlyQuitConfig_DecrementPenaltyOnly_Basic(t *testing.T) {
	tests := []struct {
		name                     string
		initialPenalty           int32
		initialCompletedMatches  int32
		initialEarlyQuits        int32
		expectedPenalty          int32
		expectedCompletedMatches int32
	}{
		{
			name:                     "Decrement from penalty 2",
			initialPenalty:           2,
			initialCompletedMatches:  5,
			initialEarlyQuits:        7,
			expectedPenalty:          1,
			expectedCompletedMatches: 5, // Should not change
		},
		{
			name:                     "Decrement from penalty 1",
			initialPenalty:           1,
			initialCompletedMatches:  10,
			initialEarlyQuits:        11,
			expectedPenalty:          0,
			expectedCompletedMatches: 10, // Should not change
		},
		{
			name:                     "Decrement at minimum penalty",
			initialPenalty:           MinEarlyQuitPenaltyLevel,
			initialCompletedMatches:  20,
			initialEarlyQuits:        1,
			expectedPenalty:          MinEarlyQuitPenaltyLevel, // Should stay at min
			expectedCompletedMatches: 20,                       // Should not change
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := NewEarlyQuitConfig()
			config.EarlyQuitPenaltyLevel = tt.initialPenalty
			config.TotalCompletedMatches = tt.initialCompletedMatches
			config.TotalEarlyQuits = tt.initialEarlyQuits
			initialReliabilityRating := CalculatePlayerReliabilityRating(tt.initialEarlyQuits, tt.initialCompletedMatches)
			config.PlayerReliabilityRating = initialReliabilityRating

			config.DecrementPenaltyOnly()

			if config.EarlyQuitPenaltyLevel != tt.expectedPenalty {
				t.Errorf("EarlyQuitPenaltyLevel = %v, want %v", config.EarlyQuitPenaltyLevel, tt.expectedPenalty)
			}

			if config.TotalCompletedMatches != tt.expectedCompletedMatches {
				t.Errorf("TotalCompletedMatches = %v, want %v (should not change)", config.TotalCompletedMatches, tt.expectedCompletedMatches)
			}

			// Reliability rating should not change
			if config.PlayerReliabilityRating != initialReliabilityRating {
				t.Errorf("PlayerReliabilityRating = %v, want %v (should not change)", config.PlayerReliabilityRating, initialReliabilityRating)
			}

			if !config.LastEarlyQuitTime.IsZero() {
				t.Error("LastEarlyQuitTime should be cleared after DecrementPenaltyOnly")
			}
		})
	}
}

// Test that DecrementPenaltyOnly and IncrementCompletedMatches behave differently
func TestEarlyQuitConfig_DecrementPenaltyOnly_vs_IncrementCompletedMatches(t *testing.T) {
	// Setup for DecrementPenaltyOnly
	config1 := NewEarlyQuitConfig()
	config1.EarlyQuitPenaltyLevel = 2
	config1.TotalCompletedMatches = 10
	config1.TotalEarlyQuits = 5
	config1.PlayerReliabilityRating = CalculatePlayerReliabilityRating(5, 10)

	// Setup for IncrementCompletedMatches
	config2 := NewEarlyQuitConfig()
	config2.EarlyQuitPenaltyLevel = 2
	config2.TotalCompletedMatches = 10
	config2.TotalEarlyQuits = 5
	config2.PlayerReliabilityRating = CalculatePlayerReliabilityRating(5, 10)

	config1.DecrementPenaltyOnly()
	config2.IncrementCompletedMatches()

	// Both should decrement penalty
	if config1.EarlyQuitPenaltyLevel != 1 || config2.EarlyQuitPenaltyLevel != 1 {
		t.Errorf("Both should decrement penalty to 1, got config1=%v, config2=%v", config1.EarlyQuitPenaltyLevel, config2.EarlyQuitPenaltyLevel)
	}

	// But only IncrementCompletedMatches should increment the completed matches counter
	if config1.TotalCompletedMatches != 10 {
		t.Errorf("DecrementPenaltyOnly should not change TotalCompletedMatches, got %v", config1.TotalCompletedMatches)
	}

	if config2.TotalCompletedMatches != 11 {
		t.Errorf("IncrementCompletedMatches should increment TotalCompletedMatches, got %v", config2.TotalCompletedMatches)
	}

	// And only IncrementCompletedMatches should update the reliability rating
	if config1.PlayerReliabilityRating != CalculatePlayerReliabilityRating(5, 10) {
		t.Errorf("DecrementPenaltyOnly should not change PlayerReliabilityRating, got %v", config1.PlayerReliabilityRating)
	}

	if config2.PlayerReliabilityRating != CalculatePlayerReliabilityRating(5, 11) {
		t.Errorf("IncrementCompletedMatches should update PlayerReliabilityRating, got %v", config2.PlayerReliabilityRating)
	}
}

// Test DecrementPenaltyOnly clears LastEarlyQuitTime
func TestEarlyQuitConfig_DecrementPenaltyOnly_ClearsLastEarlyQuitTime(t *testing.T) {
	config := NewEarlyQuitConfig()
	config.EarlyQuitPenaltyLevel = 2
	config.LastEarlyQuitTime = time.Now().UTC()

	if config.LastEarlyQuitTime.IsZero() {
		t.Fatal("LastEarlyQuitTime should be set before test")
	}

	config.DecrementPenaltyOnly()

	if !config.LastEarlyQuitTime.IsZero() {
		t.Error("LastEarlyQuitTime should be cleared after DecrementPenaltyOnly")
	}
}

// Test DecrementPenaltyOnly integration with tier updates
func TestDecrementPenaltyOnly_IntegrationWithTierUpdate(t *testing.T) {
	config := NewEarlyQuitConfig()

	// Start with penalty that puts player in Tier 2
	config.EarlyQuitPenaltyLevel = 2
	config.TotalCompletedMatches = 10
	config.TotalEarlyQuits = 5
	config.PlayerReliabilityRating = CalculatePlayerReliabilityRating(5, 10)

	oldTier, newTier, changed := config.UpdateTier(ptrInt32(0))
	if newTier != MatchmakingTier2 {
		t.Fatalf("Expected to start in Tier 2, got %d", newTier)
	}

	// Decrement penalty only (simulating logout forgiveness)
	config.DecrementPenaltyOnly()

	// Still in Tier 2 because penalty is 1 (above threshold of 0)
	oldTier, newTier, changed = config.UpdateTier(ptrInt32(0))
	if changed {
		t.Error("Tier should not change yet (penalty=1, threshold=0)")
	}
	if newTier != MatchmakingTier2 {
		t.Errorf("Expected to remain in Tier 2, got %d", newTier)
	}

	// Decrement penalty again
	config.DecrementPenaltyOnly()

	// Now should return to Tier 1 (penalty=0, threshold=0)
	oldTier, newTier, changed = config.UpdateTier(ptrInt32(0))
	if !changed {
		t.Error("Tier should change from Tier 2 to Tier 1")
	}
	if oldTier != MatchmakingTier2 {
		t.Errorf("Expected oldTier to be Tier 2, got %d", oldTier)
	}
	if newTier != MatchmakingTier1 {
		t.Errorf("Expected newTier to be Tier 1, got %d", newTier)
	}

	// Verify match statistics weren't inflated
	if config.TotalCompletedMatches != 10 {
		t.Errorf("TotalCompletedMatches should remain 10, got %d", config.TotalCompletedMatches)
	}
	if config.PlayerReliabilityRating != CalculatePlayerReliabilityRating(5, 10) {
		t.Errorf("PlayerReliabilityRating should remain unchanged, got %f", config.PlayerReliabilityRating)
	}
}

// Test concurrent access to DecrementPenaltyOnly
func TestEarlyQuitConfig_DecrementPenaltyOnly_ConcurrentAccess(t *testing.T) {
	config := NewEarlyQuitConfig()
	config.EarlyQuitPenaltyLevel = MaxEarlyQuitPenaltyLevel

	// Run concurrent decrements
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			config.DecrementPenaltyOnly()
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify penalty level is within valid range
	level := config.GetPenaltyLevel()
	if level < int(MinEarlyQuitPenaltyLevel) || level > int(MaxEarlyQuitPenaltyLevel) {
		t.Errorf("Penalty level %v is outside valid range [%v, %v]", level, MinEarlyQuitPenaltyLevel, MaxEarlyQuitPenaltyLevel)
	}

	// Verify statistics weren't modified
	if config.TotalCompletedMatches != 0 {
		t.Errorf("TotalCompletedMatches should be 0, got %v", config.TotalCompletedMatches)
	}
}
