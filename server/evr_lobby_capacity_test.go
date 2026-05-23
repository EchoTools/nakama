package server

import (
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// TestLobbyCapacity_ReservationsInflateSize demonstrates that rebuildCache() counts
// reservations in Size, causing inaccurate capacity reporting.
// Bug: A lobby with 6 actual players and 6 reservations reports Size=12 and OpenSlots()=0,
// making the lobby appear full even though there are no actual players occupying those slots.
func TestLobbyCapacity_ReservationsInflateSize(t *testing.T) {
	state := newSocialTestMatchLabel()
	state.Mode = evr.ModeSocialPublic
	state.MaxSize = SocialLobbyMaxSize
	state.PlayerLimit = SocialLobbyMaxSize

	// Add 6 actual players to the lobby
	for i := 0; i < 6; i++ {
		userID := uuid.Must(uuid.NewV4())
		sessionID := uuid.Must(uuid.NewV4())
		presence := &EvrMatchPresence{
			UserID:        userID,
			SessionID:     sessionID,
			RoleAlignment: evr.TeamSocial,
			Username:      "Player" + string(rune('A'+i)),
			DisplayName:   "Player" + string(rune('A'+i)),
			EvrID:         evr.EvrId{PlatformCode: evr.PlatformCode(i + 1), AccountId: uint64(i + 1)},
		}
		state.presenceMap[sessionID.String()] = presence
		state.presenceByEvrID[presence.EvrID] = presence
		state.joinTimestamps[sessionID.String()] = time.Now()
	}

	// Add 6 reservations (simulating party members who will join later)
	for i := 0; i < 6; i++ {
		userID := uuid.Must(uuid.NewV4())
		sessionID := uuid.Must(uuid.NewV4())
		reservation := &slotReservation{
			Presence: &EvrMatchPresence{
				UserID:        userID,
				SessionID:     sessionID,
				RoleAlignment: evr.TeamSocial,
				Username:      "Reserved" + string(rune('A'+i)),
				DisplayName:   "Reserved" + string(rune('A'+i)),
				EvrID:         evr.EvrId{PlatformCode: evr.PlatformCode(10 + i), AccountId: uint64(10 + i)},
			},
			Expiry: time.Now().Add(5 * time.Minute),
		}
		state.reservationMap[sessionID.String()] = reservation
		state.joinTimestamps[sessionID.String()] = time.Now()
	}

	state.rebuildCache()

	// Bug: Size includes both actual players (6) and reservations (6) = 12
	if state.Size != 12 {
		t.Errorf("Expected Size=12 (6 actual + 6 reservations), got %d", state.Size)
	}

	// Bug: OpenSlots appears to be 0 (12 - 12 = 0), making the lobby appear full
	if state.OpenSlots() != 0 {
		t.Errorf("Expected OpenSlots()=0 (capacity full), got %d", state.OpenSlots())
	}

	// Verify we have the expected number of actual players
	actualPlayerCount := len(state.presenceMap)
	if actualPlayerCount != 6 {
		t.Errorf("Expected 6 actual players in presenceMap, got %d", actualPlayerCount)
	}

	// Verify we have the expected number of reservations
	reservationCount := len(state.reservationMap)
	if reservationCount != 6 {
		t.Errorf("Expected 6 reservations in reservationMap, got %d", reservationCount)
	}
}

// TestLobbyCapacity_ExpiredReservationsCleanedUp demonstrates that expired
// reservations are cleaned up and stop counting toward Size.
func TestLobbyCapacity_ExpiredReservationsCleanedUp(t *testing.T) {
	state := newSocialTestMatchLabel()
	state.Mode = evr.ModeSocialPublic
	state.MaxSize = SocialLobbyMaxSize
	state.PlayerLimit = SocialLobbyMaxSize

	// Add 6 actual players
	for i := 0; i < 6; i++ {
		userID := uuid.Must(uuid.NewV4())
		sessionID := uuid.Must(uuid.NewV4())
		presence := &EvrMatchPresence{
			UserID:        userID,
			SessionID:     sessionID,
			RoleAlignment: evr.TeamSocial,
			Username:      "Player" + string(rune('A'+i)),
			DisplayName:   "Player" + string(rune('A'+i)),
			EvrID:         evr.EvrId{PlatformCode: evr.PlatformCode(i + 1), AccountId: uint64(i + 1)},
		}
		state.presenceMap[sessionID.String()] = presence
		state.presenceByEvrID[presence.EvrID] = presence
		state.joinTimestamps[sessionID.String()] = time.Now()
	}

	// Add 6 reservations with expiry in the past (expired)
	for i := 0; i < 6; i++ {
		userID := uuid.Must(uuid.NewV4())
		sessionID := uuid.Must(uuid.NewV4())
		reservation := &slotReservation{
			Presence: &EvrMatchPresence{
				UserID:        userID,
				SessionID:     sessionID,
				RoleAlignment: evr.TeamSocial,
				Username:      "Reserved" + string(rune('A'+i)),
				DisplayName:   "Reserved" + string(rune('A'+i)),
				EvrID:         evr.EvrId{PlatformCode: evr.PlatformCode(10 + i), AccountId: uint64(10 + i)},
			},
			// Expired 1 minute ago
			Expiry: time.Now().Add(-1 * time.Minute),
		}
		state.reservationMap[sessionID.String()] = reservation
		state.joinTimestamps[sessionID.String()] = time.Now()
	}

	// Before rebuild, we have 6 expired reservations
	if len(state.reservationMap) != 6 {
		t.Errorf("Before rebuild: expected 6 reservations, got %d", len(state.reservationMap))
	}

	state.rebuildCache()

	// After rebuild, expired reservations should be deleted
	if len(state.reservationMap) != 0 {
		t.Errorf("After rebuild: expected 0 reservations (all expired), got %d", len(state.reservationMap))
	}

	// Size should only count actual players (6)
	if state.Size != 6 {
		t.Errorf("Expected Size=6 (only actual players), got %d", state.Size)
	}

	// OpenSlots should reflect actual capacity
	expectedOpenSlots := SocialLobbyMaxSize - 6
	if state.OpenSlots() != expectedOpenSlots {
		t.Errorf("Expected OpenSlots()=%d, got %d", expectedOpenSlots, state.OpenSlots())
	}
}

// TestLobbyCapacity_MixedExpiringReservations demonstrates that a lobby with
// a mix of actual players and expired reservations correctly reports capacity.
// Bug scenario: 8 actual players + 4 expired reservations should report Size=8, OpenSlots()=4,
// but the bug would count the expired reservations until they're explicitly checked.
func TestLobbyCapacity_MixedExpiringReservations(t *testing.T) {
	state := newSocialTestMatchLabel()
	state.Mode = evr.ModeSocialPublic
	state.MaxSize = SocialLobbyMaxSize
	state.PlayerLimit = SocialLobbyMaxSize

	// Add 8 actual players
	for i := 0; i < 8; i++ {
		userID := uuid.Must(uuid.NewV4())
		sessionID := uuid.Must(uuid.NewV4())
		presence := &EvrMatchPresence{
			UserID:        userID,
			SessionID:     sessionID,
			RoleAlignment: evr.TeamSocial,
			Username:      "Player" + string(rune('A'+i)),
			DisplayName:   "Player" + string(rune('A'+i)),
			EvrID:         evr.EvrId{PlatformCode: evr.PlatformCode(i + 1), AccountId: uint64(i + 1)},
		}
		state.presenceMap[sessionID.String()] = presence
		state.presenceByEvrID[presence.EvrID] = presence
		state.joinTimestamps[sessionID.String()] = time.Now()
	}

	// Add 4 reservations that have expired
	for i := 0; i < 4; i++ {
		userID := uuid.Must(uuid.NewV4())
		sessionID := uuid.Must(uuid.NewV4())
		reservation := &slotReservation{
			Presence: &EvrMatchPresence{
				UserID:        userID,
				SessionID:     sessionID,
				RoleAlignment: evr.TeamSocial,
				Username:      "ExpiredReserved" + string(rune('A'+i)),
				DisplayName:   "ExpiredReserved" + string(rune('A'+i)),
				EvrID:         evr.EvrId{PlatformCode: evr.PlatformCode(20 + i), AccountId: uint64(20 + i)},
			},
			// Expired 10 seconds ago
			Expiry: time.Now().Add(-10 * time.Second),
		}
		state.reservationMap[sessionID.String()] = reservation
	}

	state.rebuildCache()

	// All expired reservations should be cleaned up
	if len(state.reservationMap) != 0 {
		t.Errorf("Expected 0 reservations after cleanup, got %d", len(state.reservationMap))
	}

	// Size should only count actual players (8)
	if state.Size != 8 {
		t.Errorf("Expected Size=8, got %d", state.Size)
	}

	// OpenSlots should be 4 (12 - 8)
	expectedOpenSlots := 4
	if state.OpenSlots() != expectedOpenSlots {
		t.Errorf("Expected OpenSlots()=%d, got %d", expectedOpenSlots, state.OpenSlots())
	}
}

// TestLobbyCapacity_ReservationConsumedDoesNotChangeSize demonstrates that
// when a reserved player actually joins (consuming the reservation), Size doesn't
// change because the reservation slot is converted to a presence slot.
func TestLobbyCapacity_ReservationConsumedDoesNotChangeSize(t *testing.T) {
	state := newSocialTestMatchLabel()
	state.Mode = evr.ModeSocialPublic
	state.MaxSize = SocialLobbyMaxSize
	state.PlayerLimit = SocialLobbyMaxSize

	// Add 6 actual players
	for i := 0; i < 6; i++ {
		userID := uuid.Must(uuid.NewV4())
		sessionID := uuid.Must(uuid.NewV4())
		presence := &EvrMatchPresence{
			UserID:        userID,
			SessionID:     sessionID,
			RoleAlignment: evr.TeamSocial,
			Username:      "Player" + string(rune('A'+i)),
			DisplayName:   "Player" + string(rune('A'+i)),
			EvrID:         evr.EvrId{PlatformCode: evr.PlatformCode(i + 1), AccountId: uint64(i + 1)},
		}
		state.presenceMap[sessionID.String()] = presence
		state.presenceByEvrID[presence.EvrID] = presence
		state.joinTimestamps[sessionID.String()] = time.Now()
	}

	// Create a reservation for a party member
	reservedUserID := uuid.Must(uuid.NewV4())
	reservedSessionID := uuid.Must(uuid.NewV4())
	reservation := &slotReservation{
		Presence: &EvrMatchPresence{
			UserID:        reservedUserID,
			SessionID:     reservedSessionID,
			RoleAlignment: evr.TeamSocial,
			Username:      "ReservedPartyMember",
			DisplayName:   "ReservedPartyMember",
			EvrID:         evr.EvrId{PlatformCode: 100, AccountId: 100},
		},
		Expiry: time.Now().Add(5 * time.Minute),
	}
	state.reservationMap[reservedSessionID.String()] = reservation

	state.rebuildCache()

	// Size should be 7 (6 actual + 1 reservation)
	if state.Size != 7 {
		t.Errorf("Before join: expected Size=7, got %d", state.Size)
	}

	// Now the reserved player actually joins
	// This happens when LoadAndDeleteReservation is called and the reservation is converted
	// The reservation is deleted from reservationMap
	delete(state.reservationMap, reservedSessionID.String())

	// The presence is added directly (in normal join flow)
	state.presenceMap[reservedSessionID.String()] = reservation.Presence
	state.presenceByEvrID[reservation.Presence.EvrID] = reservation.Presence
	state.joinTimestamps[reservedSessionID.String()] = time.Now()

	state.rebuildCache()

	// Size should still be 7 (6 original + 1 that was reserved, now actually present)
	// The Size doesn't increase because the reservation slot becomes an actual presence slot
	if state.Size != 7 {
		t.Errorf("After join: expected Size=7 (no change), got %d", state.Size)
	}

	// OpenSlots should be 5 (12 - 7)
	expectedOpenSlots := 5
	if state.OpenSlots() != expectedOpenSlots {
		t.Errorf("After join: expected OpenSlots()=%d, got %d", expectedOpenSlots, state.OpenSlots())
	}
}

// TestLobbyCapacity_ValidReservationsNotCleaned demonstrates that valid
// (non-expired) reservations are preserved during rebuildCache().
func TestLobbyCapacity_ValidReservationsNotCleaned(t *testing.T) {
	state := newSocialTestMatchLabel()
	state.Mode = evr.ModeSocialPublic
	state.MaxSize = SocialLobbyMaxSize
	state.PlayerLimit = SocialLobbyMaxSize

	// Add 5 actual players
	for i := 0; i < 5; i++ {
		userID := uuid.Must(uuid.NewV4())
		sessionID := uuid.Must(uuid.NewV4())
		presence := &EvrMatchPresence{
			UserID:        userID,
			SessionID:     sessionID,
			RoleAlignment: evr.TeamSocial,
			Username:      "Player" + string(rune('A'+i)),
			DisplayName:   "Player" + string(rune('A'+i)),
			EvrID:         evr.EvrId{PlatformCode: evr.PlatformCode(i + 1), AccountId: uint64(i + 1)},
		}
		state.presenceMap[sessionID.String()] = presence
		state.presenceByEvrID[presence.EvrID] = presence
		state.joinTimestamps[sessionID.String()] = time.Now()
	}

	// Add 2 valid reservations (5 minute expiry for social lobbies)
	reservationSessionIDs := make([]string, 0)
	for i := 0; i < 2; i++ {
		userID := uuid.Must(uuid.NewV4())
		sessionID := uuid.Must(uuid.NewV4())
		reservation := &slotReservation{
			Presence: &EvrMatchPresence{
				UserID:        userID,
				SessionID:     sessionID,
				RoleAlignment: evr.TeamSocial,
				Username:      "Reserved" + string(rune('A'+i)),
				DisplayName:   "Reserved" + string(rune('A'+i)),
				EvrID:         evr.EvrId{PlatformCode: evr.PlatformCode(10 + i), AccountId: uint64(10 + i)},
			},
			// Valid for 5 minutes
			Expiry: time.Now().Add(5 * time.Minute),
		}
		state.reservationMap[sessionID.String()] = reservation
		reservationSessionIDs = append(reservationSessionIDs, sessionID.String())
		state.joinTimestamps[sessionID.String()] = time.Now()
	}

	state.rebuildCache()

	// Size should be 7 (5 actual + 2 valid reservations)
	if state.Size != 7 {
		t.Errorf("Expected Size=7 (5 actual + 2 valid reservations), got %d", state.Size)
	}

	// All 2 reservations should still be present
	if len(state.reservationMap) != 2 {
		t.Errorf("Expected 2 reservations after rebuild, got %d", len(state.reservationMap))
	}

	// Verify the specific reservations are still there
	for _, sessionID := range reservationSessionIDs {
		if _, ok := state.reservationMap[sessionID]; !ok {
			t.Errorf("Expected reservation with sessionID %s to be present", sessionID)
		}
	}

	// OpenSlots should be 5 (12 - 7)
	expectedOpenSlots := 5
	if state.OpenSlots() != expectedOpenSlots {
		t.Errorf("Expected OpenSlots()=%d, got %d", expectedOpenSlots, state.OpenSlots())
	}
}

// TestLobbyCapacity_PartialExpiredReservations demonstrates selective cleanup
// of only the expired reservations, preserving valid ones.
func TestLobbyCapacity_PartialExpiredReservations(t *testing.T) {
	state := newSocialTestMatchLabel()
	state.Mode = evr.ModeSocialPublic
	state.MaxSize = SocialLobbyMaxSize
	state.PlayerLimit = SocialLobbyMaxSize

	// Add 4 actual players
	for i := 0; i < 4; i++ {
		userID := uuid.Must(uuid.NewV4())
		sessionID := uuid.Must(uuid.NewV4())
		presence := &EvrMatchPresence{
			UserID:        userID,
			SessionID:     sessionID,
			RoleAlignment: evr.TeamSocial,
			Username:      "Player" + string(rune('A'+i)),
			DisplayName:   "Player" + string(rune('A'+i)),
			EvrID:         evr.EvrId{PlatformCode: evr.PlatformCode(i + 1), AccountId: uint64(i + 1)},
		}
		state.presenceMap[sessionID.String()] = presence
		state.presenceByEvrID[presence.EvrID] = presence
		state.joinTimestamps[sessionID.String()] = time.Now()
	}

	// Add 3 expired reservations
	for i := 0; i < 3; i++ {
		userID := uuid.Must(uuid.NewV4())
		sessionID := uuid.Must(uuid.NewV4())
		reservation := &slotReservation{
			Presence: &EvrMatchPresence{
				UserID:        userID,
				SessionID:     sessionID,
				RoleAlignment: evr.TeamSocial,
				Username:      "ExpiredReserved" + string(rune('A'+i)),
				DisplayName:   "ExpiredReserved" + string(rune('A'+i)),
				EvrID:         evr.EvrId{PlatformCode: evr.PlatformCode(20 + i), AccountId: uint64(20 + i)},
			},
			Expiry: time.Now().Add(-1 * time.Minute),
		}
		state.reservationMap[sessionID.String()] = reservation
	}

	// Add 2 valid reservations
	validReservationSessionIDs := make([]string, 0)
	for i := 0; i < 2; i++ {
		userID := uuid.Must(uuid.NewV4())
		sessionID := uuid.Must(uuid.NewV4())
		reservation := &slotReservation{
			Presence: &EvrMatchPresence{
				UserID:        userID,
				SessionID:     sessionID,
				RoleAlignment: evr.TeamSocial,
				Username:      "ValidReserved" + string(rune('A'+i)),
				DisplayName:   "ValidReserved" + string(rune('A'+i)),
				EvrID:         evr.EvrId{PlatformCode: evr.PlatformCode(30 + i), AccountId: uint64(30 + i)},
			},
			Expiry: time.Now().Add(5 * time.Minute),
		}
		state.reservationMap[sessionID.String()] = reservation
		validReservationSessionIDs = append(validReservationSessionIDs, sessionID.String())
	}

	state.rebuildCache()

	// All 3 expired reservations should be deleted
	// Only 2 valid reservations should remain
	if len(state.reservationMap) != 2 {
		t.Errorf("Expected 2 valid reservations after cleanup, got %d", len(state.reservationMap))
	}

	// Verify valid reservations are preserved
	for _, sessionID := range validReservationSessionIDs {
		if _, ok := state.reservationMap[sessionID]; !ok {
			t.Errorf("Expected valid reservation with sessionID %s to be present", sessionID)
		}
	}

	// Size should be 6 (4 actual + 2 valid reservations)
	if state.Size != 6 {
		t.Errorf("Expected Size=6 (4 actual + 2 valid reservations), got %d", state.Size)
	}

	// OpenSlots should be 6 (12 - 6)
	expectedOpenSlots := 6
	if state.OpenSlots() != expectedOpenSlots {
		t.Errorf("Expected OpenSlots()=%d, got %d", expectedOpenSlots, state.OpenSlots())
	}
}

// TestLobbyCapacity_EmptyLobbyWithReservations demonstrates an edge case where
// a lobby has no actual players but has reservations from party members.
func TestLobbyCapacity_EmptyLobbyWithReservations(t *testing.T) {
	state := newSocialTestMatchLabel()
	state.Mode = evr.ModeSocialPublic
	state.MaxSize = SocialLobbyMaxSize
	state.PlayerLimit = SocialLobbyMaxSize

	// No actual players, just reservations

	// Add 3 valid reservations
	for i := 0; i < 3; i++ {
		userID := uuid.Must(uuid.NewV4())
		sessionID := uuid.Must(uuid.NewV4())
		reservation := &slotReservation{
			Presence: &EvrMatchPresence{
				UserID:        userID,
				SessionID:     sessionID,
				RoleAlignment: evr.TeamSocial,
				Username:      "Reserved" + string(rune('A'+i)),
				DisplayName:   "Reserved" + string(rune('A'+i)),
				EvrID:         evr.EvrId{PlatformCode: evr.PlatformCode(i + 1), AccountId: uint64(i + 1)},
			},
			Expiry: time.Now().Add(5 * time.Minute),
		}
		state.reservationMap[sessionID.String()] = reservation
		state.joinTimestamps[sessionID.String()] = time.Now()
	}

	state.rebuildCache()

	// Size should be 3 (only reservations)
	if state.Size != 3 {
		t.Errorf("Expected Size=3, got %d", state.Size)
	}

	// OpenSlots should be 9 (12 - 3)
	expectedOpenSlots := 9
	if state.OpenSlots() != expectedOpenSlots {
		t.Errorf("Expected OpenSlots()=%d, got %d", expectedOpenSlots, state.OpenSlots())
	}
}
