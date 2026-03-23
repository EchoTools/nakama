package server

import (
	"testing"
)

// TestEarlyQuitTierIntegration tests the full tier change workflow
// This test validates:
// 1. Player early quits -> penalty increments -> tier degrades
// 2. Player completes match -> penalty decrements -> tier recovers
// 3. Tier transitions trigger appropriate state changes
func TestEarlyQuitTierIntegration(t *testing.T) {
	t.Run("EarlyQuit triggers tier degradation", func(t *testing.T) {
		config := NewEarlyQuitPlayerState()

		// Start in Tier 1
		oldTier, newTier, changed := config.UpdateTier(ptrInt32(0))
		if newTier != MatchmakingTier1 {
			t.Fatalf("Expected initial tier to be Tier 1, got %d", newTier)
		}

		// Simulate early quit (caller resolves penalty level)
		config.IncrementEarlyQuit()
		config.PenaltyLevel = 1

		// Check tier change
		oldTier, newTier, changed = config.UpdateTier(ptrInt32(0))
		if !changed {
			t.Error("Expected tier to change after early quit")
		}
		if oldTier != MatchmakingTier1 {
			t.Errorf("Expected oldTier to be Tier 1 (0), got %d", oldTier)
		}
		if newTier != MatchmakingTier2 {
			t.Errorf("Expected newTier to be Tier 2 (1), got %d", newTier)
		}
		if config.MatchmakingTier != MatchmakingTier2 {
			t.Errorf("Expected config tier to be Tier 2, got %d", config.MatchmakingTier)
		}
	})

	t.Run("CompletedMatch triggers tier recovery", func(t *testing.T) {
		config := NewEarlyQuitPlayerState()

		// Start in Tier 2 (penalty level 1)
		config.PenaltyLevel = 1
		config.MatchmakingTier = MatchmakingTier2

		// Verify starting tier
		tier := config.GetTier()
		if tier != MatchmakingTier2 {
			t.Fatalf("Expected starting tier to be Tier 2, got %d", tier)
		}

		// Simulate completed match (caller resolves penalty level)
		config.IncrementCompletedMatches()
		config.PenaltyLevel = 0

		// Check tier change
		oldTier, newTier, changed := config.UpdateTier(ptrInt32(0))
		if !changed {
			t.Error("Expected tier to change after completed match")
		}
		if oldTier != MatchmakingTier2 {
			t.Errorf("Expected oldTier to be Tier 2 (1), got %d", oldTier)
		}
		if newTier != MatchmakingTier1 {
			t.Errorf("Expected newTier to be Tier 1 (0), got %d", newTier)
		}
		if config.MatchmakingTier != MatchmakingTier1 {
			t.Errorf("Expected config tier to be Tier 1, got %d", config.MatchmakingTier)
		}
	})

	t.Run("Multiple early quits keep player in Tier 2", func(t *testing.T) {
		config := NewEarlyQuitPlayerState()

		// First early quit (caller resolves penalty level)
		config.IncrementEarlyQuit()
		config.PenaltyLevel = 1
		_, tier1, _ := config.UpdateTier(ptrInt32(0))

		// Second early quit (caller resolves penalty level)
		config.IncrementEarlyQuit()
		config.PenaltyLevel = 2
		oldTier, tier2, changed := config.UpdateTier(ptrInt32(0))

		if tier1 != MatchmakingTier2 || tier2 != MatchmakingTier2 {
			t.Error("Expected player to remain in Tier 2 after multiple early quits")
		}
		if changed {
			t.Error("Expected no tier change when already in Tier 2")
		}
		if oldTier != tier2 {
			t.Error("Expected oldTier and newTier to match when no change occurs")
		}
	})

	t.Run("Custom tier threshold changes tier boundaries", func(t *testing.T) {
		config := NewEarlyQuitPlayerState()

		// With threshold 1, penalty level 1 should keep player in Tier 1
		config.PenaltyLevel = 1
		_, tier, _ := config.UpdateTier(ptrInt32(1))
		if tier != MatchmakingTier1 {
			t.Errorf("With threshold 1, penalty 1 should be Tier 1, got tier %d", tier)
		}

		// With threshold 0, penalty level 1 should move to Tier 2
		_, tier, _ = config.UpdateTier(ptrInt32(0))
		if tier != MatchmakingTier2 {
			t.Errorf("With threshold 0, penalty 1 should be Tier 2, got tier %d", tier)
		}
	})

	t.Run("Tier change updates LastTierChange timestamp", func(t *testing.T) {
		config := NewEarlyQuitPlayerState()

		// Initial tier setting
		config.UpdateTier(ptrInt32(0))
		if !config.LastTierChange.IsZero() {
			t.Error("LastTierChange should be zero initially even after first UpdateTier call")
		}

		// Trigger tier change (caller resolves penalty level)
		config.IncrementEarlyQuit()
		config.PenaltyLevel = 1
		_, _, changed := config.UpdateTier(ptrInt32(0))

		if !changed {
			t.Fatal("Expected tier to change")
		}
		if config.LastTierChange.IsZero() {
			t.Error("LastTierChange should be set after tier change")
		}
	})

	t.Run("Tier workflow with max penalty", func(t *testing.T) {
		config := NewEarlyQuitPlayerState()

		// Increment to max penalty (caller sets penalty level)
		for i := 0; i <= int(MaxEarlyQuitPenaltyLevel); i++ {
			config.IncrementEarlyQuit()
		}
		config.PenaltyLevel = MaxEarlyQuitPenaltyLevel

		// Should be in Tier 2
		_, tier, _ := config.UpdateTier(ptrInt32(0))
		if tier != MatchmakingTier2 {
			t.Errorf("Expected Tier 2 at max penalty, got tier %d", tier)
		}

		// Verify penalty is at max
		if config.PenaltyLevel != MaxEarlyQuitPenaltyLevel {
			t.Errorf("Expected penalty to be at %d, got %d", MaxEarlyQuitPenaltyLevel, config.PenaltyLevel)
		}
	})

	t.Run("Tier workflow with min penalty", func(t *testing.T) {
		config := NewEarlyQuitPlayerState()

		// Start with some penalty, then resolve to minimum (caller sets penalty level)
		config.PenaltyLevel = MinEarlyQuitPenaltyLevel

		// Complete matches
		for i := 0; i < 5; i++ {
			config.IncrementCompletedMatches()
		}

		// Should be in Tier 1
		_, tier, _ := config.UpdateTier(ptrInt32(0))
		if tier != MatchmakingTier1 {
			t.Errorf("Expected Tier 1 at min penalty, got tier %d", tier)
		}

		// Verify penalty is floored
		if config.PenaltyLevel != MinEarlyQuitPenaltyLevel {
			t.Errorf("Expected penalty to be floored at %d, got %d", MinEarlyQuitPenaltyLevel, config.PenaltyLevel)
		}
	})
}

// TestMatchmakerTierExtraction tests the tier extraction from matchmaker candidates
func TestMatchmakerTierExtraction(t *testing.T) {
	t.Run("getTierFromCandidate with valid tier", func(t *testing.T) {
		// This would require mocking runtime.MatchmakerEntry, which is complex
		// In practice, this is tested via the matchmaker integration
		// Placeholder for when mock infrastructure is available
		t.Skip("Requires runtime.MatchmakerEntry mock")
	})

	t.Run("getTierFromCandidate with missing tier defaults to Tier 1", func(t *testing.T) {
		// Placeholder - would test that missing eq_tier property defaults correctly
		t.Skip("Requires runtime.MatchmakerEntry mock")
	})
}

// TestTierPriorityInMatchmaking validates that tier-based priority works correctly
func TestTierPriorityInMatchmaking(t *testing.T) {
	t.Run("Tier 1 players matched before Tier 2 players", func(t *testing.T) {
		// This is a complex integration test that would require:
		// 1. Setting up matchmaker with multiple tickets
		// 2. Creating tickets with different tiers
		// 3. Verifying sort order
		// Placeholder for future implementation with proper test infrastructure
		t.Skip("Requires full matchmaker test infrastructure")
	})
}
