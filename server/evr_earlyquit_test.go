package server

import (
	"sync"
	"testing"
)

func TestEarlyQuitConfig_UpdateTier(t *testing.T) {
	tests := []struct {
		name           string
		penaltyLevel   int32
		tier1Threshold int32
		expectedTier   int32
	}{
		{
			name:           "New player with no penalty stays in Tier 1",
			penaltyLevel:   0,
			tier1Threshold: 0,
			expectedTier:   MatchmakingTier1,
		},
		{
			name:           "Player with negative penalty stays in Tier 1",
			penaltyLevel:   -1,
			tier1Threshold: 0,
			expectedTier:   MatchmakingTier1,
		},
		{
			name:           "Player with penalty level 1 moves to Tier 2 (default threshold)",
			penaltyLevel:   1,
			tier1Threshold: 0,
			expectedTier:   MatchmakingTier2,
		},
		{
			name:           "Player with penalty level 2 stays in Tier 2",
			penaltyLevel:   2,
			tier1Threshold: 0,
			expectedTier:   MatchmakingTier2,
		},
		{
			name:           "Player with penalty level 3 (max) stays in Tier 2",
			penaltyLevel:   3,
			tier1Threshold: 0,
			expectedTier:   MatchmakingTier2,
		},
		{
			name:           "Custom threshold: penalty 1 stays in Tier 1 with threshold 1",
			penaltyLevel:   1,
			tier1Threshold: 1,
			expectedTier:   MatchmakingTier1,
		},
		{
			name:           "Custom threshold: penalty 2 moves to Tier 2 with threshold 1",
			penaltyLevel:   2,
			tier1Threshold: 1,
			expectedTier:   MatchmakingTier2,
		},
		{
			name:           "Custom threshold: penalty 2 stays in Tier 1 with threshold 2",
			penaltyLevel:   2,
			tier1Threshold: 2,
			expectedTier:   MatchmakingTier1,
		},
		{
			name:           "Boundary: penalty equals threshold stays in Tier 1",
			penaltyLevel:   0,
			tier1Threshold: 0,
			expectedTier:   MatchmakingTier1,
		},
		{
			name:           "Boundary: penalty just above threshold moves to Tier 2",
			penaltyLevel:   1,
			tier1Threshold: 0,
			expectedTier:   MatchmakingTier2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := NewEarlyQuitConfig()
			config.EarlyQuitPenaltyLevel = tt.penaltyLevel

			oldTier, newTier, changed := config.UpdateTier(tt.tier1Threshold)

			if newTier != tt.expectedTier {
				t.Errorf("UpdateTier() newTier = %v, want %v", newTier, tt.expectedTier)
			}

			if oldTier != 0 {
				t.Errorf("UpdateTier() oldTier = %v, want 0 (initial tier)", oldTier)
			}

			// For cases where we start at tier 0 and stay at tier 0, changed should be false
			expectChanged := (tt.expectedTier != MatchmakingTier1)
			if changed != expectChanged {
				t.Errorf("UpdateTier() changed = %v, want %v", changed, expectChanged)
			}

			if config.MatchmakingTier != tt.expectedTier {
				t.Errorf("config.MatchmakingTier = %v, want %v", config.MatchmakingTier, tt.expectedTier)
			}
		})
	}
}

func TestEarlyQuitConfig_UpdateTier_NoChange(t *testing.T) {
	config := NewEarlyQuitConfig()
	config.EarlyQuitPenaltyLevel = 1
	config.MatchmakingTier = MatchmakingTier2

	oldTier, newTier, changed := config.UpdateTier(0)

	if oldTier != MatchmakingTier2 {
		t.Errorf("oldTier = %v, want %v", oldTier, MatchmakingTier2)
	}

	if newTier != MatchmakingTier2 {
		t.Errorf("newTier = %v, want %v", newTier, MatchmakingTier2)
	}

	if changed {
		t.Errorf("changed = true, want false when tier doesn't change")
	}
}

func TestEarlyQuitConfig_UpdateTier_TierChange(t *testing.T) {
	config := NewEarlyQuitConfig()
	config.EarlyQuitPenaltyLevel = 1
	config.MatchmakingTier = MatchmakingTier2

	// Reduce penalty to move back to Tier 1
	config.EarlyQuitPenaltyLevel = 0

	oldTier, newTier, changed := config.UpdateTier(0)

	if oldTier != MatchmakingTier2 {
		t.Errorf("oldTier = %v, want %v", oldTier, MatchmakingTier2)
	}

	if newTier != MatchmakingTier1 {
		t.Errorf("newTier = %v, want %v", newTier, MatchmakingTier1)
	}

	if !changed {
		t.Errorf("changed = false, want true when tier changes")
	}

	if config.LastTierChange.IsZero() {
		t.Error("LastTierChange should be set when tier changes")
	}
}

func TestEarlyQuitConfig_IncrementEarlyQuit(t *testing.T) {
	config := NewEarlyQuitConfig()
	initialLevel := config.EarlyQuitPenaltyLevel

	config.IncrementEarlyQuit()

	if config.EarlyQuitPenaltyLevel != initialLevel+1 {
		t.Errorf("EarlyQuitPenaltyLevel = %v, want %v", config.EarlyQuitPenaltyLevel, initialLevel+1)
	}

	if config.LastEarlyQuitTime.IsZero() {
		t.Error("LastEarlyQuitTime should be set after IncrementEarlyQuit")
	}
}

func TestEarlyQuitConfig_IncrementEarlyQuit_MaxPenalty(t *testing.T) {
	config := NewEarlyQuitConfig()
	config.EarlyQuitPenaltyLevel = MaxEarlyQuitPenaltyLevel

	config.IncrementEarlyQuit()

	if config.EarlyQuitPenaltyLevel != MaxEarlyQuitPenaltyLevel {
		t.Errorf("EarlyQuitPenaltyLevel = %v, want %v (max)", config.EarlyQuitPenaltyLevel, MaxEarlyQuitPenaltyLevel)
	}
}

func TestEarlyQuitConfig_IncrementCompletedMatches(t *testing.T) {
	config := NewEarlyQuitConfig()
	config.EarlyQuitPenaltyLevel = 2

	config.IncrementCompletedMatches()

	if config.EarlyQuitPenaltyLevel != 1 {
		t.Errorf("EarlyQuitPenaltyLevel = %v, want 1", config.EarlyQuitPenaltyLevel)
	}

	if !config.LastEarlyQuitTime.IsZero() {
		t.Error("LastEarlyQuitTime should be cleared after IncrementCompletedMatches")
	}
}

func TestEarlyQuitConfig_IncrementCompletedMatches_MinPenalty(t *testing.T) {
	config := NewEarlyQuitConfig()
	config.EarlyQuitPenaltyLevel = MinEarlyQuitPenaltyLevel

	config.IncrementCompletedMatches()

	if config.EarlyQuitPenaltyLevel != MinEarlyQuitPenaltyLevel {
		t.Errorf("EarlyQuitPenaltyLevel = %v, want %v (min)", config.EarlyQuitPenaltyLevel, MinEarlyQuitPenaltyLevel)
	}
}

func TestEarlyQuitConfig_GetTier(t *testing.T) {
	config := NewEarlyQuitConfig()
	config.MatchmakingTier = MatchmakingTier2

	tier := config.GetTier()

	if tier != MatchmakingTier2 {
		t.Errorf("GetTier() = %v, want %v", tier, MatchmakingTier2)
	}
}

func TestEarlyQuitConfig_GetPenaltyLevel(t *testing.T) {
	config := NewEarlyQuitConfig()
	config.EarlyQuitPenaltyLevel = 2

	level := config.GetPenaltyLevel()

	if level != 2 {
		t.Errorf("GetPenaltyLevel() = %v, want 2", level)
	}
}

func TestEarlyQuitConfig_ConcurrentAccess(t *testing.T) {
	config := NewEarlyQuitConfig()
	var wg sync.WaitGroup

	// Simulate concurrent early quits and completed matches
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			config.IncrementEarlyQuit()
		}()
		go func() {
			defer wg.Done()
			config.IncrementCompletedMatches()
		}()
	}

	wg.Wait()

	// Verify the config is still in a valid state
	level := config.GetPenaltyLevel()
	if level < int(MinEarlyQuitPenaltyLevel) || level > int(MaxEarlyQuitPenaltyLevel) {
		t.Errorf("Penalty level %v is outside valid range [%v, %v]", level, MinEarlyQuitPenaltyLevel, MaxEarlyQuitPenaltyLevel)
	}
}

func TestEarlyQuitConfig_ConcurrentUpdateTier(t *testing.T) {
	config := NewEarlyQuitConfig()
	config.EarlyQuitPenaltyLevel = 1
	var wg sync.WaitGroup

	// Simulate concurrent tier updates
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			config.UpdateTier(0)
		}()
	}

	wg.Wait()

	// Verify tier is consistent
	tier := config.GetTier()
	if tier != MatchmakingTier2 {
		t.Errorf("GetTier() = %v, want %v after concurrent updates", tier, MatchmakingTier2)
	}
}

func TestEarlyQuitConfig_TierTransitionFlow(t *testing.T) {
	config := NewEarlyQuitConfig()

	// Start in Tier 1
	_, tier, _ := config.UpdateTier(0)
	if tier != MatchmakingTier1 {
		t.Errorf("Initial tier = %v, want %v", tier, MatchmakingTier1)
	}

	// Early quit moves to Tier 2
	config.IncrementEarlyQuit()
	oldTier, newTier, changed := config.UpdateTier(0)
	if oldTier != MatchmakingTier1 || newTier != MatchmakingTier2 || !changed {
		t.Errorf("After early quit: oldTier=%v, newTier=%v, changed=%v, want Tier1→Tier2", oldTier, newTier, changed)
	}

	// Complete match returns to Tier 1
	config.IncrementCompletedMatches()
	oldTier, newTier, changed = config.UpdateTier(0)
	if oldTier != MatchmakingTier2 || newTier != MatchmakingTier1 || !changed {
		t.Errorf("After completed match: oldTier=%v, newTier=%v, changed=%v, want Tier2→Tier1", oldTier, newTier, changed)
	}
}

func TestCalculatePlayerReliabilityRating(t *testing.T) {
	tests := []struct {
		name             string
		earlyQuits       int32
		completedMatches int32
		expectedRating   float64
	}{
		{
			name:             "No matches played",
			earlyQuits:       0,
			completedMatches: 0,
			expectedRating:   1.0,
		},
		{
			name:             "All matches completed",
			earlyQuits:       0,
			completedMatches: 10,
			expectedRating:   1.0,
		},
		{
			name:             "All matches early quit",
			earlyQuits:       10,
			completedMatches: 0,
			expectedRating:   0.0,
		},
		{
			name:             "Half early quit",
			earlyQuits:       5,
			completedMatches: 5,
			expectedRating:   0.5,
		},
		{
			name:             "75% completion rate",
			earlyQuits:       25,
			completedMatches: 75,
			expectedRating:   0.75,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rating := CalculatePlayerReliabilityRating(tt.earlyQuits, tt.completedMatches)
			if rating != tt.expectedRating {
				t.Errorf("CalculatePlayerReliabilityRating() = %v, want %v", rating, tt.expectedRating)
			}
		})
	}
}
