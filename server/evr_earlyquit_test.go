package server

import (
	"sync"
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

// Helper function to create int32 pointers
func ptrInt32(v int32) *int32 {
	return &v
}

func TestEarlyQuitPlayerState_UpdateTier(t *testing.T) {
	tests := []struct {
		name           string
		penaltyLevel   int32
		tier1Threshold *int32
		expectedTier   int32
	}{
		{
			name:           "New player with no penalty stays in Tier 1",
			penaltyLevel:   0,
			tier1Threshold: ptrInt32(0),
			expectedTier:   MatchmakingTier1,
		},
		{
			name:           "Player with negative penalty stays in Tier 1",
			penaltyLevel:   -1,
			tier1Threshold: ptrInt32(0),
			expectedTier:   MatchmakingTier1,
		},
		{
			name:           "Player with penalty level 1 moves to Tier 2 (default threshold)",
			penaltyLevel:   1,
			tier1Threshold: ptrInt32(0),
			expectedTier:   MatchmakingTier2,
		},
		{
			name:           "Player with penalty level 2 stays in Tier 2",
			penaltyLevel:   2,
			tier1Threshold: ptrInt32(0),
			expectedTier:   MatchmakingTier2,
		},
		{
			name:           "Player with penalty level 3 (max) stays in Tier 2",
			penaltyLevel:   3,
			tier1Threshold: ptrInt32(0),
			expectedTier:   MatchmakingTier2,
		},
		{
			name:           "Custom threshold: penalty 1 stays in Tier 1 with threshold 1",
			penaltyLevel:   1,
			tier1Threshold: ptrInt32(1),
			expectedTier:   MatchmakingTier1,
		},
		{
			name:           "Custom threshold: penalty 2 moves to Tier 2 with threshold 1",
			penaltyLevel:   2,
			tier1Threshold: ptrInt32(1),
			expectedTier:   MatchmakingTier2,
		},
		{
			name:           "Custom threshold: penalty 2 stays in Tier 1 with threshold 2",
			penaltyLevel:   2,
			tier1Threshold: ptrInt32(2),
			expectedTier:   MatchmakingTier1,
		},
		{
			name:           "Boundary: penalty equals threshold stays in Tier 1",
			penaltyLevel:   0,
			tier1Threshold: ptrInt32(0),
			expectedTier:   MatchmakingTier1,
		},
		{
			name:           "Boundary: penalty just above threshold moves to Tier 2",
			penaltyLevel:   1,
			tier1Threshold: ptrInt32(0),
			expectedTier:   MatchmakingTier2,
		},
		{
			name:           "Nil threshold defaults to 0",
			penaltyLevel:   0,
			tier1Threshold: nil,
			expectedTier:   MatchmakingTier1,
		},
		{
			name:           "Nil threshold with penalty defaults to 0",
			penaltyLevel:   1,
			tier1Threshold: nil,
			expectedTier:   MatchmakingTier2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := NewEarlyQuitPlayerState()
			config.PenaltyLevel = tt.penaltyLevel

			oldTier, newTier, changed := config.UpdateTier(tt.tier1Threshold)

			if newTier != tt.expectedTier {
				t.Errorf("UpdateTier() newTier = %v, want %v", newTier, tt.expectedTier)
			}

			if oldTier != MatchmakingTier1 {
				t.Errorf("UpdateTier() oldTier = %v, want %v (initial tier)", oldTier, MatchmakingTier1)
			}

			// For cases where we start at Tier 1 and move to a higher tier, changed should be true
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

func TestEarlyQuitPlayerState_UpdateTier_NoChange(t *testing.T) {
	config := NewEarlyQuitPlayerState()
	config.PenaltyLevel = 1
	config.MatchmakingTier = MatchmakingTier2

	oldTier, newTier, changed := config.UpdateTier(ptrInt32(0))

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

func TestEarlyQuitPlayerState_UpdateTier_TierChange(t *testing.T) {
	config := NewEarlyQuitPlayerState()
	config.PenaltyLevel = 1
	config.MatchmakingTier = MatchmakingTier2

	// Reduce penalty to move back to Tier 1
	config.PenaltyLevel = 0

	oldTier, newTier, changed := config.UpdateTier(ptrInt32(0))

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

func TestEarlyQuitPlayerState_IncrementEarlyQuit(t *testing.T) {
	config := NewEarlyQuitPlayerState()
	initialQuits := config.NumEarlyQuits

	config.IncrementEarlyQuit()

	if config.NumEarlyQuits != initialQuits+1 {
		t.Errorf("NumEarlyQuits = %v, want %v", config.NumEarlyQuits, initialQuits+1)
	}

	if config.NumSteadyEarlyQuits != 1 {
		t.Errorf("NumSteadyEarlyQuits = %v, want 1", config.NumSteadyEarlyQuits)
	}
}

func TestEarlyQuitPlayerState_IncrementEarlyQuit_CountsAccumulate(t *testing.T) {
	config := NewEarlyQuitPlayerState()

	config.IncrementEarlyQuit()
	config.IncrementEarlyQuit()
	config.IncrementEarlyQuit()

	if config.NumEarlyQuits != 3 {
		t.Errorf("NumEarlyQuits = %v, want 3", config.NumEarlyQuits)
	}
}

func TestEarlyQuitPlayerState_IncrementCompletedMatches(t *testing.T) {
	config := NewEarlyQuitPlayerState()

	config.IncrementCompletedMatches()

	if config.TotalCompletedMatches != 1 {
		t.Errorf("TotalCompletedMatches = %v, want 1", config.TotalCompletedMatches)
	}

	if config.NumSteadyMatches != 1 {
		t.Errorf("NumSteadyMatches = %v, want 1", config.NumSteadyMatches)
	}
}

func TestEarlyQuitPlayerState_IncrementCompletedMatches_Accumulates(t *testing.T) {
	config := NewEarlyQuitPlayerState()
	config.TotalCompletedMatches = 10

	config.IncrementCompletedMatches()

	if config.TotalCompletedMatches != 11 {
		t.Errorf("TotalCompletedMatches = %v, want 11", config.TotalCompletedMatches)
	}
}

func TestEarlyQuitPlayerState_GetTier(t *testing.T) {
	config := NewEarlyQuitPlayerState()
	config.MatchmakingTier = MatchmakingTier2

	tier := config.GetTier()

	if tier != MatchmakingTier2 {
		t.Errorf("GetTier() = %v, want %v", tier, MatchmakingTier2)
	}
}

func TestEarlyQuitPlayerState_GetPenaltyLevel(t *testing.T) {
	config := NewEarlyQuitPlayerState()
	config.PenaltyLevel = 2

	level := config.GetPenaltyLevel()

	if level != 2 {
		t.Errorf("GetPenaltyLevel() = %v, want 2", level)
	}
}

func TestEarlyQuitPlayerState_ConcurrentAccess(t *testing.T) {
	config := NewEarlyQuitPlayerState()
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

func TestEarlyQuitPlayerState_ConcurrentUpdateTier(t *testing.T) {
	config := NewEarlyQuitPlayerState()
	config.PenaltyLevel = 1
	var wg sync.WaitGroup

	// Simulate concurrent tier updates
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			config.UpdateTier(ptrInt32(0))
		}()
	}

	wg.Wait()

	// Verify tier is consistent
	tier := config.GetTier()
	if tier != MatchmakingTier2 {
		t.Errorf("GetTier() = %v, want %v after concurrent updates", tier, MatchmakingTier2)
	}
}

func TestEarlyQuitPlayerState_TierTransitionFlow(t *testing.T) {
	config := NewEarlyQuitPlayerState()

	// Start in Tier 1
	_, tier, _ := config.UpdateTier(ptrInt32(0))
	if tier != MatchmakingTier1 {
		t.Errorf("Initial tier = %v, want %v", tier, MatchmakingTier1)
	}

	// Early quit moves to Tier 2 (caller resolves penalty level from config)
	config.IncrementEarlyQuit()
	config.PenaltyLevel = 1 // Caller sets penalty level
	oldTier, newTier, changed := config.UpdateTier(ptrInt32(0))
	if oldTier != MatchmakingTier1 || newTier != MatchmakingTier2 || !changed {
		t.Errorf("After early quit: oldTier=%v, newTier=%v, changed=%v, want Tier1→Tier2", oldTier, newTier, changed)
	}

	// Complete match returns to Tier 1 (caller resolves penalty level from config)
	config.IncrementCompletedMatches()
	config.PenaltyLevel = 0 // Caller re-resolves penalty level
	oldTier, newTier, changed = config.UpdateTier(ptrInt32(0))
	if oldTier != MatchmakingTier2 || newTier != MatchmakingTier1 || !changed {
		t.Errorf("After completed match: oldTier=%v, newTier=%v, changed=%v, want Tier2→Tier1", oldTier, newTier, changed)
	}
}

func TestResolvePenaltyLevel(t *testing.T) {
	cfg := evr.NewDefaultSNSEarlyQuitConfig()
	// Default levels: 0(0-2,0s), 1(3-5,120s), 2(6-10,300s), 3(11-999,900s)

	tests := []struct {
		name          string
		numQuits      int32
		expectLevel   int32
		expectLockout int32
	}{
		{"zero quits", 0, 0, 0},
		{"2 quits (top of level 0)", 2, 0, 0},
		{"3 quits (level 1 min)", 3, 1, 120},
		{"5 quits (level 1 max)", 5, 1, 120},
		{"6 quits (level 2 min)", 6, 2, 300},
		{"10 quits (level 2 max)", 10, 2, 300},
		{"11 quits (level 3 min)", 11, 3, 900},
		{"999 quits (level 3 max)", 999, 3, 900},
		{"1000 quits (above all ranges)", 1000, 3, 900},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			level, lockout := ResolvePenaltyLevel(tt.numQuits, cfg)
			if level != tt.expectLevel {
				t.Errorf("ResolvePenaltyLevel(%d) level = %d, want %d", tt.numQuits, level, tt.expectLevel)
			}
			if lockout != tt.expectLockout {
				t.Errorf("ResolvePenaltyLevel(%d) lockout = %d, want %d", tt.numQuits, lockout, tt.expectLockout)
			}
		})
	}
}

func TestResolvePenaltyLevel_NilConfig(t *testing.T) {
	level, lockout := ResolvePenaltyLevel(5, nil)
	if level != 0 || lockout != 0 {
		t.Errorf("ResolvePenaltyLevel with nil config = (%d, %d), want (0, 0)", level, lockout)
	}
}

func TestResolveSteadyPlayerLevel(t *testing.T) {
	cfg := evr.NewDefaultSNSEarlyQuitConfig()
	// Default steady levels: 0(0 matches, 0.0), 1(10 matches, 0.9), 2(25 matches, 0.95)

	tests := []struct {
		name          string
		matches       int32
		quits         int32
		expectLevel   int32
	}{
		{"no matches", 0, 0, 0},
		{"5 matches no quits", 5, 0, 0},          // below 10 min_matches for level 1
		{"10 matches no quits", 10, 0, 1},         // 100% ratio >= 0.9, matches >= 10
		{"10 matches 1 quit", 10, 1, 1},           // 90% ratio >= 0.9
		{"10 matches 2 quits", 10, 2, 0},          // 80% ratio < 0.9
		{"25 matches no quits", 25, 0, 2},         // 100% ratio >= 0.95, matches >= 25
		{"25 matches 1 quit", 25, 1, 2},           // 96% ratio >= 0.95
		{"25 matches 2 quits", 25, 2, 1},          // 92% ratio < 0.95 but >= 0.9, matches >= 10
		{"100 matches 5 quits", 100, 5, 2},        // 95% ratio >= 0.95
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			level := ResolveSteadyPlayerLevel(tt.matches, tt.quits, cfg)
			if level != tt.expectLevel {
				t.Errorf("ResolveSteadyPlayerLevel(%d, %d) = %d, want %d", tt.matches, tt.quits, level, tt.expectLevel)
			}
		})
	}
}
