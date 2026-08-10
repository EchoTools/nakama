package server

import (
	"context"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
)

// These tests cover the atomic capacity guard on party-reservation creation
// (production 2026-07-05, deployed as v3.27.2-evr.319). Before the fix,
// SignalCreatePartyReservations upserted a reservation UNCONDITIONALLY -- it only
// checked whether the member was already present. Reservations therefore stacked
// onto already-full social lobbies; the follower's join then died at the capacity
// gate (ErrJoinRejectReasonReservationViolated) and the client stormed rejects
// (2-13 rejects, up to ~50s) before landing in a different lobby.
//
// The signal handler runs on the single match goroutine (match_handler.go's
// select over signalCh/joinAttemptCh), so a capacity check inside the handler is
// atomic with respect to joins AND to other reservation-creates. rebuildCache()
// after each upsert keeps OpenSlots() current within the create loop, so the
// SECOND follower in one signal sees the FIRST's reservation -- closing the
// two-follower overbook race (issue #510 transactional shape).

// fillSocialLobby adds n filler player presences to the match so that
// OpenSlots() == MaxSize - n (Size counts real presences). It mirrors a lobby
// that already has n occupants.
func fillSocialLobby(state *MatchLabel, n int) {
	for i := 0; i < n; i++ {
		sid := uuid.Must(uuid.NewV4())
		state.presenceMap[sid.String()] = &EvrMatchPresence{
			UserID:        uuid.Must(uuid.NewV4()),
			SessionID:     sid,
			RoleAlignment: evr.TeamSocial,
		}
	}
	state.rebuildCache()
}

func socialMember() *EvrMatchPresence {
	return &EvrMatchPresence{
		UserID:        uuid.Must(uuid.NewV4()),
		SessionID:     uuid.Must(uuid.NewV4()),
		RoleAlignment: evr.TeamSocial,
	}
}

// FIX 1 test A: a reservation must NOT be created for a member when the lobby is
// already full (OpenSlots == 0). RED without the guard (reservation created,
// OpenSlots goes negative); GREEN with it (member simply gets no reservation).
func TestReservationCapacity_NoReservationIntoFullLobby(t *testing.T) {
	m := &EvrMatch{}
	state := newDedupTestState()
	fillSocialLobby(state, SocialLobbyMaxSize) // OpenSlots == 0
	if got := state.OpenSlots(); got != 0 {
		t.Fatalf("setup: expected OpenSlots()==0 (full lobby), got %d", got)
	}

	member := socialMember()
	before := len(state.reservationMap)
	state = signalCreatePartyReservations(t, m, state, member)

	if got := len(state.reservationMap); got != before {
		t.Errorf("expected NO reservation created into a full lobby; reservationMap grew %d -> %d", before, got)
	}
	if _, ok := state.reservationMap[member.SessionID.String()]; ok {
		t.Errorf("a reservation was created for the member despite the lobby being full")
	}
	if got := state.OpenSlots(); got < 0 {
		t.Errorf("OpenSlots() went negative (%d): the reservation overbooked a full lobby", got)
	}
}

// FIX 1 test B: the two-follower race. A lobby with exactly ONE open slot and a
// single signal carrying TWO members must produce exactly ONE reservation -- the
// second is skipped for lack of room. RED without the in-handler check + rebuild
// (both created -> overbook, OpenSlots == -1); GREEN with it (exactly one).
func TestReservationCapacity_TwoFollowersOneSlot_OnlyOneReserved(t *testing.T) {
	m := &EvrMatch{}
	state := newDedupTestState()
	fillSocialLobby(state, SocialLobbyMaxSize-1) // OpenSlots == 1
	if got := state.OpenSlots(); got != 1 {
		t.Fatalf("setup: expected OpenSlots()==1 (one open slot), got %d", got)
	}

	m1 := socialMember()
	m2 := socialMember()
	state = signalCreatePartyReservations(t, m, state, m1, m2)

	reserved := 0
	if _, ok := state.reservationMap[m1.SessionID.String()]; ok {
		reserved++
	}
	if _, ok := state.reservationMap[m2.SessionID.String()]; ok {
		reserved++
	}
	if reserved != 1 {
		t.Errorf("expected exactly 1 of 2 followers reserved into a 1-slot lobby, got %d (overbook)", reserved)
	}
	if state.ReservationCount != 1 {
		t.Errorf("expected ReservationCount==1, got %d", state.ReservationCount)
	}
	if got := state.OpenSlots(); got != 0 {
		t.Errorf("expected OpenSlots()==0 after the single slot is filled, got %d", got)
	}
}

// FIX 1 test C: happy path -- a lobby with room for the whole party reserves all
// members (no regression).
func TestReservationCapacity_RoomForWholeParty_AllReserved(t *testing.T) {
	m := &EvrMatch{}
	state := newDedupTestState()
	fillSocialLobby(state, SocialLobbyMaxSize-3) // OpenSlots == 3
	if got := state.OpenSlots(); got != 3 {
		t.Fatalf("setup: expected OpenSlots()==3, got %d", got)
	}

	members := []*EvrMatchPresence{socialMember(), socialMember(), socialMember()}
	state = signalCreatePartyReservations(t, m, state, members...)

	for i, mm := range members {
		if _, ok := state.reservationMap[mm.SessionID.String()]; !ok {
			t.Errorf("member %d (%s) should be reserved when there is room", i, mm.SessionID)
		}
	}
	if state.ReservationCount != 3 {
		t.Errorf("expected ReservationCount==3, got %d", state.ReservationCount)
	}
	if got := state.OpenSlots(); got != 0 {
		t.Errorf("expected OpenSlots()==0 after reserving the whole party, got %d", got)
	}
}

// The arena-mode guard that used to be tested here is deliberately NOT part of
// this change. createPartyReservations hardcodes RoleAlignment: evr.TeamSocial,
// so gating it on label.IsSocial() is correct in principle -- but both current
// callers already gate, making it defence in depth rather than a live fix, and
// adding it breaks TestCreatePartyReservations_GroupParty_ReservesFollower,
// which calls the function with a match ID it never registers. That test is
// valid under the current contract, where the function trusts its callers.
// Changing that contract is a separate change with its own test updates.

func TestCreatePartyReservations_SocialMatch_ReservesFollower(t *testing.T) {
	env := mkGroupReservationEnv(t, "monarch12")
	groupID := uuid.Must(uuid.NewV4())

	follower := newPartyMemberSession(t, "follower", env.tracker, env.pr, env.ep)
	env.sessions.sessions[follower.id] = follower
	env.tracker.Track(context.Background(), env.leaderSID, env.ph.Stream, env.leaderUID, PresenceMeta{Username: "leader"})
	env.tracker.Track(context.Background(), follower.id, env.ph.Stream, follower.userID, PresenceMeta{Username: "follower"})

	socialMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.registry.SetMatch(socialMatchID, mkGroupSocialLabel(socialMatchID, groupID))

	env.ep.createPartyReservations(context.Background(), loggerForTest(t), socialMatchID, env.leaderSID, env.ph.ID)

	assert.Contains(t, env.registry.reservedSessionIDs(), follower.id,
		"a follower whose leader is in a social match must still be reserved")
}
