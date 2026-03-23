package server

import (
	"testing"
)

// Test ForgiveLastQuit behavior
func TestEarlyQuitPlayerState_ForgiveLastQuit_Basic(t *testing.T) {
	tests := []struct {
		name                     string
		initialCompletedMatches  int32
		initialEarlyQuits        int32
		expectedEarlyQuits       int32
		expectedCompletedMatches int32
	}{
		{
			name:                     "Forgive with early quits",
			initialCompletedMatches:  5,
			initialEarlyQuits:        7,
			expectedEarlyQuits:       6,
			expectedCompletedMatches: 5, // Should not change
		},
		{
			name:                     "Forgive with one early quit",
			initialCompletedMatches:  10,
			initialEarlyQuits:        1,
			expectedEarlyQuits:       0,
			expectedCompletedMatches: 10, // Should not change
		},
		{
			name:                     "Forgive at zero early quits",
			initialCompletedMatches:  20,
			initialEarlyQuits:        0,
			expectedEarlyQuits:       0, // Should stay at 0
			expectedCompletedMatches: 20,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := NewEarlyQuitPlayerState()
			config.TotalCompletedMatches = tt.initialCompletedMatches
			config.NumEarlyQuits = tt.initialEarlyQuits

			config.ForgiveLastQuit()

			if config.NumEarlyQuits != tt.expectedEarlyQuits {
				t.Errorf("NumEarlyQuits = %v, want %v", config.NumEarlyQuits, tt.expectedEarlyQuits)
			}

			if config.TotalCompletedMatches != tt.expectedCompletedMatches {
				t.Errorf("TotalCompletedMatches = %v, want %v (should not change)", config.TotalCompletedMatches, tt.expectedCompletedMatches)
			}
		})
	}
}

// Test that ForgiveLastQuit and IncrementCompletedMatches behave differently
func TestEarlyQuitPlayerState_ForgiveLastQuit_vs_IncrementCompletedMatches(t *testing.T) {
	// Setup for ForgiveLastQuit
	config1 := NewEarlyQuitPlayerState()
	config1.TotalCompletedMatches = 10
	config1.NumEarlyQuits = 5

	// Setup for IncrementCompletedMatches
	config2 := NewEarlyQuitPlayerState()
	config2.TotalCompletedMatches = 10
	config2.NumEarlyQuits = 5

	config1.ForgiveLastQuit()
	config2.IncrementCompletedMatches()

	// ForgiveLastQuit should decrement NumEarlyQuits
	if config1.NumEarlyQuits != 4 {
		t.Errorf("ForgiveLastQuit should decrement NumEarlyQuits to 4, got %v", config1.NumEarlyQuits)
	}

	// IncrementCompletedMatches should not change NumEarlyQuits
	if config2.NumEarlyQuits != 5 {
		t.Errorf("IncrementCompletedMatches should not change NumEarlyQuits, got %v", config2.NumEarlyQuits)
	}

	// ForgiveLastQuit should not change TotalCompletedMatches
	if config1.TotalCompletedMatches != 10 {
		t.Errorf("ForgiveLastQuit should not change TotalCompletedMatches, got %v", config1.TotalCompletedMatches)
	}

	// IncrementCompletedMatches should increment TotalCompletedMatches
	if config2.TotalCompletedMatches != 11 {
		t.Errorf("IncrementCompletedMatches should increment TotalCompletedMatches, got %v", config2.TotalCompletedMatches)
	}
}

// Test ForgiveLastQuit decrements NumSteadyEarlyQuits
func TestEarlyQuitPlayerState_ForgiveLastQuit_DecrementsSteadyQuits(t *testing.T) {
	config := NewEarlyQuitPlayerState()
	config.NumEarlyQuits = 3
	config.NumSteadyEarlyQuits = 2

	config.ForgiveLastQuit()

	if config.NumEarlyQuits != 2 {
		t.Errorf("NumEarlyQuits = %v, want 2", config.NumEarlyQuits)
	}
	if config.NumSteadyEarlyQuits != 1 {
		t.Errorf("NumSteadyEarlyQuits = %v, want 1", config.NumSteadyEarlyQuits)
	}
}

// Test ForgiveLastQuit integration with tier updates
func TestForgiveLastQuit_IntegrationWithTierUpdate(t *testing.T) {
	config := NewEarlyQuitPlayerState()

	// Start with penalty that puts player in Tier 2
	config.PenaltyLevel = 2
	config.TotalCompletedMatches = 10
	config.NumEarlyQuits = 5

	oldTier, newTier, changed := config.UpdateTier(ptrInt32(0))
	if newTier != MatchmakingTier2 {
		t.Fatalf("Expected to start in Tier 2, got %d", newTier)
	}

	// Forgive quit (caller resolves penalty level afterward)
	config.ForgiveLastQuit()
	config.PenaltyLevel = 1 // Caller re-resolves penalty level

	// Still in Tier 2 because penalty is 1 (above threshold of 0)
	oldTier, newTier, changed = config.UpdateTier(ptrInt32(0))
	if changed {
		t.Error("Tier should not change yet (penalty=1, threshold=0)")
	}
	if newTier != MatchmakingTier2 {
		t.Errorf("Expected to remain in Tier 2, got %d", newTier)
	}

	// Forgive again (caller resolves penalty level afterward)
	config.ForgiveLastQuit()
	config.PenaltyLevel = 0 // Caller re-resolves penalty level

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
}

// Test concurrent access to ForgiveLastQuit
func TestEarlyQuitPlayerState_ForgiveLastQuit_ConcurrentAccess(t *testing.T) {
	config := NewEarlyQuitPlayerState()
	config.NumEarlyQuits = 20

	// Run concurrent decrements
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			config.ForgiveLastQuit()
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify NumEarlyQuits was decremented correctly (20 - 10 = 10)
	quits := config.GetEarlyQuitCount()
	if quits != 10 {
		t.Errorf("NumEarlyQuits = %v, want 10 after 10 concurrent forgives from 20", quits)
	}

	// Verify statistics weren't modified
	if config.TotalCompletedMatches != 0 {
		t.Errorf("TotalCompletedMatches should be 0, got %v", config.TotalCompletedMatches)
	}
}
