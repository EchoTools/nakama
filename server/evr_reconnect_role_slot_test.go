package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// arenaRoleSlotFixture builds a public arena lobby (MaxSize 16, PlayerLimit 8,
// TeamSize 4 — the production geometry) that is full on orange and one short on
// blue, with the blue vacancy held by a reconnect reservation for a crashed
// player.
//
// The whole-lobby gate is deliberately NOT the binding constraint here: MaxSize
// (16) exceeds PlayerLimit (8) because of spectator headroom, so OpenSlots() is
// 8 even with the roster full. Only the per-role gate can refuse a player. That
// is the geometry every public arena match runs in, and it is why a reconnect
// reservation that is invisible to the role gate protects nothing.
func arenaRoleSlotFixture(t *testing.T) (*MatchLabel, uuid.UUID) {
	t.Helper()

	state := reconnectTestState(evr.ModeArenaPublic)

	seated := []*EvrMatchPresence{
		reconnectTestPlayer("role-blue-one", evr.TeamBlue),
		reconnectTestPlayer("role-blue-two", evr.TeamBlue),
		reconnectTestPlayer("role-blue-three", evr.TeamBlue),
		reconnectTestPlayer("role-orange-one", evr.TeamOrange),
		reconnectTestPlayer("role-orange-two", evr.TeamOrange),
		reconnectTestPlayer("role-orange-three", evr.TeamOrange),
		reconnectTestPlayer("role-orange-four", evr.TeamOrange),
	}
	for _, p := range seated {
		state.presenceMap[p.GetSessionId()] = p
		state.presenceByEvrID[p.EvrID] = p
		state.joinTimestamps[p.GetSessionId()] = time.Now().Add(-time.Minute)
	}

	crashedID := uuid.NewV5(uuid.Nil, "user-role-slot-crashed")
	crashed := reconnectTestPlayer("role-slot-crashed-old", evr.TeamBlue)
	crashed.UserID = crashedID
	crashed.RoleAlignment = evr.TeamBlue
	state.reconnectReservations[crashedID.String()] = &reconnectReservation{
		Presence:     crashed,
		Expiry:       time.Now().Add(30 * time.Second),
		UserID:       crashedID.String(),
		DeferPenalty: true,
	}
	state.rebuildCache()

	// Guard the premise: the whole-lobby gate has slack, so anything that
	// happens below is decided by the per-role gate alone.
	if got := state.OpenSlots(); got < 1 {
		t.Fatalf("fixture precondition: expected whole-lobby slack, OpenSlots()=%d", got)
	}
	return state, crashedID
}

// TestReconnectReservation_RoleSlotNotStolenByNewcomer is the #584 mechanism
// test. A stranger with no reservation must not be seated in the role slot that
// a live reconnect reservation is holding.
//
// Before the fix: OpenSlotsByRole() derives from RoleCount(), which skips
// reservations entirely, so blue reads 3/4 and the newcomer is admitted into
// the crashed player's seat.
func TestReconnectReservation_RoleSlotNotStolenByNewcomer(t *testing.T) {
	state, _ := arenaRoleSlotFixture(t)

	newcomer := reconnectTestPlayer("role-slot-newcomer", evr.TeamUnassigned)
	newcomer.RoleAlignment = evr.TeamUnassigned

	m := &EvrMatch{}
	_, allowed, reason := m.MatchJoinAttempt(context.Background(), reconnectTestLogger(), nil,
		&reconnectTestNakamaModule{}, nil, 1, state, newcomer,
		NewJoinMetadata(newcomer).ToMatchMetadata())

	if allowed {
		t.Fatalf("newcomer was seated in a role slot held by a live reconnect reservation; "+
			"blue was 3 seated + 1 reserved against TeamSize %d (reason=%q)", state.TeamSize, reason)
	}
	requireJoinRejectReason(t, reason, ErrJoinRejectReasonLobbyFull)
}

// TestReconnectReservation_CrashedPlayerRejoinsAfterNewcomerAttempt is the #584
// end-to-end regression test: crash, a newcomer tries the lobby while the slot
// is held, then the crashed player relaunches and rejoins the same match.
//
// Before the fix the newcomer takes the blue seat and this rejoin is refused
// with "lobby full" — the exact production symptom in #584.
func TestReconnectReservation_CrashedPlayerRejoinsAfterNewcomerAttempt(t *testing.T) {
	state, crashedID := arenaRoleSlotFixture(t)

	m := &EvrMatch{}
	nk := &reconnectTestNakamaModule{}

	newcomer := reconnectTestPlayer("role-slot-interloper", evr.TeamUnassigned)
	newcomer.RoleAlignment = evr.TeamUnassigned
	next, _, _ := m.MatchJoinAttempt(context.Background(), reconnectTestLogger(), nil, nk, nil, 1,
		state, newcomer, NewJoinMetadata(newcomer).ToMatchMetadata())
	state = next.(*MatchLabel)

	// The crashed player relaunches. New session ID, same user ID — that is what
	// the reconnect reservation is keyed on.
	rejoin := reconnectTestPlayer("role-slot-crashed-new", evr.TeamUnassigned)
	rejoin.UserID = crashedID
	rejoin.RoleAlignment = evr.TeamUnassigned
	rejoin.SessionID = uuid.NewV5(uuid.Nil, "session-role-slot-crashed-new")

	after, allowed, reason := m.MatchJoinAttempt(context.Background(), reconnectTestLogger(), nil, nk, nil, 2,
		state, rejoin, NewJoinMetadata(rejoin).ToMatchMetadata())
	if !allowed {
		t.Fatalf("crashed player was refused from the match holding their reconnect reservation: %s", reason)
	}

	parsed := &EvrMatchPresence{}
	if err := json.Unmarshal([]byte(reason), parsed); err != nil {
		t.Fatalf("failed to parse accepted presence: %v", err)
	}
	if parsed.RoleAlignment != evr.TeamBlue {
		t.Fatalf("expected the reserved blue role to be restored, got %d", parsed.RoleAlignment)
	}
	if _, ok := after.(*MatchLabel).reconnectReservations[crashedID.String()]; ok {
		t.Fatalf("expected the reconnect reservation to be consumed on a successful rejoin")
	}
}
