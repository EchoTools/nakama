package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// TestRejectReasonCode_CoversEveryRejectSentinel pins that every reason
// MatchJoinAttempt can return maps to a code other than "unknown". A new
// ErrJoinRejectReason* that is not wired into RejectReasonCode shows up here
// rather than as an unclassifiable log line in production.
func TestRejectReasonCode_CoversEveryRejectSentinel(t *testing.T) {
	for _, err := range []error{
		ErrJoinRejectReasonUnassignedLobby,
		ErrJoinRejectReasonDuplicateJoin,
		ErrJoinRejectDuplicateEvrID,
		ErrJoinRejectReasonLobbyFull,
		ErrJoinRejectReasonReservationViolated,
		ErrJoinRejectReasonFailedToAssignTeam,
		ErrJoinRejectReasonPartyMembersMustHaveRoles,
		ErrJoinRejectReasonMatchTerminating,
		ErrJoinRejectReasonMatchClosed,
		ErrJoinRejectReasonFeatureMismatch,
	} {
		if got := RejectReasonCode(err.Error()); got == RejectReasonUnknown {
			t.Errorf("reason %q maps to %q; wire it into RejectReasonCode", err.Error(), got)
		}
	}
}

// TestRejectReasonCode_NoPrefixCollision is the guard the doc comment on
// RejectReasonCode promises. ErrJoinRejectReasonReservationViolated
// ("lobby full: reservation violated") has ErrJoinRejectReasonLobbyFull
// ("lobby full") as a literal prefix, so any move to prefix or substring
// matching would silently classify a broken-promise event as ordinary
// contention — exactly the conflation #585 exists to end.
func TestRejectReasonCode_NoPrefixCollision(t *testing.T) {
	full := ErrJoinRejectReasonLobbyFull.Error()
	violated := ErrJoinRejectReasonReservationViolated.Error()

	if !strings.HasPrefix(violated, full) {
		t.Fatalf("premise changed: %q is no longer prefixed by %q; this guard needs rewriting", violated, full)
	}
	if RejectReasonCode(full) == RejectReasonCode(violated) {
		t.Fatalf("prefix-sibling reasons collapsed to the same code %q", RejectReasonCode(full))
	}
}

// TestHeldReservationFor_DistinguishesReservationKinds is the #585 proposal 1
// data path: the fact that a seat was being held for this specific user, and
// how long it had left, must be recoverable from the label the match handler
// returns with its decision.
func TestHeldReservationFor_DistinguishesReservationKinds(t *testing.T) {
	state := reconnectTestState(evr.ModeArenaPublic)
	state.ID.Node = "test-node" // MatchID does not unmarshal without a node

	seated := reconnectTestPlayer("held-seated", evr.TeamBlue)
	state.presenceMap[seated.GetSessionId()] = seated
	state.presenceByEvrID[seated.EvrID] = seated

	crashedID := uuid.NewV5(uuid.Nil, "user-held-crashed")
	crashed := reconnectTestPlayer("held-crashed", evr.TeamOrange)
	crashed.UserID = crashedID
	state.reconnectReservations[crashedID.String()] = &reconnectReservation{
		Presence: crashed,
		Expiry:   time.Now().Add(45 * time.Second),
		UserID:   crashedID.String(),
		ID:       "reservation-correlation-id",
	}

	partyID := uuid.NewV5(uuid.Nil, "user-held-party")
	party := reconnectTestPlayer("held-party", evr.TeamBlue)
	party.UserID = partyID
	state.reservationMap[party.GetSessionId()] = &slotReservation{
		Presence: party,
		Expiry:   time.Now().Add(15 * time.Second),
	}

	state.rebuildCache()

	// Round-trip through the label string, which is the actual channel: this is
	// what LobbyJoinEntrants receives back from matchRegistry.JoinAttempt.
	decoded := decodeMatchLabel(state.GetLabel())
	if decoded == nil {
		t.Fatal("label did not round-trip through JSON")
	}

	reconnectHeld := heldReservationFor(decoded, crashedID.String())
	if reconnectHeld == nil {
		t.Fatal("crashed player's held seat is not visible on the label")
	}
	if reconnectHeld.ReservationKind != ReservationKindReconnect {
		t.Errorf("expected kind %q, got %q", ReservationKindReconnect, reconnectHeld.ReservationKind)
	}
	if reconnectHeld.ReservationID != "reservation-correlation-id" {
		t.Errorf("expected the correlation id to survive to the label, got %q", reconnectHeld.ReservationID)
	}
	ttl, ok := reconnectHeld.ReservationTimeToExpiry(time.Now())
	if !ok || ttl <= 0 || ttl > 45*time.Second {
		t.Errorf("expected a positive time-to-expiry within the window, got %v (ok=%v)", ttl, ok)
	}

	slotHeld := heldReservationFor(decoded, partyID.String())
	if slotHeld == nil {
		t.Fatal("party slot reservation is not visible on the label")
	}
	if slotHeld.ReservationKind != ReservationKindSlot {
		t.Errorf("expected kind %q, got %q", ReservationKindSlot, slotHeld.ReservationKind)
	}
	if slotHeld.ReservationID != "" {
		t.Errorf("slot reservations carry no correlation id, got %q", slotHeld.ReservationID)
	}

	// A connected player is not a held seat — this is what keeps ordinary
	// contention at WARN under proposal 3.
	if got := heldReservationFor(decoded, seated.UserID.String()); got != nil {
		t.Errorf("a seated player must not read as holding a reservation, got %+v", got)
	}
	if got := heldReservationFor(decoded, uuid.Must(uuid.NewV4()).String()); got != nil {
		t.Errorf("a stranger must not read as holding a reservation, got %+v", got)
	}
}

// TestHeldReservationFor_ExpiredSeatIsNotAPromise: an expired reservation is
// swept by rebuildCache, so a player refused after their window closed is
// ordinary contention and must stay WARN. Promoting that to ERROR would make
// the severity meaningless.
func TestHeldReservationFor_ExpiredSeatIsNotAPromise(t *testing.T) {
	state := reconnectTestState(evr.ModeArenaPublic)
	state.ID.Node = "test-node" // MatchID does not unmarshal without a node

	crashedID := uuid.NewV5(uuid.Nil, "user-expired-crashed")
	crashed := reconnectTestPlayer("expired-crashed", evr.TeamBlue)
	crashed.UserID = crashedID
	state.reconnectReservations[crashedID.String()] = &reconnectReservation{
		Presence: crashed,
		Expiry:   time.Now().Add(-time.Second),
		UserID:   crashedID.String(),
		ID:       "expired-reservation",
	}
	state.rebuildCache()

	decoded := decodeMatchLabel(state.GetLabel())
	if decoded == nil {
		t.Fatal("label did not round-trip through JSON")
	}
	if got := heldReservationFor(decoded, crashedID.String()); got != nil {
		t.Fatalf("an expired reservation must not read as a live promise, got %+v", got)
	}
}

// TestDecodeMatchLabel_FallsBackOnGarbage — the rejection logger must never
// panic or lose its capacity fields because the label did not parse; it falls
// back to the caller's label.
func TestDecodeMatchLabel_FallsBackOnGarbage(t *testing.T) {
	for _, in := range []string{"", "not json", "{", `{"id":`} {
		if got := decodeMatchLabel(in); got != nil {
			t.Errorf("decodeMatchLabel(%q) = %+v, want nil", in, got)
		}
	}
	valid, err := json.Marshal(&MatchLabel{PlayerLimit: 8})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := decodeMatchLabel(string(valid)); got == nil || got.PlayerLimit != 8 {
		t.Errorf("decodeMatchLabel of a valid label did not round-trip: %+v", got)
	}
}
