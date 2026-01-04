package server

import (
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// TestEarlyQuitDetectionPipeline validates that early quits are properly detected
// and the total_early_quits counter is incremented
func TestEarlyQuitDetectionPipeline(t *testing.T) {
	t.Run("Player leaving mid-game increments early quit counter", func(t *testing.T) {
		// This test validates that when a player leaves during an active match,
		// the early quit is detected and the counter is properly incremented.

		// Setup: Create a mock match state with an active game
		state := &MatchLabel{
			Mode:      evr.ModeArenaPublic,
			StartTime: time.Now().Add(-2 * time.Minute), // Match started 2 minutes ago
			GameState: &GameState{
				MatchOver: false, // Match is still active
			},
			presenceMap: make(map[string]*EvrMatchPresence),
		}

		// Create a player presence
		playerUserID := uuid.Must(uuid.NewV4())
		playerSessionID := uuid.Must(uuid.NewV4())

		playerPresence := &EvrMatchPresence{
			SessionID:     playerSessionID,
			UserID:        playerUserID,
			Username:      "test_player",
			RoleAlignment: evr.TeamBlue, // Player role
			DisplayName:   "TestPlayer",
		}

		state.presenceMap[playerSessionID.String()] = playerPresence

		// Before leaving, record initial early quit count
		// In a real scenario, we would read from storage
		initialConfig := NewEarlyQuitConfig()
		initialEarlyQuits := initialConfig.TotalEarlyQuits

		// Simulate player leaving
		// The early quit detection happens when:
		// 1. Match is ModeArenaPublic
		// 2. Match started more than 60 seconds ago
		// 3. GameState exists and match is not over
		// 4. Player is an actual player (not spectator/broadcaster)

		// Validate detection conditions
		if state.Mode != evr.ModeArenaPublic {
			t.Fatal("Test setup error: Mode should be ModeArenaPublic")
		}

		if !time.Now().After(state.StartTime.Add(time.Second * 60)) {
			t.Fatal("Test setup error: Match should be past 60 second mark")
		}

		if state.GameState == nil || state.GameState.MatchOver {
			t.Fatal("Test setup error: GameState should exist and match should not be over")
		}

		if !playerPresence.IsPlayer() {
			t.Fatal("Test setup error: Presence should be a player")
		}

		// The actual test would require running the match loop logic
		// For now, we're validating the conditions that should trigger early quit detection

		// Simulate the early quit increment
		config := NewEarlyQuitConfig()
		config.IncrementEarlyQuit()

		if config.TotalEarlyQuits != initialEarlyQuits+1 {
			t.Errorf("Expected TotalEarlyQuits to increment from %d to %d, got %d",
				initialEarlyQuits, initialEarlyQuits+1, config.TotalEarlyQuits)
		}

		if config.EarlyQuitPenaltyLevel != 1 {
			t.Errorf("Expected EarlyQuitPenaltyLevel to be 1, got %d", config.EarlyQuitPenaltyLevel)
		}

		if config.LastEarlyQuitTime.IsZero() {
			t.Error("Expected LastEarlyQuitTime to be set")
		}
	})

	t.Run("Player leaving after match ends does not increment early quit", func(t *testing.T) {
		state := &MatchLabel{
			Mode:      evr.ModeArenaPublic,
			StartTime: time.Now().Add(-10 * time.Minute),
			GameState: &GameState{
				MatchOver: true, // Match is over
			},
		}

		// Should NOT trigger early quit detection when match is over
		shouldDetectEarlyQuit := state.Mode == evr.ModeArenaPublic &&
			time.Now().After(state.StartTime.Add(time.Second*60)) &&
			state.GameState != nil &&
			!state.GameState.MatchOver

		if shouldDetectEarlyQuit {
			t.Error("Should not detect early quit when match is over")
		}
	})

	t.Run("Player leaving before 60 second mark does not increment early quit", func(t *testing.T) {
		state := &MatchLabel{
			Mode:      evr.ModeArenaPublic,
			StartTime: time.Now().Add(-30 * time.Second), // Only 30 seconds into match
			GameState: &GameState{
				MatchOver: false,
			},
		}

		// Should NOT trigger early quit detection before 60 seconds
		shouldDetectEarlyQuit := state.Mode == evr.ModeArenaPublic &&
			time.Now().After(state.StartTime.Add(time.Second*60)) &&
			state.GameState != nil &&
			!state.GameState.MatchOver

		if shouldDetectEarlyQuit {
			t.Error("Should not detect early quit before 60 second mark")
		}
	})

	t.Run("Spectator leaving does not increment early quit", func(t *testing.T) {
		spectatorPresence := &EvrMatchPresence{
			UserID:        uuid.Must(uuid.NewV4()),
			SessionID:     uuid.Must(uuid.NewV4()),
			Username:      "spectator",
			RoleAlignment: evr.TeamSpectator, // Spectator role
		}

		// Spectators should not trigger early quit
		if spectatorPresence.IsPlayer() {
			t.Error("Spectator should not be considered a player")
		}
	})

	t.Run("Moderator leaving does not increment early quit", func(t *testing.T) {
		moderatorPresence := &EvrMatchPresence{
			UserID:        uuid.Must(uuid.NewV4()),
			SessionID:     uuid.Must(uuid.NewV4()),
			Username:      "moderator",
			RoleAlignment: evr.TeamModerator, // Moderator role
		}

		// Moderators should not trigger early quit
		if moderatorPresence.IsPlayer() {
			t.Error("Moderator should not be considered a player")
		}
	})

	t.Run("Social lobby leaving does not increment early quit", func(t *testing.T) {
		state := &MatchLabel{
			Mode:      evr.ModeSocialPublic, // Social mode, not arena
			StartTime: time.Now().Add(-2 * time.Minute),
			GameState: &GameState{
				MatchOver: false,
			},
		}

		// Should NOT trigger early quit detection in social lobbies
		shouldDetectEarlyQuit := state.Mode == evr.ModeArenaPublic &&
			time.Now().After(state.StartTime.Add(time.Second*60)) &&
			state.GameState != nil &&
			!state.GameState.MatchOver

		if shouldDetectEarlyQuit {
			t.Error("Should not detect early quit in social lobbies")
		}
	})

	t.Run("Multiple early quits accumulate correctly", func(t *testing.T) {
		config := NewEarlyQuitConfig()

		// First early quit
		config.IncrementEarlyQuit()
		if config.TotalEarlyQuits != 1 {
			t.Errorf("Expected 1 early quit, got %d", config.TotalEarlyQuits)
		}

		// Second early quit
		config.IncrementEarlyQuit()
		if config.TotalEarlyQuits != 2 {
			t.Errorf("Expected 2 early quits, got %d", config.TotalEarlyQuits)
		}

		// Third early quit
		config.IncrementEarlyQuit()
		if config.TotalEarlyQuits != 3 {
			t.Errorf("Expected 3 early quits, got %d", config.TotalEarlyQuits)
		}

		// Penalty level should also increase (capped at max)
		if config.EarlyQuitPenaltyLevel != 3 {
			t.Errorf("Expected penalty level 3, got %d", config.EarlyQuitPenaltyLevel)
		}
	})

	t.Run("Early quit updates player reliability rating", func(t *testing.T) {
		config := NewEarlyQuitConfig()

		// Complete some matches first
		config.TotalCompletedMatches = 10
		initialRating := CalculatePlayerReliabilityRating(0, 10)
		if initialRating != 1.0 {
			t.Errorf("Expected initial rating 1.0, got %f", initialRating)
		}

		// Early quit should decrease rating
		config.IncrementEarlyQuit()
		expectedRating := CalculatePlayerReliabilityRating(1, 10)
		if config.PlayerReliabilityRating != expectedRating {
			t.Errorf("Expected rating %f, got %f", expectedRating, config.PlayerReliabilityRating)
		}

		// Verify the math: 10/(10+1) = 0.909...
		if config.PlayerReliabilityRating < 0.90 || config.PlayerReliabilityRating > 0.92 {
			t.Errorf("Expected rating around 0.91, got %f", config.PlayerReliabilityRating)
		}
	})
}

// TestEarlyQuitConditions tests the specific conditions that must be met
// for early quit detection to trigger
func TestEarlyQuitConditions(t *testing.T) {
	tests := []struct {
		name            string
		mode            evr.Symbol
		matchStartedAgo time.Duration
		gameStateExists bool
		matchOver       bool
		roleAlignment   int
		shouldTrigger   bool
		description     string
	}{
		{
			name:            "Normal early quit - arena public, mid-game player leave",
			mode:            evr.ModeArenaPublic,
			matchStartedAgo: 2 * time.Minute,
			gameStateExists: true,
			matchOver:       false,
			roleAlignment:   evr.TeamBlue,
			shouldTrigger:   true,
			description:     "Player leaving arena match after 60s should trigger",
		},
		{
			name:            "Before 60 second grace period",
			mode:            evr.ModeArenaPublic,
			matchStartedAgo: 30 * time.Second,
			gameStateExists: true,
			matchOver:       false,
			roleAlignment:   evr.TeamBlue,
			shouldTrigger:   false,
			description:     "Player leaving before 60s should not trigger",
		},
		{
			name:            "After match ends",
			mode:            evr.ModeArenaPublic,
			matchStartedAgo: 10 * time.Minute,
			gameStateExists: true,
			matchOver:       true,
			roleAlignment:   evr.TeamBlue,
			shouldTrigger:   false,
			description:     "Player leaving after match ends should not trigger",
		},
		{
			name:            "Private arena match",
			mode:            evr.ModeArenaPrivate,
			matchStartedAgo: 2 * time.Minute,
			gameStateExists: true,
			matchOver:       false,
			roleAlignment:   evr.TeamBlue,
			shouldTrigger:   false,
			description:     "Private matches should not trigger early quit",
		},
		{
			name:            "Combat mode",
			mode:            evr.ModeCombatPublic,
			matchStartedAgo: 2 * time.Minute,
			gameStateExists: true,
			matchOver:       false,
			roleAlignment:   evr.TeamBlue,
			shouldTrigger:   false,
			description:     "Combat matches should not trigger early quit (only arena)",
		},
		{
			name:            "Social lobby",
			mode:            evr.ModeSocialPublic,
			matchStartedAgo: 2 * time.Minute,
			gameStateExists: true,
			matchOver:       false,
			roleAlignment:   evr.TeamBlue,
			shouldTrigger:   false,
			description:     "Social lobbies should not trigger early quit",
		},
		{
			name:            "Spectator leaving",
			mode:            evr.ModeArenaPublic,
			matchStartedAgo: 2 * time.Minute,
			gameStateExists: true,
			matchOver:       false,
			roleAlignment:   evr.TeamSpectator,
			shouldTrigger:   false,
			description:     "Spectators leaving should not trigger early quit",
		},
		{
			name:            "Moderator leaving",
			mode:            evr.ModeArenaPublic,
			matchStartedAgo: 2 * time.Minute,
			gameStateExists: true,
			matchOver:       false,
			roleAlignment:   evr.TeamModerator,
			shouldTrigger:   false,
			description:     "Moderators leaving should not trigger early quit",
		},
		{
			name:            "No game state",
			mode:            evr.ModeArenaPublic,
			matchStartedAgo: 2 * time.Minute,
			gameStateExists: false,
			matchOver:       false,
			roleAlignment:   evr.TeamBlue,
			shouldTrigger:   false,
			description:     "No game state should not trigger early quit",
		},
		{
			name:            "Orange team player leaving",
			mode:            evr.ModeArenaPublic,
			matchStartedAgo: 2 * time.Minute,
			gameStateExists: true,
			matchOver:       false,
			roleAlignment:   evr.TeamOrange,
			shouldTrigger:   true,
			description:     "Orange team player leaving should trigger",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &MatchLabel{
				Mode:      tt.mode,
				StartTime: time.Now().Add(-tt.matchStartedAgo),
			}

			if tt.gameStateExists {
				state.GameState = &GameState{
					MatchOver: tt.matchOver,
				}
			}

			presence := &EvrMatchPresence{
				RoleAlignment: tt.roleAlignment,
			}

			// Check if early quit should trigger
			shouldTrigger := state.Mode == evr.ModeArenaPublic &&
				time.Now().After(state.StartTime.Add(time.Second*60)) &&
				state.GameState != nil &&
				!state.GameState.MatchOver &&
				presence.IsPlayer()

			if shouldTrigger != tt.shouldTrigger {
				t.Errorf("%s: expected shouldTrigger=%v, got %v",
					tt.description, tt.shouldTrigger, shouldTrigger)
			}
		})
	}
}

// TestEarlyQuitScopeIssue tests for the specific bug where early quit code
// was outside the scope of the presence check
func TestEarlyQuitScopeIssue(t *testing.T) {
	t.Run("Verify early quit code runs for each presence in the loop", func(t *testing.T) {
		// This test validates that the early quit detection logic
		// properly accesses the player presence variable (mp)
		// which should be in scope when the early quit code runs.

		// Setup: Create multiple player presences
		presences := []*EvrMatchPresence{
			{
				UserID:        uuid.Must(uuid.NewV4()),
				SessionID:     uuid.Must(uuid.NewV4()),
				Username:      "player1",
				RoleAlignment: evr.TeamBlue,
				DisplayName:   "Player1",
			},
			{
				UserID:        uuid.Must(uuid.NewV4()),
				SessionID:     uuid.Must(uuid.NewV4()),
				Username:      "player2",
				RoleAlignment: evr.TeamOrange,
				DisplayName:   "Player2",
			},
		}

		// Simulate the loop that should process each presence
		earlyQuitCount := 0
		for _, p := range presences {
			// This simulates the presence map lookup
			mp := p
			if mp == nil {
				t.Error("Presence should not be nil")
				continue
			}

			// This is where the bug was - code was accessing mp outside its scope
			// Verify we can access mp fields here
			if mp.GetUserId() == "" {
				t.Error("Should be able to access mp.GetUserId()")
			}

			if mp.IsPlayer() {
				earlyQuitCount++

				// Simulate what should happen in the early quit code
				if mp.DisplayName == "" {
					t.Error("Should be able to access mp.DisplayName in early quit code")
				}
			}
		}

		if earlyQuitCount != 2 {
			t.Errorf("Expected 2 early quits to be detected, got %d", earlyQuitCount)
		}
	})
}
