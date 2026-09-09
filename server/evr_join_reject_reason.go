package server

import (
	"fmt"
	"slices"
	"strings"
)

// Stable identities for a refused join. These are the values log consumers
// classify on: they are the `reject_reason` field on `failed to join match`,
// and they never change when a player-facing message is reworded. See #585
// proposal 4.
//
// RejectReasonMatchNotFound and RejectReasonMatchLabelEmpty have no wire
// message and so no registered reason: LobbyJoinEntrants derives them from
// JoinAttempt's `found` and `labelStr` returns, which are separate channels
// from `reason`.
const (
	RejectReasonUnknown               = "unknown"
	RejectReasonNone                  = ""
	RejectReasonLobbyFull             = "lobby_full"
	RejectReasonReservationViolated   = "reservation_violated"
	RejectReasonMatchClosed           = "match_closed"
	RejectReasonMatchTerminating      = "match_terminating"
	RejectReasonDuplicateJoin         = "duplicate_join"
	RejectReasonDuplicateEvrID        = "duplicate_evr_id"
	RejectReasonUnassignedLobby       = "unassigned_lobby"
	RejectReasonFeatureMismatch       = "feature_mismatch"
	RejectReasonFailedToAssignTeam    = "failed_to_assign_team"
	RejectReasonPartyMembersNeedRoles = "party_members_must_have_roles"
	RejectReasonMatchNotFound         = "match_not_found"
	RejectReasonMatchLabelEmpty       = "match_label_empty"
)

// JoinRejectReason is the typed identity of a refused join — the second half of
// #585 proposal 4, which asked for "typed sentinels compared with errors.Is".
//
// The wire is not ours to change. Nakama's runtime.Match interface fixes
// MatchJoinAttempt's third return as a plain string (nakama-common v1.44.1,
// runtime/runtime.go:953), and pipeline_match.go forwards that string to the
// client verbatim as the MATCH_JOIN_REJECTED envelope message. So the reason
// crossing the boundary is simultaneously an upstream interface AND
// player-visible text: it must stay a string, and it must stay these exact
// bytes.
//
// What IS ours is the identity at each end of it. A JoinRejectReason pairs the
// wire message with a stable, log-facing code in one literal, and registers
// both in a closed set at package init. The producer puts .Error() on the wire
// exactly as it always did; the consumer decodes once, at the boundary, with a
// single exact map lookup, and routes on the result with errors.Is.
//
// That is what retires the prefix hazard #585 names, rather than merely testing
// against it. "lobby full" is still a prefix of "lobby full: reservation
// violated" — it has to be, both are player-visible — but no code path compares
// a reason message with ==, strings.HasPrefix or strings.Contains any more. The
// only string operation left is a Go map index, which cannot partially match,
// and every decision downstream of it is pointer identity through errors.Is.
// There is no comparison left that a future edit could loosen into a prefix
// match.
//
// Registration also closes the other hole: newJoinRejectReason requires a code,
// so a new reject reason cannot reach production unclassified the way it could
// when the mapping was a switch somebody had to remember to extend.
type JoinRejectReason struct {
	code    string
	message string
}

// Error returns the wire message byte-for-byte. This is what MatchJoinAttempt
// puts on its string return and what the player is ultimately shown, so it is
// pinned by TestJoinRejectReason_CodeAndWireTextArePinned.
func (r *JoinRejectReason) Error() string { return r.message }

// Code returns the stable log-facing identity. It is decoupled from the message
// on purpose: rewording the message must never move an alert.
func (r *JoinRejectReason) Code() string { return r.code }

var (
	joinRejectReasonsByMessage = make(map[string]*JoinRejectReason)
	joinRejectReasonsByCode    = make(map[string]*JoinRejectReason)
)

// newJoinRejectReason registers one reject identity and returns it.
//
// It panics on an empty or unknown code, an empty message, or a duplicate of
// either — at package init, so the failure is immediate and total rather than a
// misrouted alert months later. Two reasons sharing a message would make the
// decode ambiguous; two sharing a code would silently merge two distinct events
// in every log query.
func newJoinRejectReason(code, message string) *JoinRejectReason {
	switch {
	case code == "" || code == RejectReasonUnknown:
		panic(fmt.Sprintf("join reject reason %q: code must be a stable value other than %q", message, RejectReasonUnknown))
	case message == "":
		panic(fmt.Sprintf("join reject reason %q: message must be non-empty; it is the wire text", code))
	}
	if prev, dup := joinRejectReasonsByCode[code]; dup {
		panic(fmt.Sprintf("duplicate join reject code %q: already held by %q, cannot also hold %q", code, prev.message, message))
	}
	if prev, dup := joinRejectReasonsByMessage[message]; dup {
		panic(fmt.Sprintf("duplicate join reject message %q: already held by code %q, cannot also hold %q", message, prev.code, code))
	}

	r := &JoinRejectReason{code: code, message: message}
	joinRejectReasonsByCode[code] = r
	joinRejectReasonsByMessage[message] = r
	return r
}

// The closed set of reject identities MatchJoinAttempt can put on the wire.
// Code and message are declared as one literal pair so they cannot drift apart.
var (
	ErrJoinRejectReasonUnassignedLobby           = newJoinRejectReason(RejectReasonUnassignedLobby, "unassigned lobby")
	ErrJoinRejectReasonDuplicateJoin             = newJoinRejectReason(RejectReasonDuplicateJoin, "duplicate join")
	ErrJoinRejectDuplicateEvrID                  = newJoinRejectReason(RejectReasonDuplicateEvrID, "duplicate evr id")
	ErrJoinRejectReasonLobbyFull                 = newJoinRejectReason(RejectReasonLobbyFull, "lobby full")
	ErrJoinRejectReasonReservationViolated       = newJoinRejectReason(RejectReasonReservationViolated, "lobby full: reservation violated")
	ErrJoinRejectReasonFailedToAssignTeam        = newJoinRejectReason(RejectReasonFailedToAssignTeam, "failed to assign team")
	ErrJoinRejectReasonPartyMembersMustHaveRoles = newJoinRejectReason(RejectReasonPartyMembersNeedRoles, "party members must have roles")
	ErrJoinRejectReasonMatchTerminating          = newJoinRejectReason(RejectReasonMatchTerminating, "match terminating")
	ErrJoinRejectReasonMatchClosed               = newJoinRejectReason(RejectReasonMatchClosed, "match closed to new entrants")
	ErrJoinRejectReasonFeatureMismatch           = newJoinRejectReason(RejectReasonFeatureMismatch, "feature mismatch")
)

// JoinRejectReasonOf resolves the free-text reason that crossed the
// MatchJoinAttempt boundary back to its typed identity, so callers can route
// with errors.Is instead of comparing strings.
//
// It returns a nil error — a true nil interface, safe to hand straight to
// errors.Is — for anything not in the closed set. That is not a failure: the
// same channel also carries the joining presence as JSON on accept, and two
// reject paths build their reason with fmt.Sprintf around dynamic data (the
// metadata unmarshal failure and the early-quit penalty), which cannot be
// exact-matched. Those classify as RejectReasonUnknown, exactly as they did
// before.
//
// The lookup is a map index. It is exact by construction, not by convention.
func JoinRejectReasonOf(reason string) error {
	if r, ok := joinRejectReasonsByMessage[reason]; ok {
		return r
	}
	return nil
}

// RejectReasonCode maps the free-text reason that crossed the boundary to the
// stable code log consumers classify on.
func RejectReasonCode(reason string) string {
	if reason == "" {
		return RejectReasonNone
	}
	if r, ok := joinRejectReasonsByMessage[reason]; ok {
		return r.code
	}
	return RejectReasonUnknown
}

// JoinRejectReasons returns every registered reject identity, ordered by code.
// It exists so a test can enumerate the closed set rather than restate it.
func JoinRejectReasons() []*JoinRejectReason {
	out := make([]*JoinRejectReason, 0, len(joinRejectReasonsByCode))
	for _, r := range joinRejectReasonsByCode {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b *JoinRejectReason) int { return strings.Compare(a.code, b.code) })
	return out
}
