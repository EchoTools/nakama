package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/atomic"
	"go.uber.org/zap"
)

// Characterization of three join-path claims. These tests document behaviour as
// it is today; they deliberately do NOT fix anything. Two of the three claims
// are real defects and the tests below assert the DEFECTIVE behaviour, so each
// will fail the moment the defect is fixed -- which is the signal to update the
// test alongside the fix.

// --- shared harness ------------------------------------------------------

// charLiveSession is a Session that can be stored in a SessionRegistry and whose
// context carries SessionParameters, so production code that does
// sessions.Get(id) -> LoadParams(s.Context()) finds a real cache to refresh.
type charLiveSession struct {
	Session
	id     uuid.UUID
	userID uuid.UUID
	ctx    context.Context
}

func (s *charLiveSession) ID() uuid.UUID                { return s.id }
func (s *charLiveSession) UserID() uuid.UUID            { return s.userID }
func (s *charLiveSession) Context() context.Context     { return s.ctx }
func (s *charLiveSession) SetContext(c context.Context) { s.ctx = c }

// charRegisterLiveSession adds a live session for the given presence to the
// registry and returns the SessionParameters whose earlyQuitConfig cache
// production code is expected to refresh. The cache starts at a pristine
// (level 0, zero quits) state.
func charRegisterLiveSession(t *testing.T, registry SessionRegistry, p *EvrMatchPresence) *SessionParameters {
	t.Helper()

	params := &SessionParameters{
		earlyQuitConfig: atomic.NewPointer[EarlyQuitPlayerState](nil),
	}
	params.earlyQuitConfig.Store(NewEarlyQuitPlayerState())

	ptr := atomic.NewPointer(params)
	ctx := context.WithValue(context.Background(), ctxSessionParametersKey{}, ptr)

	registry.Add(&charLiveSession{id: p.SessionID, userID: p.UserID, ctx: ctx})
	return params
}

// charCachedPenaltyLevel reports the penalty level the session cache currently
// advertises. This is exactly the value evr_lobby_parameters.go reads to compute
// LobbySessionParameters.EarlyQuitPenaltyLevel, which in turn gates the storage
// read at evr_lobby_find.go:305.
func charCachedPenaltyLevel(params *SessionParameters) int {
	c := params.earlyQuitConfig.Load()
	if c == nil {
		return 0
	}
	return c.GetPenaltyLevel()
}

func charCachedQuitCount(params *SessionParameters) int32 {
	c := params.earlyQuitConfig.Load()
	if c == nil {
		return 0
	}
	return c.NumEarlyQuits
}

// charStoredEarlyQuit reads the persisted early-quit state.
func charStoredEarlyQuit(t *testing.T, ctx context.Context, nk runtime.NakamaModule, userID string) *EarlyQuitPlayerState {
	t.Helper()
	eq := NewEarlyQuitPlayerState()
	if err := StorableRead(ctx, nk, userID, eq, false); err != nil {
		t.Fatalf("StorableRead(EarlyQuit, %s): %v", userID, err)
	}
	return eq
}

// --- F14: earlyQuitConfig staleness --------------------------------------

// F14. The MatchLoop reservation-expiry charge persists an incremented
// early-quit state but never refreshes the live session's earlyQuitConfig
// cache, unlike the MatchLeave charge which does (evr_match.go:1069). The
// cached penalty level therefore stays 0 while storage says otherwise.
//
// The consequence is the gate at evr_lobby_find.go:305 --
//
//	if lobbyParams.Mode == evr.ModeArenaPublic && lobbyParams.EarlyQuitPenaltyLevel > 0 && ...
//
// -- whose EarlyQuitPenaltyLevel is computed from this cache
// (evr_lobby_parameters.go:448-455). With a cached 0 the fresh StorableRead on
// the next line is unreachable, so the lockout goes unenforced for the rest of
// the session.
//
// This is an A/B test: the same charge, once through MatchLeave and once
// through the MatchLoop expiry path, with an identical live session.
func TestEarlyQuitCache_MatchLoopReservationExpiry_LeavesSessionCacheStale(t *testing.T) {
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")

	// --- A: MatchLeave (the reference path) ---
	nkA, dbA := newChargeModule(t)
	playerA := reconnectTestPlayer("cache-matchleave", evr.TeamBlue)
	preseedEarlyQuitConfig(t, ctx, nkA, playerA.GetUserId())
	paramsA := charRegisterLiveSession(t, nkA.sessions, playerA)

	if got := charCachedQuitCount(paramsA); got != 0 {
		t.Fatalf("precondition: expected a pristine cache, got %d quits", got)
	}

	driveVoluntaryLeave(ctx, t, dbA, nkA, chargeState(evr.ModeArenaPublic, playerA), playerA)

	storedA := charStoredEarlyQuit(t, ctx, nkA, playerA.GetUserId())
	cachedA := charCachedQuitCount(paramsA)

	if storedA.NumEarlyQuits != 1 {
		t.Fatalf("MatchLeave: expected the charge to persist 1 quit, got %d", storedA.NumEarlyQuits)
	}
	if cachedA != storedA.NumEarlyQuits {
		t.Errorf("MatchLeave: session cache (%d) does not match storage (%d) -- the reference path regressed",
			cachedA, storedA.NumEarlyQuits)
	}
	t.Logf("MatchLeave path        : stored=%d cached=%d  (cache refreshed)", storedA.NumEarlyQuits, cachedA)

	// --- B: MatchLoop reconnect-reservation expiry (the path under test) ---
	nkB, _ := newChargeModule(t)
	playerB := reconnectTestPlayer("cache-matchloop", evr.TeamBlue)
	preseedEarlyQuitConfig(t, ctx, nkB, playerB.GetUserId())
	paramsB := charRegisterLiveSession(t, nkB.sessions, playerB)

	stateB := reconnectTestState(evr.ModeArenaPublic)
	stateB.ID.Node = "test-node"
	stateB.participations[playerB.GetUserId()] = &PlayerParticipation{
		UserID:      playerB.GetUserId(),
		Username:    playerB.Username,
		DisplayName: playerB.DisplayName,
		Team:        BlueTeam,
		JoinTime:    time.Now().Add(-2 * time.Minute),
		LeaveTime:   time.Now(),
	}
	stateB.reconnectReservations[playerB.GetUserId()] = &reconnectReservation{
		Presence:     playerB,
		Expiry:       time.Now().Add(-time.Second), // already expired
		UserID:       playerB.GetUserId(),
		DeferPenalty: true,
	}

	m := &EvrMatch{}
	if got := m.MatchLoop(ctx, reconnectTestLogger(), nil, nkB, &reconnectTestDispatcher{}, 1, stateB, nil); got == nil {
		t.Fatal("MatchLoop returned nil state")
	}

	storedB := charStoredEarlyQuit(t, ctx, nkB, playerB.GetUserId())
	cachedB := charCachedQuitCount(paramsB)

	if storedB.NumEarlyQuits != 1 {
		t.Fatalf("MatchLoop: expected the deferred charge to persist 1 quit, got %d", storedB.NumEarlyQuits)
	}

	// THE DEFECT: storage advanced, the session cache did not.
	if cachedB == storedB.NumEarlyQuits {
		t.Fatalf("MatchLoop path refreshed the session cache (cached=%d stored=%d) -- claim 14 is FALSE",
			cachedB, storedB.NumEarlyQuits)
	}
	if cachedB != 0 {
		t.Errorf("expected the stale cache to still read 0 quits, got %d", cachedB)
	}

	t.Logf("MatchLoop expiry path  : stored=%d cached=%d  (cache NOT refreshed)  <-- DEFECT",
		storedB.NumEarlyQuits, cachedB)
	t.Logf("cached penalty level advertised to evr_lobby_find.go:305 = %d", charCachedPenaltyLevel(paramsB))
	t.Logf("that gate is `EarlyQuitPenaltyLevel > 0`, so the fresh storage read never runs and the lockout is unenforced for this session")
}

// F14b. Pins the gate itself: a session whose cache reads level 0 produces
// EarlyQuitPenaltyLevel 0, which short-circuits evr_lobby_find.go:305 no matter
// what storage holds.
func TestEarlyQuitCache_StaleZeroLevel_ShortCircuitsTheLockoutGate(t *testing.T) {
	t.Parallel()

	params := &SessionParameters{
		earlyQuitConfig: atomic.NewPointer[EarlyQuitPlayerState](nil),
	}

	// A pristine (post-login, pre-charge) cache.
	params.earlyQuitConfig.Store(NewEarlyQuitPlayerState())
	if got := charCachedPenaltyLevel(params); got != 0 {
		t.Fatalf("expected a pristine cache to advertise level 0, got %d", got)
	}
	t.Logf("stale cache advertises level %d -> gate `EarlyQuitPenaltyLevel > 0` is FALSE -> no storage read, no enforcement",
		charCachedPenaltyLevel(params))

	// The same session after a charge that DID refresh the cache.
	charged := NewEarlyQuitPlayerState()
	charged.PenaltyLevel = 2
	charged.PenaltyTimestamp = time.Now().Add(10 * time.Minute).Unix()
	params.earlyQuitConfig.Store(charged)

	if got := charCachedPenaltyLevel(params); got != 2 {
		t.Fatalf("expected a refreshed cache to advertise level 2, got %d", got)
	}
	t.Logf("refreshed cache advertises level %d -> gate is TRUE -> storage read runs and the lockout is enforced",
		charCachedPenaltyLevel(params))
}

// --- F15: MatchJoinAttempt ordering --------------------------------------

// F15. The early-quit refusal in MatchJoinAttempt (evr_match.go:321-329) runs
// BEFORE the reconnect-reservation lookup (evr_match.go:351). A penalized player
// recovering from a crash is therefore refused, their reconnect reservation is
// never consumed, it expires in MatchLoop with DeferPenalty set, and they are
// charged a SECOND early quit for the same crash.
//
// The refusal is currently gated on isEarlyQuitEnforcementTestUser, so in
// production this fires for exactly one hardcoded account. This test uses that
// account to unmask the ordering, and a second subtest shows the masking.
func TestMatchJoinAttempt_PenalizedReconnect_RefusedThenChargedTwice(t *testing.T) {
	// ServiceSettingsUpdate mutates process-global state: not parallel-safe.
	original := ServiceSettings()
	defer ServiceSettingsUpdate(original)
	ServiceSettingsUpdate(&ServiceSettingsData{
		Matchmaking: GlobalMatchmakingSettings{EnableEarlyQuitPenalty: true},
	})

	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")

	// The one account the enforcement gate lets through (evr_earlyquit.go:265-267).
	const gatedUserID = "580230ee-3866-446f-8f3f-6cc68e3c8621"
	if !isEarlyQuitEnforcementTestUser(gatedUserID) {
		t.Fatalf("precondition: %s is no longer the early-quit enforcement test user", gatedUserID)
	}

	run := func(t *testing.T, userID string) (accepted bool, reason string, reservationSurvived bool, quitsAfter int32) {
		t.Helper()

		nk, _ := newChargeModule(t)

		player := reconnectTestPlayer("ordering-"+userID[:8], evr.TeamBlue)
		player.UserID = uuid.FromStringOrNil(userID)

		// The player already carries an ACTIVE lockout from an earlier quit.
		if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
			Collection: StorageCollectionEarlyQuit,
			Key:        StorageKeyEarlyQuit,
			UserID:     player.GetUserId(),
			Value: `{"num_early_quits":2,"num_steady_early_quits":2,"matchmaking_tier":1,` +
				`"penalty_level":1,"penalty_ts":` + itoa(time.Now().Add(10*time.Minute).Unix()) + `}`,
			PermissionRead:  int(runtime.STORAGE_PERMISSION_NO_READ),
			PermissionWrite: int(runtime.STORAGE_PERMISSION_NO_WRITE),
		}}); err != nil {
			t.Fatalf("preseed: %v", err)
		}

		// They crashed out of a match; a reconnect reservation is holding their
		// seat with the penalty deferred.
		state := reconnectTestState(evr.ModeArenaPublic)
		state.ID.Node = "test-node"
		state.participations[player.GetUserId()] = &PlayerParticipation{
			UserID:      player.GetUserId(),
			Username:    player.Username,
			DisplayName: player.DisplayName,
			Team:        BlueTeam,
			JoinTime:    time.Now().Add(-2 * time.Minute),
			LeaveTime:   time.Now(),
		}
		state.reconnectReservations[player.GetUserId()] = &reconnectReservation{
			Presence:     player,
			Expiry:       time.Now().Add(time.Minute), // still live
			UserID:       player.GetUserId(),
			DeferPenalty: true,
		}
		state.rebuildCache()

		m := &EvrMatch{}
		meta := EntrantMetadata{Presence: player}
		gotState, ok, why := m.MatchJoinAttempt(ctx, reconnectTestLogger(), nil, nk, nil, 10,
			state, player, meta.ToMatchMetadata())

		label, isLabel := gotState.(*MatchLabel)
		if !isLabel {
			t.Fatalf("MatchJoinAttempt returned non-*MatchLabel: %T", gotState)
		}

		_, survived := label.reconnectReservations[player.GetUserId()]

		// Now let the (unconsumed) reservation expire in MatchLoop.
		if rr, present := label.reconnectReservations[player.GetUserId()]; present {
			rr.Expiry = time.Now().Add(-time.Second)
		}
		if got := m.MatchLoop(ctx, reconnectTestLogger(), nil, nk, &reconnectTestDispatcher{}, 1, label, nil); got == nil {
			t.Fatal("MatchLoop returned nil state")
		}

		return ok, why, survived, charStoredEarlyQuit(t, ctx, nk, player.GetUserId()).NumEarlyQuits
	}

	t.Run("gated user: refused, reservation unconsumed, charged a second time", func(t *testing.T) {
		accepted, reason, survived, quits := run(t, gatedUserID)

		if accepted {
			t.Fatalf("expected the penalized reconnect to be REFUSED, but it was accepted (reason=%q)", reason)
		}
		if !strings.Contains(reason, "early quit penalty active") {
			t.Errorf("expected an early-quit refusal reason, got %q", reason)
		}
		if !survived {
			t.Errorf("expected the reconnect reservation to survive the refusal (never consumed), but it was gone")
		}
		if quits != 3 {
			t.Errorf("expected a SECOND charge for the same crash (2 -> 3), got %d", quits)
		}

		t.Logf("join      : accepted=%v reason=%q", accepted, reason)
		t.Logf("reservation survived the refusal: %v (so it later expires with DeferPenalty)", survived)
		t.Logf("early quits: 2 before the reconnect attempt -> %d after  <-- DOUBLE CHARGE", quits)
	})

	t.Run("any other user: refusal is masked by the test-user gate", func(t *testing.T) {
		accepted, reason, survived, quits := run(t, uuid.Must(uuid.NewV4()).String())

		if !accepted {
			t.Fatalf("expected a non-gated user's reconnect to be accepted, got refusal %q", reason)
		}
		if survived {
			t.Errorf("expected an accepted reconnect to CONSUME its reservation, but it survived")
		}
		if quits != 2 {
			t.Errorf("expected no additional charge for a consumed reservation, got %d quits", quits)
		}

		t.Logf("join      : accepted=%v", accepted)
		t.Logf("early quits: unchanged at %d -- the ordering defect is latent for everyone but the gated account", quits)
	})
}

// itoa avoids pulling strconv in for one call site in a test literal.
func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// --- F16: party-member authorization -------------------------------------

// F16. Two halves, and they point opposite ways.
//
// The LITERAL claim is TRUE: LobbyJoinEntrants performs exactly one suspension
// check, against the `session` parameter (entrants[0]'s session); entrants[1:]
// are never examined. enforceJoinSuspension takes no entrants at all.
//
// The EXPLOIT claim is FALSE: entrants[1:] only receive slot RESERVATIONS, not
// seats. Every entrant that actually occupies a seat re-enters through its own
// LobbyJoinEntrants call as entrants[0] with its own session, and is checked
// then. A reservation is not authorization.
func TestLobbyJoinEntrants_SuspendedPartyMember_CannotRideOnLeaderAuthorization(t *testing.T) {
	t.Parallel()

	groupID := uuid.Must(uuid.NewV4()).String()
	leaderID := uuid.Must(uuid.NewV4()).String()
	suspendedFollowerID := uuid.Must(uuid.NewV4()).String()

	nk := newSeatTestNK()
	writeSuspension(t, nk, suspendedFollowerID, groupID, time.Now().Add(24*time.Hour), "follower is banned")

	ggReg := seatTestGuildGroupRegistry(map[string]*GuildGroup{
		groupID: seatTestGuildGroup(groupID, "TestGuild", false),
	})
	label := makeLabel(groupID, evr.ModeArenaPublic)

	// (a) The leader's call authorizes the LEADER only. The suspended follower
	//     travels as entrants[1:], which this check never inspects.
	leaderSession := newSeatTestSession(uuid.FromStringOrNil(leaderID), []string{leaderID})
	if err := enforceJoinSuspension(context.Background(), zap.NewNop(), nk, ggReg, label, leaderSession); err != nil {
		t.Fatalf("expected the clean leader to be authorized, got: %v", err)
	}
	t.Logf("(a) leader's LobbyJoinEntrants call: ALLOWED -- and it checked only the leader")

	// (b) The suspended follower's own redemption IS checked, and refused.
	//     This is what closes the hole: the reservation must be redeemed by a
	//     separately authorized join on the follower's own session.
	followerSession := newSeatTestSession(uuid.FromStringOrNil(suspendedFollowerID), []string{suspendedFollowerID})
	err := enforceJoinSuspension(context.Background(), zap.NewNop(), nk, ggReg, label, followerSession)
	if err == nil {
		t.Fatal("EXPLOIT CONFIRMED: the suspended follower's own join was allowed -- claim 16 would be REAL")
	}
	if lobbyErr, ok := err.(LobbyError); !ok || lobbyErr.Code() != KickedFromLobbyGroup {
		t.Fatalf("expected KickedFromLobbyGroup, got %T: %v", err, err)
	}
	t.Logf("(b) follower's own join call      : REFUSED (%v)", err)
	t.Logf("=> the suspended member gets a seat HELD, then is rejected when they sit in it")
	t.Logf("=> claim 16's exploit is FALSE; the literal 'only entrants[0] is checked' is TRUE but not exploitable")
}

// F16b. enforceJoinSuspension derives its subject solely from the session, so a
// party's follower list cannot influence the leader's verdict in either
// direction. This pins the narrow invariant that actually holds.
func TestEnforceJoinSuspension_SubjectComesOnlyFromTheSession(t *testing.T) {
	t.Parallel()

	groupID := uuid.Must(uuid.NewV4()).String()
	cleanID := uuid.Must(uuid.NewV4()).String()
	bannedID := uuid.Must(uuid.NewV4()).String()

	nk := newSeatTestNK()
	writeSuspension(t, nk, bannedID, groupID, time.Now().Add(24*time.Hour), "banned")

	ggReg := seatTestGuildGroupRegistry(map[string]*GuildGroup{
		groupID: seatTestGuildGroup(groupID, "TestGuild", false),
	})
	label := makeLabel(groupID, evr.ModeArenaPublic)

	// A banned user exists in storage, but a clean session is unaffected by it
	// unless that banned user is in the session's OWN enforcementUserIDs (alts).
	clean := newSeatTestSession(uuid.FromStringOrNil(cleanID), []string{cleanID})
	if err := enforceJoinSuspension(context.Background(), zap.NewNop(), nk, ggReg, label, clean); err != nil {
		t.Errorf("a clean session was affected by an unrelated banned user: %v", err)
	}

	banned := newSeatTestSession(uuid.FromStringOrNil(bannedID), []string{bannedID})
	if err := enforceJoinSuspension(context.Background(), zap.NewNop(), nk, ggReg, label, banned); err == nil {
		t.Error("the banned session was allowed")
	}

	t.Logf("the verdict tracks session.UserID() + params.enforcementUserIDs, nothing else")
}
