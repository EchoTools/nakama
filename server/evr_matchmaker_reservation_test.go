package server

import (
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

// Test starving ticket identification based on wait time
func TestBuildReservations_StarvingIdentification(t *testing.T) {
	m := NewSkillBasedMatchmaker()
	now := time.Now().UTC()
	nowUnix := now.Unix()

	tests := []struct {
		name              string
		waitTimeSecs      int64
		reservationThresh int
		expectStarving    bool
	}{
		{
			name:              "Below threshold - not starving",
			waitTimeSecs:      60,
			reservationThresh: 90,
			expectStarving:    false,
		},
		{
			name:              "At threshold - starving",
			waitTimeSecs:      90,
			reservationThresh: 90,
			expectStarving:    true,
		},
		{
			name:              "Above threshold - starving",
			waitTimeSecs:      120,
			reservationThresh: 90,
			expectStarving:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear starving state
			m.starvingTickets = make(map[string]*StarvingTicket)

			submissionTime := nowUnix - tt.waitTimeSecs
			candidates := [][]runtime.MatchmakerEntry{
				{
					&MatchmakerEntry{
						Ticket: "ticket1",
						Presence: &MatchmakerPresence{
							UserId:    "user1",
							SessionId: "session1",
							Username:  "user1",
							Node:      "node1",
						},
						Properties: map[string]any{
							"submission_time": float64(submissionTime),
						},
					},
				},
			}

			settings := &GlobalMatchmakingSettings{
				ReservationThresholdSecs: tt.reservationThresh,
				MaxReservationRatio:      0.4,
				ReservationSafetyValveSecs: 300,
			}

			starving, _ := m.buildReservations(candidates, []PredictedMatch{}, settings)

			if tt.expectStarving {
				if len(starving) == 0 {
					t.Errorf("Expected starving ticket, but got none")
				}
				if _, ok := starving["session1"]; !ok {
					t.Errorf("Expected session1 to be starving")
				}
			} else {
				if len(starving) > 0 {
					t.Errorf("Expected no starving tickets, but got %d", len(starving))
				}
			}
		})
	}
}

// Test safety valve expiration
func TestBuildReservations_SafetyValve(t *testing.T) {
	m := NewSkillBasedMatchmaker()
	now := time.Now().UTC()
	nowUnix := now.Unix()

	// Create a ticket that has been starving for 10 minutes (past safety valve)
	submissionTime := nowUnix - 600 // 10 minutes ago
	candidates := [][]runtime.MatchmakerEntry{
		{
			&MatchmakerEntry{
				Ticket: "ticket1",
				Presence: &MatchmakerPresence{
					UserId:    "user1",
					SessionId: "session1",
					Username:  "user1",
					Node:      "node1",
				},
				Properties: map[string]any{
					"submission_time": float64(submissionTime),
				},
			},
		},
	}

	settings := &GlobalMatchmakingSettings{
		ReservationThresholdSecs:   90,  // 1.5 minutes
		MaxReservationRatio:        0.4,
		ReservationSafetyValveSecs: 300, // 5 minutes
	}

	// First call - ticket becomes starving
	m.starvingTickets["ticket1"] = &StarvingTicket{
		Ticket:         "ticket1",
		FirstStarvedAt: now.Add(-6 * time.Minute), // Started starving 6 minutes ago
	}

	starving, _ := m.buildReservations(candidates, []PredictedMatch{}, settings)

	// Safety valve should have fired - no longer starving
	if len(starving) > 0 {
		t.Errorf("Expected safety valve to fire and clear starving tickets, but got %d starving", len(starving))
	}

	if _, exists := m.starvingTickets["ticket1"]; exists {
		t.Errorf("Expected starving ticket to be removed after safety valve")
	}
}

// Test max reservation ratio capping
func TestBuildReservations_MaxRatioCapping(t *testing.T) {
	m := NewSkillBasedMatchmaker()
	now := time.Now().UTC()
	nowUnix := now.Unix()

	// Create 10 total players (1 starving + 9 others)
	submissionTime := nowUnix - 120 // 2 minutes ago (starving)
	candidates := [][]runtime.MatchmakerEntry{}

	// One starving player
	starvingCandidate := []runtime.MatchmakerEntry{
		&MatchmakerEntry{
			Ticket: "ticket_starving",
			Presence: &MatchmakerPresence{
				UserId:    "user_starving",
				SessionId: "session_starving",
				Username:  "user_starving",
				Node:      "node1",
			},
			Properties: map[string]any{
				"submission_time": float64(submissionTime),
			},
		},
	}
	candidates = append(candidates, starvingCandidate)

	// 9 other players (to be potentially reserved)
	for i := 0; i < 9; i++ {
		candidates = append(candidates, []runtime.MatchmakerEntry{
			&MatchmakerEntry{
				Ticket: "ticket_" + string(rune('0'+i)),
				Presence: &MatchmakerPresence{
					UserId:    "user_" + string(rune('0'+i)),
					SessionId: "session_" + string(rune('0'+i)),
					Username:  "user_" + string(rune('0'+i)),
					Node:      "node1",
				},
				Properties: map[string]any{
					"submission_time": float64(nowUnix - 30), // Fresh tickets
				},
			},
		})
	}

	// Create a prediction where the starving player matches with all 9 others
	allPlayers := []runtime.MatchmakerEntry{}
	for _, c := range candidates {
		allPlayers = append(allPlayers, c...)
	}
	predictions := []PredictedMatch{
		{
			Candidate: allPlayers,
		},
	}

	settings := &GlobalMatchmakingSettings{
		ReservationThresholdSecs:   90,
		MaxReservationRatio:        0.4, // Only 40% of 10 = 4 players can be reserved
		ReservationSafetyValveSecs: 300,
	}

	_, reserved := m.buildReservations(candidates, predictions, settings)

	// Should only reserve 4 players (40% of 10 total players)
	if len(reserved) > 4 {
		t.Errorf("Expected max 4 reserved players (40%% of 10), but got %d", len(reserved))
	}
}

// Test hard reservation blocking in Pass 2
func TestAssembleMatchesWithReservations_HardBlocking(t *testing.T) {
	m := NewSkillBasedMatchmaker()

	// Create 8 players total
	players := make([]*MatchmakerEntry, 8)
	for i := 0; i < 8; i++ {
		players[i] = &MatchmakerEntry{
			Ticket: "ticket_" + string(rune('0'+i)),
			Presence: &MatchmakerPresence{
				UserId:    "user_" + string(rune('0'+i)),
				SessionId: "session_" + string(rune('0'+i)),
				Username:  "user_" + string(rune('0'+i)),
				Node:      "node1",
			},
			Properties: map[string]any{},
		}
	}

	// Prediction 1: Players 0-3 (contains starving player 0)
	pred1 := PredictedMatch{
		Candidate: []runtime.MatchmakerEntry{
			players[0], players[1], players[2], players[3],
		},
	}

	// Prediction 2: Players 4-7 (no starving players)
	pred2 := PredictedMatch{
		Candidate: []runtime.MatchmakerEntry{
			players[4], players[5], players[6], players[7],
		},
	}

	// Prediction 3: Players 1, 4, 5, 6 (contains reserved player 1)
	pred3 := PredictedMatch{
		Candidate: []runtime.MatchmakerEntry{
			players[1], players[4], players[5], players[6],
		},
	}

	predictions := []PredictedMatch{pred1, pred2, pred3}

	// Mark player 0 as starving and players 1,2,3 as reserved
	starvingSessionIDs := map[string]struct{}{
		"session_0": {},
	}
	reservedSessionIDs := map[string]struct{}{
		"session_1": {},
		"session_2": {},
		"session_3": {},
	}

	matches := m.assembleMatchesWithReservations(predictions, starvingSessionIDs, reservedSessionIDs)

	// Should get 2 matches:
	// - Match 1: pred1 (contains starving player, gets priority in Pass 1)
	// - Match 2: pred2 (no reserved players, allowed in Pass 2)
	// pred3 should be blocked because it tries to consume reserved player 1

	if len(matches) != 2 {
		t.Errorf("Expected 2 matches, got %d", len(matches))
	}

	// Verify pred3 was NOT included
	for _, match := range matches {
		if len(match) == 4 {
			hasPlayer1 := false
			hasPlayer4 := false
			for _, p := range match {
				if p.GetPresence().GetSessionId() == "session_1" {
					hasPlayer1 = true
				}
				if p.GetPresence().GetSessionId() == "session_4" {
					hasPlayer4 = true
				}
			}
			// If it has both player 1 and player 4, it's pred3 which should be blocked
			if hasPlayer1 && hasPlayer4 {
				t.Errorf("pred3 should have been blocked due to reserved player")
			}
		}
	}
}

// Test cleanup of matched starving tickets
func TestAssembleMatchesWithReservations_CleanupMatched(t *testing.T) {
	m := NewSkillBasedMatchmaker()

	// Add starving tickets
	m.starvingTickets["ticket1"] = &StarvingTicket{
		Ticket:         "ticket1",
		FirstStarvedAt: time.Now().Add(-2 * time.Minute),
	}
	m.starvingTickets["ticket2"] = &StarvingTicket{
		Ticket:         "ticket2",
		FirstStarvedAt: time.Now().Add(-2 * time.Minute),
	}

	// Create players
	players := []*MatchmakerEntry{
		{
			Ticket: "ticket1",
			Presence: &MatchmakerPresence{
				UserId:    "user1",
				SessionId: "session1",
				Username:  "user1",
				Node:      "node1",
			},
			Properties: map[string]any{},
		},
		{
			Ticket: "ticket2",
			Presence: &MatchmakerPresence{
				UserId:    "user2",
				SessionId: "session2",
				Username:  "user2",
				Node:      "node1",
			},
			Properties: map[string]any{},
		},
	}

	pred := PredictedMatch{
		Candidate: []runtime.MatchmakerEntry{players[0], players[1]},
	}

	starvingSessionIDs := map[string]struct{}{
		"session1": {},
		"session2": {},
	}
	reservedSessionIDs := map[string]struct{}{}

	m.assembleMatchesWithReservations([]PredictedMatch{pred}, starvingSessionIDs, reservedSessionIDs)

	// Both starving tickets should be removed after matching
	if len(m.starvingTickets) != 0 {
		t.Errorf("Expected starving tickets to be cleaned up after matching, but got %d remaining", len(m.starvingTickets))
	}
}

// Test feature flag - when disabled, should use normal path
func TestProcessPotentialMatches_FeatureFlagDisabled(t *testing.T) {
	m := NewSkillBasedMatchmaker()

	// Create test candidates
	candidates := [][]runtime.MatchmakerEntry{
		{
			&MatchmakerEntry{
				Ticket: "ticket1",
				Presence: &MatchmakerPresence{
					UserId:    "user1",
					SessionId: "session1",
					Username:  "user1",
					Node:      "node1",
				},
				Properties: map[string]any{
					"submission_time": float64(time.Now().Unix() - 120),
					"max_rtt":         200.0,
				},
			},
			&MatchmakerEntry{
				Ticket: "ticket2",
				Presence: &MatchmakerPresence{
					UserId:    "user2",
					SessionId: "session2",
					Username:  "user2",
					Node:      "node1",
				},
				Properties: map[string]any{
					"submission_time": float64(time.Now().Unix() - 30),
					"max_rtt":         200.0,
				},
			},
		},
	}

	// When feature is disabled (default), should not populate starving/reserved counts
	_, _, filterCounts, _ := m.processPotentialMatches(candidates)

	// Should not have reservation-related filter counts
	if _, exists := filterCounts["starving_tickets"]; exists {
		t.Errorf("Feature disabled, but found starving_tickets in filter counts")
	}
	if _, exists := filterCounts["reserved_players"]; exists {
		t.Errorf("Feature disabled, but found reserved_players in filter counts")
	}
}

// Test helper functions
func TestHelperFunctions(t *testing.T) {
	players := []*MatchmakerEntry{
		{
			Presence: &MatchmakerPresence{SessionId: "session1"},
		},
		{
			Presence: &MatchmakerPresence{SessionId: "session2"},
		},
		{
			Presence: &MatchmakerPresence{SessionId: "session3"},
		},
	}

	candidate := []runtime.MatchmakerEntry{players[0], players[1], players[2]}

	t.Run("containsStarvingPlayer", func(t *testing.T) {
		starving := map[string]struct{}{
			"session2": {},
		}
		if !containsStarvingPlayer(candidate, starving) {
			t.Errorf("Expected to find starving player")
		}

		emptyStarving := map[string]struct{}{}
		if containsStarvingPlayer(candidate, emptyStarving) {
			t.Errorf("Expected no starving player")
		}
	})

	t.Run("consumesReservedPlayer", func(t *testing.T) {
		reserved := map[string]struct{}{
			"session2": {},
		}
		matched := map[string]struct{}{}

		if !consumesReservedPlayer(candidate, reserved, matched) {
			t.Errorf("Expected to consume reserved player")
		}

		// If player is already matched, shouldn't count as consuming
		matched["session2"] = struct{}{}
		if consumesReservedPlayer(candidate, reserved, matched) {
			t.Errorf("Should not consume already-matched player")
		}
	})

	t.Run("hasMatchedPlayer", func(t *testing.T) {
		matched := map[string]struct{}{
			"session1": {},
		}
		if !hasMatchedPlayer(candidate, matched) {
			t.Errorf("Expected to find matched player")
		}

		emptyMatched := map[string]struct{}{}
		if hasMatchedPlayer(candidate, emptyMatched) {
			t.Errorf("Expected no matched player")
		}
	})

	t.Run("markMatched", func(t *testing.T) {
		matched := map[string]struct{}{}
		markMatched(candidate, matched)

		if len(matched) != 3 {
			t.Errorf("Expected 3 players marked, got %d", len(matched))
		}
		for _, p := range players {
			if _, ok := matched[p.Presence.SessionId]; !ok {
				t.Errorf("Expected %s to be marked", p.Presence.SessionId)
			}
		}
	})
}
