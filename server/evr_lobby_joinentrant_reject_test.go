package server

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// isJoinRejectReason reports whether the free-text reason MatchJoinAttempt put
// on the wire routes to `want`, by the same decode-then-errors.Is path
// production uses.
//
// Every assertion on a reject reason in this package goes through this or
// requireJoinRejectReason rather than comparing the raw string. A test that
// still wrote `reason == ErrJoinRejectReasonLobbyFull.Error()` would re-pin the
// wire text as the routing key — the exact coupling #585 proposal 4 removed
// from production — and would keep passing while production had stopped
// depending on it.
func isJoinRejectReason(reason string, want *JoinRejectReason) bool {
	return errors.Is(JoinRejectReasonOf(reason), want)
}

// requireJoinRejectReason is the fatal form of isJoinRejectReason.
func requireJoinRejectReason(t *testing.T, reason string, want *JoinRejectReason) {
	t.Helper()
	if !isJoinRejectReason(reason, want) {
		t.Fatalf("reject reason %q did not route to %q (code %q)", reason, want.Error(), want.Code())
	}
}

// TestJoinRejectReasonRegistry_IsAClosedSetWithUniqueCodesAndMessages is the
// structural half of #585 proposal 4.
//
// Every reject identity is registered exactly once, by newJoinRejectReason,
// which pairs the log-facing code with the wire message in a single literal and
// panics on a duplicate of either. There is no way to add a reason without a
// code and no way to give two reasons the same one, so the old failure mode —
// a new ErrJoinRejectReason* that nobody wired into a switch, silently
// classified "unknown" in production — cannot recur.
func TestJoinRejectReasonRegistry_IsAClosedSetWithUniqueCodesAndMessages(t *testing.T) {
	reasons := JoinRejectReasons()
	if len(reasons) == 0 {
		t.Fatal("the reject reason registry is empty; nothing is registered")
	}

	seenCodes := make(map[string]*JoinRejectReason, len(reasons))
	seenMessages := make(map[string]*JoinRejectReason, len(reasons))
	for _, r := range reasons {
		if r.Code() == "" || r.Code() == RejectReasonUnknown {
			t.Errorf("reason %q has code %q; a registered reason must carry a stable, non-%q code",
				r.Error(), r.Code(), RejectReasonUnknown)
		}
		if r.Error() == "" {
			t.Errorf("reason with code %q has an empty wire message", r.Code())
		}
		if prev, dup := seenCodes[r.Code()]; dup {
			t.Errorf("code %q is shared by %q and %q", r.Code(), prev.Error(), r.Error())
		}
		if prev, dup := seenMessages[r.Error()]; dup {
			t.Errorf("message %q is shared by codes %q and %q", r.Error(), prev.Code(), r.Code())
		}
		seenCodes[r.Code()] = r
		seenMessages[r.Error()] = r

		// The decode is total over the registry: every registered message
		// resolves to its own identity and its own code.
		if got := RejectReasonCode(r.Error()); got != r.Code() {
			t.Errorf("RejectReasonCode(%q) = %q, want %q", r.Error(), got, r.Code())
		}
		if !errors.Is(JoinRejectReasonOf(r.Error()), r) {
			t.Errorf("JoinRejectReasonOf(%q) did not resolve to its own identity", r.Error())
		}
	}

	// The two consumer-side codes have no wire message — LobbyJoinEntrants sets
	// them from `found` and `labelStr`, which never cross as a reason — so they
	// are not in the registry. They must still not collide with anything in it.
	for _, code := range []string{RejectReasonMatchNotFound, RejectReasonMatchLabelEmpty, RejectReasonUnknown} {
		if prev, dup := seenCodes[code]; dup {
			t.Errorf("consumer-side code %q collides with registered reason %q", code, prev.Error())
		}
	}
}

// TestJoinRejectReason_CodeAndWireTextArePinned locks both halves of every
// identity to literals.
//
// The wire message is player-visible: pipeline_match.go forwards it verbatim as
// the MATCH_JOIN_REJECTED envelope message, and LobbyJoinEntrants interpolates
// it into "server is full: join not allowed: <message>". The code is
// alert-visible: it is the reject_reason field log consumers route on. Pinning
// both to literals here means neither can be changed as a side effect of
// touching the other — which is exactly the coupling #585 proposal 4 set out to
// break — and makes any change to either a deliberate edit of this table.
func TestJoinRejectReason_CodeAndWireTextArePinned(t *testing.T) {
	want := []struct {
		reason  *JoinRejectReason
		code    string
		message string
	}{
		{ErrJoinRejectReasonUnassignedLobby, "unassigned_lobby", "unassigned lobby"},
		{ErrJoinRejectReasonDuplicateJoin, "duplicate_join", "duplicate join"},
		{ErrJoinRejectDuplicateEvrID, "duplicate_evr_id", "duplicate evr id"},
		{ErrJoinRejectReasonLobbyFull, "lobby_full", "lobby full"},
		{ErrJoinRejectReasonReservationViolated, "reservation_violated", "lobby full: reservation violated"},
		{ErrJoinRejectReasonFailedToAssignTeam, "failed_to_assign_team", "failed to assign team"},
		{ErrJoinRejectReasonPartyMembersMustHaveRoles, "party_members_must_have_roles", "party members must have roles"},
		{ErrJoinRejectReasonMatchTerminating, "match_terminating", "match terminating"},
		{ErrJoinRejectReasonMatchClosed, "match_closed", "match closed to new entrants"},
		{ErrJoinRejectReasonFeatureMismatch, "feature_mismatch", "feature mismatch"},
	}

	for _, tc := range want {
		if got := tc.reason.Code(); got != tc.code {
			t.Errorf("code drift: %q now carries code %q, want %q", tc.message, got, tc.code)
		}
		if got := tc.reason.Error(); got != tc.message {
			t.Errorf("wire text drift: code %q now puts %q on the wire, want %q", tc.code, got, tc.message)
		}
	}

	if got := len(JoinRejectReasons()); got != len(want) {
		t.Errorf("registry holds %d reasons, this table pins %d; add the new one here", got, len(want))
	}
}

// TestRejectReasonCode_NoPrefixCollision is the guard #585 names, carried
// forward onto the typed scheme.
//
// ErrJoinRejectReasonReservationViolated ("lobby full: reservation violated")
// still has ErrJoinRejectReasonLobbyFull ("lobby full") as a literal prefix, and
// deliberately so: both strings are player-visible wire text, so neither can be
// reworded to break the relationship without changing what a client sees.
//
// What changed is that the relationship no longer matters to routing. The
// decode is a single Go map index, which cannot partially match, and everything
// downstream of it routes with errors.Is over pointer identity. There is no
// string comparison left in the path that could be "moved to prefix matching".
// This test pins that: a string merely prefixed by a registered message
// resolves to nothing at all, rather than to the reason it is prefixed by.
func TestRejectReasonCode_NoPrefixCollision(t *testing.T) {
	full := ErrJoinRejectReasonLobbyFull.Error()
	violated := ErrJoinRejectReasonReservationViolated.Error()

	if !strings.HasPrefix(violated, full) {
		t.Fatalf("premise changed: %q is no longer prefixed by %q; this guard needs rewriting", violated, full)
	}

	if RejectReasonCode(full) == RejectReasonCode(violated) {
		t.Fatalf("prefix-sibling reasons collapsed to the same code %q", RejectReasonCode(full))
	}

	// The broken-promise reason must never route as ordinary contention, and
	// vice versa. This is the conflation #585 exists to end.
	if !errors.Is(JoinRejectReasonOf(violated), ErrJoinRejectReasonReservationViolated) {
		t.Error("the reservation-violated wire text did not route to the reservation-violated identity")
	}
	if errors.Is(JoinRejectReasonOf(violated), ErrJoinRejectReasonLobbyFull) {
		t.Error("the reservation-violated wire text routed to plain lobby-full")
	}
	if errors.Is(JoinRejectReasonOf(full), ErrJoinRejectReasonReservationViolated) {
		t.Error("the plain lobby-full wire text routed to reservation-violated")
	}

	// Generalised: for every ordered pair where one registered message is a
	// proper prefix of another, the longer one must resolve to itself.
	reasons := JoinRejectReasons()
	for _, short := range reasons {
		for _, long := range reasons {
			if short == long || !strings.HasPrefix(long.Error(), short.Error()) {
				continue
			}
			if got := RejectReasonCode(long.Error()); got != long.Code() {
				t.Errorf("%q is prefixed by %q and decoded to %q, want %q",
					long.Error(), short.Error(), got, long.Code())
			}
		}
	}

	// An unregistered string that merely *starts with* a registered message is
	// not that reason. Under prefix matching it would be.
	for _, extended := range []string{full + " and then some", violated + " (retrying)"} {
		if got := JoinRejectReasonOf(extended); got != nil {
			t.Errorf("JoinRejectReasonOf(%q) = %v; an extended string is not a registered reason", extended, got)
		}
		if got := RejectReasonCode(extended); got != RejectReasonUnknown {
			t.Errorf("RejectReasonCode(%q) = %q, want %q", extended, got, RejectReasonUnknown)
		}
	}
}

// TestJoinRejectReasonOf_UnregisteredResolvesToNil covers everything else that
// can arrive on the reason channel. MatchJoinAttempt overloads it: on accept it
// carries the joining presence as JSON, and two reject paths build a reason with
// fmt.Sprintf around dynamic data (the metadata unmarshal failure and the early
// quit penalty), which cannot be exact-matched and so stay unclassified —
// exactly as they were before this change. None of them may be mistaken for a
// registered reason.
func TestJoinRejectReasonOf_UnregisteredResolvesToNil(t *testing.T) {
	unregistered := []string{
		"",
		"failed to unmarshal metadata: unexpected end of JSON input",
		"early quit penalty active [exp: 5m0s]",
		`{"session_id":"00000000-0000-0000-0000-000000000000","role_alignment":1}`,
		"Lobby Full",
		"lobby",
		"not a reason at all",
	}

	for _, reason := range unregistered {
		if got := JoinRejectReasonOf(reason); got != nil {
			t.Errorf("JoinRejectReasonOf(%q) = %v, want nil", reason, got)
		}
		for _, r := range JoinRejectReasons() {
			if errors.Is(JoinRejectReasonOf(reason), r) {
				t.Errorf("unregistered reason %q routed to %q", reason, r.Code())
			}
		}
	}

	if got := RejectReasonCode(""); got != RejectReasonNone {
		t.Errorf(`RejectReasonCode("") = %q, want %q`, got, RejectReasonNone)
	}
	for _, reason := range unregistered[1:] {
		if got := RejectReasonCode(reason); got != RejectReasonUnknown {
			t.Errorf("RejectReasonCode(%q) = %q, want %q", reason, got, RejectReasonUnknown)
		}
	}
}

// TestJoinRejectOutcome_RoutesEveryRejection is the behaviour lock on the
// consumer side: the same MatchJoinAttempt result must produce the same
// client-visible error text, the same reject_reason code, and the same
// broken-promise severity input as before the typed decode replaced the string
// switch. The error strings here are written out in full rather than built from
// the sentinels, because they are what a player is shown.
func TestJoinRejectOutcome_RoutesEveryRejection(t *testing.T) {
	const label = `{"id":"deadbeef-0000-0000-0000-000000000000.node"}`

	tests := []struct {
		name         string
		found        bool
		allowed      bool
		reason       string
		labelStr     string
		wantErr      string // "" means no error: the join was accepted
		wantCode     string
		wantViolated bool
	}{
		{
			name:     "match not found outranks everything else",
			found:    false,
			reason:   ErrJoinRejectReasonLobbyFull.Error(),
			labelStr: label,
			wantErr:  "server does not exist: match not found",
			wantCode: RejectReasonMatchNotFound,
		},
		{
			name:     "empty label",
			found:    true,
			reason:   ErrJoinRejectReasonLobbyFull.Error(),
			labelStr: "",
			wantErr:  "server does not exist: match label empty",
			wantCode: RejectReasonMatchLabelEmpty,
		},
		{
			name:     "duplicate evr id",
			found:    true,
			reason:   ErrJoinRejectDuplicateEvrID.Error(),
			labelStr: label,
			wantErr:  "bad request: duplicate evr ID",
			wantCode: RejectReasonDuplicateEvrID,
		},
		{
			name:     "match closed",
			found:    true,
			reason:   ErrJoinRejectReasonMatchClosed.Error(),
			labelStr: label,
			wantErr:  "server is locked: match closed",
			wantCode: RejectReasonMatchClosed,
		},
		{
			name:         "reservation violated is a broken promise",
			found:        true,
			reason:       ErrJoinRejectReasonReservationViolated.Error(),
			labelStr:     label,
			wantErr:      "server is full: lobby full: reservation violated — lobby was over capacity despite a valid slot reservation",
			wantCode:     RejectReasonReservationViolated,
			wantViolated: true,
		},
		{
			name:     "lobby full is ordinary contention",
			found:    true,
			reason:   ErrJoinRejectReasonLobbyFull.Error(),
			labelStr: label,
			wantErr:  "server is full: join not allowed: lobby full",
			wantCode: RejectReasonLobbyFull,
		},
		{
			name:     "match terminating",
			found:    true,
			reason:   ErrJoinRejectReasonMatchTerminating.Error(),
			labelStr: label,
			wantErr:  "server is full: join not allowed: match terminating",
			wantCode: RejectReasonMatchTerminating,
		},
		{
			name:     "unassigned lobby",
			found:    true,
			reason:   ErrJoinRejectReasonUnassignedLobby.Error(),
			labelStr: label,
			wantErr:  "server is full: join not allowed: unassigned lobby",
			wantCode: RejectReasonUnassignedLobby,
		},
		{
			name:     "feature mismatch",
			found:    true,
			reason:   ErrJoinRejectReasonFeatureMismatch.Error(),
			labelStr: label,
			wantErr:  "server is full: join not allowed: feature mismatch",
			wantCode: RejectReasonFeatureMismatch,
		},
		{
			name:     "failed to assign team",
			found:    true,
			reason:   ErrJoinRejectReasonFailedToAssignTeam.Error(),
			labelStr: label,
			wantErr:  "server is full: join not allowed: failed to assign team",
			wantCode: RejectReasonFailedToAssignTeam,
		},
		{
			name:     "an unregistered reason still refuses, and classifies unknown",
			found:    true,
			reason:   "early quit penalty active [exp: 5m0s]",
			labelStr: label,
			wantErr:  "server is full: join not allowed: early quit penalty active [exp: 5m0s]",
			wantCode: RejectReasonUnknown,
		},
		{
			name:     "accepted join yields no error",
			found:    true,
			allowed:  true,
			reason:   `{"session_id":"00000000-0000-0000-0000-000000000000"}`,
			labelStr: label,
			wantErr:  "",
			wantCode: RejectReasonUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCode, gotViolated, gotErr := joinRejectOutcome(tt.found, tt.allowed, tt.reason, tt.labelStr)

			switch {
			case tt.wantErr == "" && gotErr != nil:
				t.Fatalf("expected no error, got %q", gotErr)
			case tt.wantErr != "" && gotErr == nil:
				t.Fatalf("expected error %q, got nil", tt.wantErr)
			case tt.wantErr != "" && gotErr.Error() != tt.wantErr:
				t.Errorf("client-visible error text changed:\n got %q\nwant %q", gotErr.Error(), tt.wantErr)
			}
			if gotCode != tt.wantCode {
				t.Errorf("reject_reason = %q, want %q", gotCode, tt.wantCode)
			}
			if gotViolated != tt.wantViolated {
				t.Errorf("reservation_violated = %v, want %v", gotViolated, tt.wantViolated)
			}
		})
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
