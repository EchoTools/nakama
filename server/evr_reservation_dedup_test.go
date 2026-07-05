package server

import (
	"context"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
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

// TestReservationDedup_NilUserIDLeaveOnlyClearsOwn pins Fix 1: two DISTINCT
// members that both lack a user ID (UserID == uuid.Nil) hold party reservations.
// When one leaves as a non-leader, only the LEAVER's own reservation (keyed by
// session ID) must be cleared. A nil user ID is not a stable identity, so the
// by-UserID delete loop must NOT be used for it -- otherwise it would wrongly
// collapse the other nil-UserID member's still-valid reservation (mirrors
// upsertReservationByUserID's nil handling).
//
// Red without the guard: the by-UserID loop deletes BOTH nil-UserID
// reservations. Green with it: only the leaver's own reservation is deleted.
func TestReservationDedup_NilUserIDLeaveOnlyClearsOwn(t *testing.T) {
	m := &EvrMatch{}
	state := reconnectTestState(evr.ModeSocialPublic)

	partyID := uuid.Must(uuid.NewV4())
	leaverSid := uuid.Must(uuid.NewV4())
	otherSid := uuid.Must(uuid.NewV4())

	// Two distinct members, both with a NIL user ID, in the SAME party.
	leaver := &EvrMatchPresence{
		Node:          "test-node",
		UserID:        uuid.Nil,
		SessionID:     leaverSid,
		PartyID:       partyID,
		RoleAlignment: evr.TeamSocial,
		Username:      "leaver",
		DisplayName:   "Leaver",
		EvrID:         evr.EvrId{PlatformCode: 4, AccountId: 1},
		EntrantID:     uuid.Must(uuid.NewV4()),
	}
	other := &EvrMatchPresence{
		Node:          "test-node",
		UserID:        uuid.Nil,
		SessionID:     otherSid,
		PartyID:       partyID,
		RoleAlignment: evr.TeamSocial,
		Username:      "other",
		DisplayName:   "Other",
		EvrID:         evr.EvrId{PlatformCode: 4, AccountId: 2},
		EntrantID:     uuid.Must(uuid.NewV4()),
	}

	// Both are active presences and both hold reservations keyed by session ID.
	state.presenceMap[leaverSid.String()] = leaver
	state.presenceMap[otherSid.String()] = other
	state.joinTimestamps[leaverSid.String()] = time.Now().Add(-time.Minute)
	state.joinTimestamps[otherSid.String()] = time.Now().Add(-time.Minute)
	state.reservationMap[leaverSid.String()] = &slotReservation{Presence: leaver, Expiry: time.Now().Add(5 * time.Minute)}
	state.reservationMap[otherSid.String()] = &slotReservation{Presence: other, Expiry: time.Now().Add(5 * time.Minute)}
	state.rebuildCache()

	nk := &reconnectTestNakamaModule{}
	dispatcher := &reconnectTestDispatcher{}
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")
	// Non-leader voluntary leave (nk is not *RuntimeGoNakamaModule, so the
	// leadership lookup falls through to the non-leader path).
	leavePresence := reconnectTestPresence{EvrMatchPresence: leaver, reason: runtime.PresenceReasonLeave}

	got := m.MatchLeave(ctx, reconnectTestLogger(), nil, nk, dispatcher, 1, state, []runtime.Presence{leavePresence})
	stateAfter, ok := got.(*MatchLabel)
	if !ok {
		t.Fatalf("MatchLeave returned non-*MatchLabel state: %T", got)
	}

	// The leaver's own reservation must be gone.
	if _, ok := stateAfter.reservationMap[leaverSid.String()]; ok {
		t.Errorf("expected leaver's own reservation (sid %s) to be cleared", leaverSid)
	}
	// The OTHER nil-UserID member's reservation must REMAIN.
	if _, ok := stateAfter.reservationMap[otherSid.String()]; !ok {
		t.Errorf("other nil-UserID member's reservation (sid %s) was wrongly collapsed by the by-UserID delete", otherSid)
	}
	if got := len(stateAfter.reservationMap); got != 1 {
		t.Errorf("expected exactly 1 reservation remaining (the other member's), got %d", got)
	}
}

// TestReservationDedup_NilUserIDCreateNotSkipped pins Fix 2: a member with a
// NIL user ID whose session ID is NOT already an active presence must get a
// reservation created, even when some OTHER nil-UserID presence exists. A nil
// user ID is not a stable identity, so the "alreadyPresent" short-circuit must
// NOT treat all nil-UserID presences as the same user (mirrors
// upsertReservationByUserID, which keys nil reservations by session ID).
//
// Red without the guard: the by-UserID alreadyPresent check matches the other
// nil-UserID presence and skips creation (0 reservations). Green with it: the
// session-ID comparison finds no match and the reservation is created.
func TestReservationDedup_NilUserIDCreateNotSkipped(t *testing.T) {
	m := &EvrMatch{}
	state := newDedupTestState()

	// An UNRELATED active presence that also lacks a user ID.
	otherSid := uuid.Must(uuid.NewV4())
	state.presenceMap[otherSid.String()] = &EvrMatchPresence{
		UserID:        uuid.Nil,
		SessionID:     otherSid,
		RoleAlignment: evr.TeamSocial,
		Username:      "other",
		DisplayName:   "Other",
		EvrID:         evr.EvrId{PlatformCode: 4, AccountId: 9},
	}
	state.rebuildCache()

	// A DIFFERENT member, also nil UserID, whose session ID is NOT in presenceMap.
	newSid := uuid.Must(uuid.NewV4())
	member := &EvrMatchPresence{
		UserID:        uuid.Nil,
		SessionID:     newSid,
		RoleAlignment: evr.TeamSocial,
		Username:      "new",
		DisplayName:   "New",
		EvrID:         evr.EvrId{PlatformCode: 4, AccountId: 10},
	}
	state = signalCreatePartyReservations(t, m, state, member)

	// The reservation must be created, keyed by the member's own session ID.
	if _, ok := state.reservationMap[newSid.String()]; !ok {
		t.Errorf("expected a reservation created for nil-UserID member (sid %s); it was wrongly skipped as already-present", newSid)
	}
	if got := len(state.reservationMap); got != 1 {
		t.Errorf("expected exactly 1 reservation, got %d", got)
	}
}
