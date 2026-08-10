package server

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap/zapcore"
)

// TestMatchJoinAttempt_CreateReservationHoldsSlotAgainstBackfill is the RESV-1
// integration test: it drives the real MatchJoinAttempt capacity gate to prove
// that a slot reserved for an online party member at /create time is held
// against backfill, and can still be claimed by the party member even though
// their eventual match-connection session ID differs from the placeholder
// session captured when the reservation was made (getOnlinePartyReservations
// resolves party members via their *login*-service presence, since they have
// not started their own match connection yet -- see evr_runtime.go).
//
// The reservation is installed exactly the way SignalPrepareSession installs
// settings.Reservations into state.reservationMap (evr_match.go, SignalPrepareSession
// case, "for _, e := range settings.Reservations"), so this test exercises the
// same production conversion that /create's new getOnlinePartyReservations
// output feeds into MatchSettings.Reservations.
func TestMatchJoinAttempt_CreateReservationHoldsSlotAgainstBackfill(t *testing.T) {
	state := newSocialTestMatchLabel()
	state.Mode = evr.ModeSocialPublic
	state.LobbyType = PublicLobby
	// A tiny 2-slot lobby makes the backfill-blocking behavior unambiguous:
	// creator (1) + reservation (1) == full, with zero slack for a backfill join.
	state.MaxSize = 2
	state.PlayerLimit = 2

	partyID := uuid.Must(uuid.NewV4())
	creatorUserID := uuid.Must(uuid.NewV4())
	creatorSessionID := uuid.Must(uuid.NewV4())

	followerUserID := uuid.Must(uuid.NewV4())
	// followerLoginSessionID stands in for the placeholder session ID
	// getOnlinePartyReservations captures from the follower's login-service
	// presence at /create time -- NOT the session they'll actually join with.
	followerLoginSessionID := uuid.Must(uuid.NewV4())
	followerMatchSessionID := uuid.Must(uuid.NewV4())

	backfillUserID := uuid.Must(uuid.NewV4())
	backfillSessionID := uuid.Must(uuid.NewV4())

	// --- Step 1: simulate /create's SignalPrepareSession, which converts
	// MatchSettings.Reservations (produced by getOnlinePartyReservations) into
	// state.reservationMap. This is the exact loop body from evr_match.go's
	// SignalPrepareSession case.
	settingsReservations := []*EvrMatchPresence{
		{
			Node:          "testnode",
			SessionID:     followerLoginSessionID,
			UserID:        followerUserID,
			Username:      "follower",
			PartyID:       partyID,
			RoleAlignment: evr.TeamSocial,
		},
	}
	reservationLifetime := 45 * time.Second
	for _, e := range settingsReservations {
		state.reservationMap[e.GetSessionId()] = &slotReservation{
			Presence: e,
			Expiry:   time.Now().Add(reservationLifetime),
		}
	}
	state.rebuildCache()

	if state.OpenSlots() != 1 {
		t.Fatalf("expected 1 open slot before the creator joins (reservation holds the other), got OpenSlots()=%d", state.OpenSlots())
	}

	m := &EvrMatch{}
	ctx := context.Background()
	logger := NewRuntimeGoLogger(NewJSONLogger(os.Stdout, zapcore.ErrorLevel, JSONFormat))
	nk := &reconnectTestNakamaModule{}
	disp := &reconnectTestDispatcher{}

	// --- Step 2: the creator joins directly (no reservation needed -- they win
	// the normal join race), taking the lobby's one remaining open slot and
	// bringing total occupancy (1 actual + 1 reservation) to the 2-slot cap.
	creatorPresence := &EvrMatchPresence{
		Node:          "testnode",
		SessionID:     creatorSessionID,
		UserID:        creatorUserID,
		EvrID:         evr.EvrId{PlatformCode: 4, AccountId: 1},
		Username:      "creator",
		PartyID:       partyID,
		RoleAlignment: evr.TeamSocial,
		SessionExpiry: 9999999999,
	}
	creatorMeta := NewJoinMetadata(creatorPresence)
	resultState, allowed, reason := m.MatchJoinAttempt(ctx, logger, nil, nk, disp, 0, state, creatorPresence, creatorMeta.ToMatchMetadata())
	if !allowed {
		t.Fatalf("expected creator join to succeed, got rejected: %s", reason)
	}
	state = resultState.(*MatchLabel)

	if state.OpenSlots() != 0 {
		t.Fatalf("expected the lobby to be at capacity (creator + reservation), got OpenSlots()=%d", state.OpenSlots())
	}

	// --- Step 3: an unrelated backfill player attempts to join. The reservation
	// must hold the slot -- this is the crux of RESV-1: today's bug is that
	// nothing ever populates settings.Reservations, so a match like this would
	// have zero reservations and this backfill join would incorrectly succeed.
	backfillPresence := &EvrMatchPresence{
		Node:          "testnode",
		SessionID:     backfillSessionID,
		UserID:        backfillUserID,
		EvrID:         evr.EvrId{PlatformCode: 4, AccountId: 999},
		Username:      "backfiller",
		RoleAlignment: evr.TeamSocial,
		SessionExpiry: 9999999999,
	}
	backfillMeta := NewJoinMetadata(backfillPresence)

	resultState, allowed, reason = m.MatchJoinAttempt(ctx, logger, nil, nk, disp, 0, state, backfillPresence, backfillMeta.ToMatchMetadata())
	if allowed {
		t.Fatalf("expected backfill join to be rejected while the party reservation holds the slot, got allowed with reason=%q", reason)
	}
	if reason != ErrJoinRejectReasonLobbyFull.Error() {
		t.Fatalf("expected lobby-full rejection for backfill join, got: %s", reason)
	}
	state = resultState.(*MatchLabel)

	if _, exists := state.reservationMap[followerLoginSessionID.String()]; !exists {
		t.Fatal("reservation must survive a rejected backfill join attempt")
	}

	// --- Step 4: the reserved party member joins with their real match-connection
	// session (different from the login-session placeholder). The reservation must
	// be found via the user-ID fallback (LoadAndDeleteReservationByUserIDRaw) and
	// consumed, freeing the slot the total-capacity gate would otherwise deny.
	followerPresence := &EvrMatchPresence{
		Node:          "testnode",
		SessionID:     followerMatchSessionID,
		UserID:        followerUserID,
		EvrID:         evr.EvrId{PlatformCode: 4, AccountId: 2},
		Username:      "follower",
		PartyID:       partyID,
		RoleAlignment: evr.TeamSocial,
		SessionExpiry: 9999999999,
	}
	followerMeta := NewJoinMetadata(followerPresence)
	resultState, allowed, reason = m.MatchJoinAttempt(ctx, logger, nil, nk, disp, 0, state, followerPresence, followerMeta.ToMatchMetadata())
	if !allowed {
		t.Fatalf("expected reserved party member to join by consuming their reservation, got rejected: %s", reason)
	}
	state = resultState.(*MatchLabel)

	if _, exists := state.reservationMap[followerLoginSessionID.String()]; exists {
		t.Error("reservation should have been consumed once the reserved member actually joined")
	}
	if _, joined := state.presenceMap[followerMatchSessionID.String()]; !joined {
		t.Error("follower should now be a real presence in the match")
	}
}
