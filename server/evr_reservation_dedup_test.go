package server

import (
	"context"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// These tests cover the CREATE-side reservation-dedup fix: match slot
// reservations are keyed by session ID, but an EVR client gets a NEW session ID
// on every WebSocket reconnect while its user ID (from the auth token) stays
// stable. Without a UserID-keyed create-side guard, a reconnecting member could
// acquire a SECOND reservation (under the new session ID) while the original is
// still un-expired -- a phantom seat that counts toward capacity until it
// expires. The consume side already falls back to user ID
// (LoadAndDeleteReservationByUserIDRaw); these tests pin the create side to the
// same behavior.

// signalCreatePartyReservations drives the real SignalCreatePartyReservations
// handler (the primary create-dedup site) through MatchSignal, returning the
// updated state.
func signalCreatePartyReservations(t *testing.T, m *EvrMatch, state *MatchLabel, members ...*EvrMatchPresence) *MatchLabel {
	t.Helper()
	env := NewSignalEnvelope("", SignalCreatePartyReservations, SignalCreatePartyReservationsPayload{Members: members})
	newState, resp := m.MatchSignal(context.Background(), reconnectTestLogger(), nil, &reconnectTestNakamaModule{}, nil, 1, state, env.String())
	ns, ok := newState.(*MatchLabel)
	if !ok {
		t.Fatalf("MatchSignal returned non-*MatchLabel state; response=%q", resp)
	}
	return ns
}

func newDedupTestState() *MatchLabel {
	state := newSocialTestMatchLabel()
	state.Mode = evr.ModeSocialPublic
	state.MaxSize = SocialLobbyMaxSize
	state.PlayerLimit = SocialLobbyMaxSize
	return state
}

func reservationsForUser(state *MatchLabel, userID uuid.UUID) []string {
	sids := make([]string, 0, 1)
	for sid, r := range state.reservationMap {
		if r.Presence.UserID == userID {
			sids = append(sids, sid)
		}
	}
	return sids
}

// TestReservationDedup_ReconnectDoesNotDoubleCount is RED test A: a member who
// reconnects (same user ID, new session ID) must end with exactly ONE
// reservation, not a second phantom one under the new session ID.
func TestReservationDedup_ReconnectDoesNotDoubleCount(t *testing.T) {
	m := &EvrMatch{}
	state := newDedupTestState()

	userID := uuid.Must(uuid.NewV4())
	sidA := uuid.Must(uuid.NewV4())
	sidB := uuid.Must(uuid.NewV4())

	memberA := &EvrMatchPresence{
		UserID:        userID,
		SessionID:     sidA,
		RoleAlignment: evr.TeamSocial,
		Username:      "M",
		DisplayName:   "M",
		EvrID:         evr.EvrId{PlatformCode: 1, AccountId: 1},
	}
	state = signalCreatePartyReservations(t, m, state, memberA)

	if got := len(state.reservationMap); got != 1 {
		t.Fatalf("setup: expected 1 reservation after first create, got %d", got)
	}

	// M's client reconnects: a NEW session ID (sidB), SAME user ID. The create
	// path re-fires for M under sidB.
	memberB := &EvrMatchPresence{
		UserID:        userID,
		SessionID:     sidB,
		RoleAlignment: evr.TeamSocial,
		Username:      "M",
		DisplayName:   "M",
		EvrID:         evr.EvrId{PlatformCode: 1, AccountId: 1},
	}
	state = signalCreatePartyReservations(t, m, state, memberB)

	// Exactly ONE reservation for M overall (no phantom orphan under sidA).
	if got := len(state.reservationMap); got != 1 {
		t.Errorf("expected exactly 1 reservation after reconnect, got %d (phantom seat leak)", got)
	}
	if sids := reservationsForUser(state, userID); len(sids) != 1 {
		t.Errorf("expected exactly 1 reservation for user M, got %d: %v", len(sids), sids)
	}
	if _, ok := state.reservationMap[sidB.String()]; !ok {
		t.Errorf("expected M's reservation to be keyed under the new session ID sidB")
	}
	if _, ok := state.reservationMap[sidA.String()]; ok {
		t.Errorf("stale reservation under old session ID sidA is still present (orphan not reclaimed)")
	}
	// ReservationCount must reflect ONE held seat, not two.
	if state.ReservationCount != 1 {
		t.Errorf("expected ReservationCount=1 (one held seat), got %d", state.ReservationCount)
	}
}

// TestReservationDedup_OpenSlotsCorrectAfterReconnect is RED test B: OpenSlots
// after the reconnect reservation must equal the value for a single held seat
// (proving the phantom seat is gone).
func TestReservationDedup_OpenSlotsCorrectAfterReconnect(t *testing.T) {
	m := &EvrMatch{}
	state := newDedupTestState()

	userID := uuid.Must(uuid.NewV4())
	sidA := uuid.Must(uuid.NewV4())
	sidB := uuid.Must(uuid.NewV4())

	base := &EvrMatchPresence{
		UserID: userID, SessionID: sidA, RoleAlignment: evr.TeamSocial,
		Username: "M", DisplayName: "M", EvrID: evr.EvrId{PlatformCode: 1, AccountId: 1},
	}
	state = signalCreatePartyReservations(t, m, state, base)

	reconnect := &EvrMatchPresence{
		UserID: userID, SessionID: sidB, RoleAlignment: evr.TeamSocial,
		Username: "M", DisplayName: "M", EvrID: evr.EvrId{PlatformCode: 1, AccountId: 1},
	}
	state = signalCreatePartyReservations(t, m, state, reconnect)

	// One held seat: OpenSlots = MaxSize - Size(0) - ReservationCount(1).
	wantOpen := SocialLobbyMaxSize - 1
	if got := state.OpenSlots(); got != wantOpen {
		t.Errorf("expected OpenSlots()=%d (one held seat), got %d (phantom seat still counted)", wantOpen, got)
	}
}

// TestReservationDedup_SameMemberReseedKeepsSeat is the restore/re-seed
// regression: a create re-fire for the SAME member under the SAME session ID
// (as a legitimate restore/seed would produce) must end with exactly ONE
// reservation, never zero. Proves the UserID upsert (remove-then-insert) does
// not eat a legitimate restore of the same member.
func TestReservationDedup_SameMemberReseedKeepsSeat(t *testing.T) {
	m := &EvrMatch{}
	state := newDedupTestState()

	userID := uuid.Must(uuid.NewV4())
	sid := uuid.Must(uuid.NewV4())

	member := &EvrMatchPresence{
		UserID: userID, SessionID: sid, RoleAlignment: evr.TeamSocial,
		Username: "M", DisplayName: "M", EvrID: evr.EvrId{PlatformCode: 1, AccountId: 1},
	}
	state = signalCreatePartyReservations(t, m, state, member)
	// Re-seed / restore the same member under the same session ID.
	reseed := &EvrMatchPresence{
		UserID: userID, SessionID: sid, RoleAlignment: evr.TeamSocial,
		Username: "M", DisplayName: "M", EvrID: evr.EvrId{PlatformCode: 1, AccountId: 1},
	}
	state = signalCreatePartyReservations(t, m, state, reseed)

	if got := len(reservationsForUser(state, userID)); got != 1 {
		t.Errorf("expected exactly 1 reservation for re-seeded member M, got %d", got)
	}
	if _, ok := state.reservationMap[sid.String()]; !ok {
		t.Errorf("re-seed lost the seat: no reservation under session ID %s", sid)
	}
}

// TestReservationDedup_DistinctMembersUnaffected is the distinct-members
// regression: reservations for two DIFFERENT users under different session IDs
// must both remain (proving the UserID dedup does not collapse different
// members).
func TestReservationDedup_DistinctMembersUnaffected(t *testing.T) {
	m := &EvrMatch{}
	state := newDedupTestState()

	user1 := uuid.Must(uuid.NewV4())
	user2 := uuid.Must(uuid.NewV4())
	sid1 := uuid.Must(uuid.NewV4())
	sid2 := uuid.Must(uuid.NewV4())

	member1 := &EvrMatchPresence{
		UserID: user1, SessionID: sid1, RoleAlignment: evr.TeamSocial,
		Username: "One", DisplayName: "One", EvrID: evr.EvrId{PlatformCode: 1, AccountId: 1},
	}
	member2 := &EvrMatchPresence{
		UserID: user2, SessionID: sid2, RoleAlignment: evr.TeamSocial,
		Username: "Two", DisplayName: "Two", EvrID: evr.EvrId{PlatformCode: 1, AccountId: 2},
	}
	state = signalCreatePartyReservations(t, m, state, member1)
	state = signalCreatePartyReservations(t, m, state, member2)

	if got := len(state.reservationMap); got != 2 {
		t.Errorf("expected 2 reservations for 2 distinct members, got %d", got)
	}
	if _, ok := state.reservationMap[sid1.String()]; !ok {
		t.Errorf("member One's reservation was collapsed away")
	}
	if _, ok := state.reservationMap[sid2.String()]; !ok {
		t.Errorf("member Two's reservation was collapsed away")
	}
	if state.ReservationCount != 2 {
		t.Errorf("expected ReservationCount=2 for two distinct members, got %d", state.ReservationCount)
	}
}
