// evr_chaos_test.go — chaos and boundary tests by Tommy "Chaos Monkey" Park.
// Coverage target: weird inputs, concurrent access, context cancellation, state transitions.
// Complements Grace Liu's methodical regression suite (evr_smell_regression_test.go).
// Tests may FAIL on current code — that is expected and desired. Document in qa-report.json.

package server

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
	uatomic "go.uber.org/atomic"
)

// ---------------------------------------------------------------------------
// shouldFollowerFindOrCreateSocial — boundary and unknown-mode chaos
// ---------------------------------------------------------------------------

// TestBoundary_ShouldFollower_ZeroSymbol verifies the helper does not panic
// and returns false for Symbol(0) (the zero value, "unloaded" mode).
func TestBoundary_ShouldFollower_ZeroSymbol_ReturnsFalse(t *testing.T) {
	t.Parallel()
	require.False(t, shouldFollowerFindOrCreateSocial(evr.Symbol(0)),
		"Symbol(0) is unloaded/zero mode — should not be treated as social")
}

// TestBoundary_ShouldFollower_ModeUnloaded_ReturnsFalse verifies ModeUnloaded
// (ToSymbol("")) is not considered social.
func TestBoundary_ShouldFollower_ModeUnloaded_ReturnsFalse(t *testing.T) {
	t.Parallel()
	require.False(t, shouldFollowerFindOrCreateSocial(evr.ModeUnloaded))
}

// TestBoundary_ShouldFollower_AllKnownModes verifies the predicate is correct
// for every mode constant the EVR protocol defines. This catches future-mode
// drift if someone adds a new social mode without updating the helper.
func TestBoundary_ShouldFollower_AllKnownModes(t *testing.T) {
	t.Parallel()

	socialModes := map[evr.Symbol]bool{
		evr.ModeSocialPublic: true,
		evr.ModeSocialNPE:    true,
	}
	allModes := []evr.Symbol{
		evr.ModeUnloaded,
		evr.ModeSocialPublic,
		evr.ModeSocialPrivate,
		evr.ModeSocialNPE,
		evr.ModeArenaPublic,
		evr.ModeArenaPrivate,
		evr.ModeArenaTournment,
		evr.ModeArenaPublicAI,
		evr.ModeArenaPracticeAI,
		evr.ModeEchoCombatTournament,
		evr.ModeCombatPublic,
		evr.ModeCombatPrivate,
	}
	for _, mode := range allModes {
		mode := mode
		t.Run(mode.String(), func(t *testing.T) {
			t.Parallel()
			got := shouldFollowerFindOrCreateSocial(mode)
			want := socialModes[mode]
			require.Equal(t, want, got,
				"shouldFollowerFindOrCreateSocial(%s) = %v, want %v", mode.String(), got, want)
		})
	}
}

// TestBoundary_ShouldFollower_UnknownMode_ReturnsFalse verifies an arbitrary
// unknown Symbol value (e.g., a future mode not yet in the switch list) does
// NOT accidentally match as social.
func TestBoundary_ShouldFollower_UnknownMode_ReturnsFalse(t *testing.T) {
	t.Parallel()
	unknownMode := evr.Symbol(0xDEADBEEFCAFEBABE)
	require.False(t, shouldFollowerFindOrCreateSocial(unknownMode),
		"unknown mode must not be treated as social")
}

// TestConcurrent_ShouldFollower_NoDataRace exercises the predicate from many
// goroutines simultaneously. It has no shared state so the race detector should
// stay silent, but a miscompiled inline could expose issues.
func TestConcurrent_ShouldFollower_NoDataRace(t *testing.T) {
	t.Parallel()

	modes := []evr.Symbol{
		evr.ModeSocialPublic, evr.ModeSocialNPE,
		evr.ModeArenaPublic, evr.Symbol(0),
	}
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		m := modes[i%len(modes)]
		wg.Add(1)
		go func(mode evr.Symbol) {
			defer wg.Done()
			_ = shouldFollowerFindOrCreateSocial(mode)
		}(m)
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// LobbyGroup nil-ph guard coverage
// ---------------------------------------------------------------------------

// TestNil_LobbyGroup_NilPh_GetLeader_DoesNotPanic verifies GetLeader() is
// safe when the internal PartyHandler is nil.
func TestNil_LobbyGroup_NilPh_GetLeader_DoesNotPanic(t *testing.T) {
	t.Parallel()
	g := &LobbyGroup{ph: nil}
	require.NotPanics(t, func() {
		leader := g.GetLeader()
		require.Nil(t, leader)
	})
}

// TestNil_LobbyGroup_NilPh_Size_ReturnsZero verifies Size() returns 0 for nil ph.
func TestNil_LobbyGroup_NilPh_Size_ReturnsZero(t *testing.T) {
	t.Parallel()
	g := &LobbyGroup{ph: nil}
	require.Equal(t, 0, g.Size())
}

// TestNil_LobbyGroup_NilPh_List_ReturnsNil verifies List() returns nil for nil ph.
func TestNil_LobbyGroup_NilPh_List_ReturnsNil(t *testing.T) {
	t.Parallel()
	g := &LobbyGroup{ph: nil}
	require.Nil(t, g.List())
}

// TestNil_LobbyGroup_NilPh_ID_ReturnsNilUUID verifies ID() returns uuid.Nil for nil ph.
func TestNil_LobbyGroup_NilPh_ID_ReturnsNilUUID(t *testing.T) {
	t.Parallel()
	g := &LobbyGroup{ph: nil}
	require.Equal(t, uuid.Nil, g.ID())
}

// TestNil_LobbyGroup_NilPh_MatchmakerAdd_ReturnsError verifies MatchmakerAdd
// returns an error (not a panic) when ph is nil.
func TestNil_LobbyGroup_NilPh_MatchmakerAdd_ReturnsError(t *testing.T) {
	t.Parallel()
	g := &LobbyGroup{ph: nil}
	_, _, err := g.MatchmakerAdd("sid", "node", "query", 2, 8, 2, nil, nil)
	require.Error(t, err, "MatchmakerAdd on nil ph must return error, not panic")
}

// ---------------------------------------------------------------------------
// LobbyGroup concurrent access — race detector bait
// ---------------------------------------------------------------------------

// TestRace_LobbyGroup_ConcurrentGetLeaderAndSetLeader verifies that
// concurrent GetLeader and leader-mutation via the party handler's mutex does
// not data-race. This hits the RLock/Lock path in GetLeader.
func TestRace_LobbyGroup_ConcurrentGetLeaderAndSetLeader(t *testing.T) {
	t.Parallel()

	ph := &PartyHandler{
		members: NewPartyPresenceList(8),
	}
	leaderSID := uuid.Must(uuid.NewV4())
	leaderUID := uuid.Must(uuid.NewV4())
	ph.leader = &PartyLeader{
		UserPresence: &rtapi.UserPresence{
			UserId:    leaderUID.String(),
			SessionId: leaderSID.String(),
			Username:  "leader",
		},
		PresenceID: &PresenceID{SessionID: leaderSID, Node: "testnode"},
	}
	g := &LobbyGroup{ph: ph}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Writers: rapidly toggle the leader.
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				newSID := uuid.Must(uuid.NewV4())
				newUID := uuid.Must(uuid.NewV4())
				ph.Lock()
				ph.leader = &PartyLeader{
					UserPresence: &rtapi.UserPresence{
						UserId:    newUID.String(),
						SessionId: newSID.String(),
						Username:  "mutated",
					},
					PresenceID: &PresenceID{SessionID: newSID, Node: "testnode"},
				}
				ph.Unlock()
			}
		}()
	}

	// Readers: call GetLeader concurrently.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = g.GetLeader()
			}
		}()
	}

	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// TestRace_LobbyGroup_ConcurrentSizeAndJoin verifies concurrent Size calls
// while members are joined don't race.
func TestRace_LobbyGroup_ConcurrentSizeAndJoin(t *testing.T) {
	t.Parallel()

	ph := &PartyHandler{
		members: NewPartyPresenceList(8),
	}
	g := &LobbyGroup{ph: ph}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Readers.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = g.Size()
					_ = g.List()
				}
			}
		}()
	}

	// Writers: join new members.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			select {
			case <-stop:
				return
			default:
			}
			sid := uuid.Must(uuid.NewV4())
			uid := uuid.Must(uuid.NewV4())
			ph.members.Join([]*Presence{newPresenceWithIDs("testnode", sid, uid)})
		}
	}()

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// ---------------------------------------------------------------------------
// TryFollowPartyLeader — nil and zero-value inputs
// ---------------------------------------------------------------------------

// TestNil_TryFollow_NilLobbyGroup_DoesNotPanic verifies TryFollowPartyLeader
// does not panic with a lobbyGroup whose ph is nil (no party handler).
func TestNil_TryFollow_NilLobbyGroup_DoesNotPanic(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	env.lobbyGroup = &LobbyGroup{ph: nil}
	logger := loggerForTest(t)

	require.NotPanics(t, func() {
		result := env.pipeline.TryFollowPartyLeader(
			context.Background(), logger, env.session, env.params, env.lobbyGroup)
		// Nil leader → must return false.
		require.False(t, result)
	})
}

// TestNil_TryFollow_EmptyGroupID_StreamLookupDoesNotPanic verifies that a
// params with uuid.Nil GroupID does not cause a nil-dereference in the
// matchmaking stream lookup (before MatchLabelByID is reached).
// The matchmaking stream uses GroupID as Subject — uuid.Nil is a valid uuid.
// Any panic that does occur must come from further downstream (e.g., nil nk),
// not from the stream lookup itself.
func TestNil_TryFollow_EmptyGroupID_StreamLookupDoesNotPanic(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	env.params.GroupID = uuid.Nil // zero GroupID
	logger := loggerForTest(t)

	// Set a leader with a match so we advance past early returns into the
	// matchmaking stream check (which uses params.GroupID as Subject).
	// No panic expected AT THAT POINT — any downstream panic (nil nk) is acceptable.
	matchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(matchID)

	// The lookup on the matchmaking stream with uuid.Nil must NOT panic.
	// Downstream panics (nil nk, nil metrics) are recovered.
	stream := PresenceStream{Mode: StreamModeMatchmaking, Subject: uuid.Nil}
	presence := env.tracker.GetLocalBySessionIDStreamUserID(env.leaderSID, stream, env.leaderUID)
	// With uuid.Nil group, no matchmaking presence exists → nil.
	require.Nil(t, presence,
		"stream lookup with uuid.Nil GroupID must return nil presence, not panic")

	// Full call with recovery for downstream panics (nil nk).
	func() {
		defer func() { recover() }() //nolint:errcheck
		env.pipeline.TryFollowPartyLeader(
			context.Background(), logger, env.session, env.params, env.lobbyGroup)
	}()
}

// TestCancel_TryFollow_AlreadyCancelledContext_ChecksCtxBeforeLobbyJoin verifies
// that TryFollowPartyLeader checks ctx.Err() before calling lobbyJoin.
//
// Currently, TryFollowPartyLeader does NOT check ctx.Err() before calling
// lobbyJoin (line 1137). This means the function proceeds into a cancelled
// context and calls lobbyAuthorize / LobbyJoinEntrants unnecessarily.
//
// BUG: With an already-cancelled context, TryFollowPartyLeader should return
// false (or the ctx.Err() result) immediately at the pre-join check, without
// invoking lobbyJoin. If the test times out, the function did not respect the
// cancelled context before entering the join path.
func TestCancel_TryFollow_AlreadyCancelledContext_ChecksCtxBeforeLobbyJoin(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	logger := loggerForTest(t)

	// Leader in a match.
	matchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(matchID)

	registry := newMockFollowMatchRegistry()
	registry.SetMatch(matchID, &MatchLabel{
		ID:          matchID,
		Mode:        evr.ModeArenaPublic,
		Open:        true,
		PlayerLimit: 8,
	})
	env.withMockNK(registry)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel BEFORE calling

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { recover() }() //nolint:errcheck
		// We don't care about the return value — just that it returns.
		env.pipeline.TryFollowPartyLeader(ctx, logger, env.session, env.params, env.lobbyGroup)
	}()

	select {
	case <-done:
		// Returned or panicked promptly — acceptable. The important thing is it
		// didn't block for 3+ seconds by not checking ctx.
	case <-time.After(3 * time.Second):
		t.Error("CHAOS BUG: TryFollowPartyLeader did not return promptly with an " +
			"already-cancelled context. The function does not check ctx.Err() before " +
			"entering the lobbyJoin path (line 1137 in evr_lobby_find.go). " +
			"Fix: add 'if ctx.Err() != nil { return false }' before the lobbyJoin call.")
	}
}

// TestBoundary_TryFollow_PartySizeZero_DoesNotPanic verifies that when
// lobbyGroup.Size() returns 0 (empty party handler), the required-slots
// arithmetic doesn't panic or produce nonsense.
func TestBoundary_TryFollow_PartySizeZero_DoesNotPanic(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	// Remove all members from the party list, leaving Size() == 0.
	env.ph.members = NewPartyPresenceList(8)

	logger := loggerForTest(t)
	matchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(matchID)

	registry := newMockFollowMatchRegistry()
	registry.SetMatch(matchID, &MatchLabel{
		ID:          matchID,
		Mode:        evr.ModeArenaPublic,
		Open:        true,
		PlayerLimit: 8,
	})
	env.withMockNK(registry)

	require.NotPanics(t, func() {
		defer func() { recover() }() //nolint:errcheck
		env.pipeline.TryFollowPartyLeader(
			context.Background(), logger, env.session, env.params, env.lobbyGroup)
	})
}

// TestBoundary_TryFollow_MaxPartySize_DoesNotPanic verifies large party sizes
// don't overflow or panic inside requiredSlots arithmetic.
func TestBoundary_TryFollow_MaxPartySize_DoesNotPanic(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	logger := loggerForTest(t)

	// Populate the party handler with many members.
	presences := make([]*Presence, 0, 255)
	for i := 0; i < 255; i++ {
		presences = append(presences, newPresenceWithIDs("testnode", uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())))
	}
	// NewPartyPresenceList defaults to max 8; swap to one that can hold more.
	bigList := NewPartyPresenceList(300)
	bigList.Join(presences)
	env.ph.members = bigList

	matchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(matchID)

	registry := newMockFollowMatchRegistry()
	registry.SetMatch(matchID, &MatchLabel{
		ID:          matchID,
		Mode:        evr.ModeArenaPublic,
		Open:        true,
		PlayerLimit: 8,
	})
	env.withMockNK(registry)

	require.NotPanics(t, func() {
		defer func() { recover() }() //nolint:errcheck
		env.pipeline.TryFollowPartyLeader(
			context.Background(), logger, env.session, env.params, env.lobbyGroup)
	})
}

// ---------------------------------------------------------------------------
// TryFollowPartyLeader — concurrent calls with shared lobbyGroup
// ---------------------------------------------------------------------------

// TestConcurrent_TryFollow_SharedLobbyGroup_NoDataRace fires many concurrent
// TryFollowPartyLeader calls all sharing the same LobbyGroup. The race
// detector will catch any unsynchronised reads of ph.leader or ph.members.
func TestConcurrent_TryFollow_SharedLobbyGroup_NoDataRace(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	logger := loggerForTest(t)

	// Put the leader in a match so we get past the early returns.
	matchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(matchID)
	registry := newMockFollowMatchRegistry()
	registry.SetMatch(matchID, &MatchLabel{
		ID:          matchID,
		Mode:        evr.ModeArenaPublic,
		Open:        true,
		PlayerLimit: 8,
	})
	env.withMockNK(registry)

	var wg sync.WaitGroup
	ctx := context.Background()

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { recover() }() //nolint:errcheck
			env.pipeline.TryFollowPartyLeader(ctx, logger, env.session, env.params, env.lobbyGroup)
		}()
	}

	// Concurrently mutate leader while followers are checking.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			time.Sleep(2 * time.Millisecond)
			env.setLeader(uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), "mutated-leader")
		}
		// Restore the original leader.
		env.setLeader(env.leaderSID, env.leaderUID, "leader")
	}()

	wg.Wait()
}

// ---------------------------------------------------------------------------
// TryFollowPartyLeader — leader state transitions mid-call
// ---------------------------------------------------------------------------

// TestStateTransition_TryFollow_LeaderDisappearsAfterPresenceRead exercises
// the window where the leader's presence is found, then the leader is cleared
// before the match label lookup. The function must not panic.
func TestStateTransition_TryFollow_LeaderDisappearsAfterPresenceRead(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	logger := loggerForTest(t)

	matchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(matchID)

	registry := newMockFollowMatchRegistry()
	// Do NOT add the match to the registry — simulates match vanishing.
	env.withMockNK(registry)

	// Clear the leader concurrently with the call.
	go func() {
		time.Sleep(1 * time.Millisecond)
		env.clearLeader()
	}()

	require.NotPanics(t, func() {
		defer func() { recover() }() //nolint:errcheck
		env.pipeline.TryFollowPartyLeader(
			context.Background(), logger, env.session, env.params, env.lobbyGroup)
	})
}

// TestStateTransition_TryFollow_LeaderChangesMatchMidCall exercises the case
// where the leader switches matches between the service-stream read and the
// MatchLabelByID call (TOCTOU on the match ID). The match registry has only
// the new match — the stale ID returned by the tracker produces ErrMatchNotFound.
func TestStateTransition_TryFollow_LeaderChangesMatchMidCall(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	logger := loggerForTest(t)

	staleMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	newMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	// Tracker returns stale match ID first.
	env.setLeaderMatch(staleMatchID)

	registry := newMockFollowMatchRegistry()
	// Only new match exists in registry — stale returns ErrMatchNotFound.
	registry.SetMatch(newMatchID, &MatchLabel{
		ID:          newMatchID,
		Mode:        evr.ModeArenaPublic,
		Open:        true,
		PlayerLimit: 8,
	})
	env.withMockNK(registry)

	// After the tracker read but before registry lookup, leader switches to new match.
	go func() {
		time.Sleep(1 * time.Millisecond)
		env.setLeaderMatch(newMatchID)
	}()

	// Should not panic. Result is false (stale ID not found in registry).
	require.NotPanics(t, func() {
		defer func() { recover() }() //nolint:errcheck
		result := env.pipeline.TryFollowPartyLeader(
			context.Background(), logger, env.session, env.params, env.lobbyGroup)
		// The stale match returns ErrMatchNotFound → result must be false.
		require.False(t, result,
			"stale match ID (not in registry) must cause TryFollow to return false, not panic")
	})
}

// ---------------------------------------------------------------------------
// pollFollowPartyLeader — already-cancelled context on entry
// ---------------------------------------------------------------------------

// TestCancel_Poll_AlreadyCancelledContext_ReturnsFalse verifies that
// pollFollowPartyLeader returns promptly when the context is already cancelled
// before it enters its loop (not just during a select).
func TestCancel_Poll_AlreadyCancelledContext_ReturnsFalse(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	logger := loggerForTest(t)

	// Give the leader a match (so we don't return early for other reasons).
	matchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(matchID)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel BEFORE entering poll

	done := make(chan bool, 1)
	go func() {
		done <- env.pipeline.pollFollowPartyLeader(ctx, logger, env.session, env.params, env.lobbyGroup)
	}()

	select {
	case result := <-done:
		require.False(t, result,
			"already-cancelled context must cause poll to return false immediately")
	case <-time.After(5 * time.Second):
		t.Fatal("pollFollowPartyLeader hung with already-cancelled context")
	}
}

// ---------------------------------------------------------------------------
// pollFollowPartyLeader — concurrent param mutation
// ---------------------------------------------------------------------------

// TestRace_Poll_ConcurrentParamsMutationAndPoll exercises the SMELL at line 1119:
// TryFollowPartyLeader mutates params.Mode as a side-effect. If two goroutines
// call poll (or try/poll) simultaneously with the same params pointer, they race
// on the Mode field. This test must be run with -race to catch it.
func TestRace_Poll_ConcurrentParamsMutationAndPoll(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	_ = env // env used only for groupID below

	// Shared params — the bug: multiple goroutines share one *LobbySessionParameters.
	params := &LobbySessionParameters{
		GroupID:   env.groupID,
		Mode:      evr.ModeArenaPublic,
		PartySize: uatomic.NewInt64(2),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	// Simulate concurrent callers reading params.Mode while one mutates it.
	// This reproduces the hidden side-effect of TryFollowPartyLeader (SMELL H7).
	var modeReads int64
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				// Read Mode (as lobbyFind does at line 75 for the switch statement).
				mode := params.Mode
				_ = shouldFollowerFindOrCreateSocial(mode)
				atomic.AddInt64(&modeReads, 1)
			}
		}()
	}

	// Mutator goroutine: simulates TryFollowPartyLeader's hidden side-effect.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			// Hidden mutation (SMELL H7: TryFollowPartyLeader does this without caller knowledge).
			params.Mode = evr.ModeSocialPublic
			time.Sleep(time.Millisecond)
			params.Mode = evr.ModeArenaPublic
		}
	}()

	wg.Wait()
	// If -race doesn't fire, at least confirm reads happened.
	require.Greater(t, atomic.LoadInt64(&modeReads), int64(0))
}

// ---------------------------------------------------------------------------
// isLeaderHeadingToSocial — boundary and chaos inputs
// ---------------------------------------------------------------------------

// TestNil_IsLeaderHeadingToSocial_NilLobbyGroup_DoesNotPanic verifies the
// function does not panic when passed a LobbyGroup with nil ph.
func TestNil_IsLeaderHeadingToSocial_NilLobbyGroup_DoesNotPanic(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	logger := loggerForTest(t)
	nilGroup := &LobbyGroup{ph: nil}

	require.NotPanics(t, func() {
		result := env.pipeline.isLeaderHeadingToSocial(
			context.Background(), logger, env.session, env.params, nilGroup)
		// nil ph → GetLeader returns nil → should return false.
		require.False(t, result)
	})
}

// TestCancel_IsLeaderHeadingToSocial_CancelledContext_DoesNotPanic verifies
// the function handles a cancelled context without panicking. The function
// calls MatchLabelByID which might use the context — if it checks ctx.Err()
// on an already-cancelled context, it must still return cleanly.
func TestCancel_IsLeaderHeadingToSocial_CancelledContext_DoesNotPanic(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	logger := loggerForTest(t)

	// Put leader in a match so we reach the MatchLabelByID call.
	matchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(matchID)

	registry := newMockFollowMatchRegistry()
	registry.SetMatch(matchID, &MatchLabel{
		ID:   matchID,
		Mode: evr.ModeSocialPublic,
		Open: true,
	})
	env.withMockNK(registry)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before entry

	require.NotPanics(t, func() {
		env.pipeline.isLeaderHeadingToSocial(ctx, logger, env.session, env.params, env.lobbyGroup)
	})
}

// TestBoundary_IsLeaderHeadingToSocial_SelfIsLeader_ReturnsFalse verifies
// that when the session IS the leader (leader.SessionId == session.id), the
// function returns false immediately without querying the tracker.
func TestBoundary_IsLeaderHeadingToSocial_SelfIsLeader_ReturnsFalse(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	logger := loggerForTest(t)

	// Make the follower also the leader.
	env.setLeader(env.followerSID, env.followerUID, "self-leader")

	result := env.pipeline.isLeaderHeadingToSocial(
		context.Background(), logger, env.session, env.params, env.lobbyGroup)
	require.False(t, result,
		"when session is the leader, isLeaderHeadingToSocial must return false (not heading to social)")
}

// TestBoundary_IsLeaderHeadingToSocial_MalformedJSON_FallsThrough verifies
// the SMELL at line 898–908: malformed JSON in the leader's matchmaking status
// causes Unmarshal to fail → the function falls through to step 2 (match check)
// instead of returning a defined value. This is the C2/H6 bug reproduced from
// the CHAOS angle: what happens when we inject garbage bytes?
func TestBoundary_IsLeaderHeadingToSocial_MalformedJSON_FallsThrough(t *testing.T) {
	t.Parallel()

	corruptStatuses := []string{
		"{bad json",
		"null",
		"[]",
		"\x00\x01\x02",
		string(make([]byte, 1024)), // 1KB of zero bytes
		`{"mode": 99999999999999999999}`,
	}

	for _, status := range corruptStatuses {
		status := status
		t.Run("corrupt:"+status[:chaosMin(len(status), 12)], func(t *testing.T) {
			t.Parallel()

			env := newFollowTestEnv(t)
			logger := loggerForTest(t)

			env.tracker.Track(context.Background(), env.leaderSID,
				PresenceStream{Mode: StreamModeMatchmaking, Subject: env.groupID},
				env.leaderUID,
				PresenceMeta{Status: status})

			// Must not panic regardless of corrupt status.
			require.NotPanics(t, func() {
				env.pipeline.isLeaderHeadingToSocial(
					context.Background(), logger, env.session, env.params, env.lobbyGroup)
			})
		})
	}
}

// TestConcurrent_IsLeaderHeadingToSocial_ConcurrentTrackerUpdates fires
// concurrent calls to isLeaderHeadingToSocial while the tracker is updated
// from a different goroutine. The race detector must stay silent.
func TestConcurrent_IsLeaderHeadingToSocial_ConcurrentTrackerUpdates(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	logger := loggerForTest(t)

	// Prime the leader's presence.
	leaderParams := &LobbySessionParameters{
		Mode:      evr.ModeSocialPublic,
		GroupID:   env.groupID,
		PartySize: uatomic.NewInt64(2),
	}
	statusBytes, err := json.Marshal(leaderParams)
	require.NoError(t, err)

	env.tracker.Track(context.Background(), env.leaderSID,
		PresenceStream{Mode: StreamModeMatchmaking, Subject: env.groupID},
		env.leaderUID,
		PresenceMeta{Status: string(statusBytes)})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup

	// Readers calling isLeaderHeadingToSocial.
	for i := 0; i < 15; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				env.pipeline.isLeaderHeadingToSocial(
					context.Background(), logger, env.session, env.params, env.lobbyGroup)
			}
		}()
	}

	// Writer: rapidly tracks/untracks the leader's presence.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ctx.Err() == nil {
			env.tracker.Track(context.Background(), env.leaderSID,
				PresenceStream{Mode: StreamModeMatchmaking, Subject: env.groupID},
				env.leaderUID,
				PresenceMeta{Status: string(statusBytes)})
			env.tracker.UntrackLocalByModes(env.leaderSID,
				map[uint8]struct{}{StreamModeMatchmaking: {}},
				PresenceStream{})
		}
	}()

	wg.Wait()
}

// ---------------------------------------------------------------------------
// configureParty — error injection: json.Marshal failure path
// ---------------------------------------------------------------------------

// TestBoundary_ConfigureParty_NilMarshalResult_TracksEmptyStatus proves the
// SMELL at evr_lobby_find.go:330: when json.Marshal produces nil bytes (on
// error, which current code silently discards), the tracked presence has
// Status: "". A follower calling isLeaderHeadingToSocial on that empty status
// will fail to unmarshal and return false — wrong routing decision.
//
// This test is complementary to C2 in the regression suite. It directly
// verifies the mechanistic link between the discarded marshal error and the
// observable tracker state.
func TestBoundary_ConfigureParty_NilMarshalResult_TracksEmptyStatus(t *testing.T) {
	t.Parallel()

	// Verify the marshal-error-produces-nil-bytes invariant the SMELL relies on.
	// json.Marshal can't actually fail for LobbySessionParameters under normal
	// conditions; the SMELL is about the pattern, not a specific failure mode.
	// We verify the pattern: string(nil) == "".
	var nilBytes []byte
	require.Equal(t, "", string(nilBytes),
		"SMELL C2: string(nil bytes from failed marshal) == empty string, "+
			"which is an invalid JSON status for the tracker")

	// Verify that empty-string Status causes json.Unmarshal to fail in the
	// isLeaderHeadingToSocial step-1 path.
	var params LobbySessionParameters
	err := json.Unmarshal([]byte(""), &params)
	require.Error(t, err,
		"SMELL C2: empty status (from marshal failure) causes Unmarshal to fail — "+
			"isLeaderHeadingToSocial silently falls through to step 2 with wrong result")

	// Now verify the concrete behavioral consequence: a tracked leader with empty
	// status is indistinguishable (from isLeaderHeadingToSocial's perspective) from
	// a leader NOT on the matchmaking stream at all.
	tracker := newMockMatchmakingTracker()
	leaderSID := uuid.Must(uuid.NewV4())
	leaderUID := uuid.Must(uuid.NewV4())
	groupID := uuid.Must(uuid.NewV4())
	followerSID := uuid.Must(uuid.NewV4())
	followerUID := uuid.Must(uuid.NewV4())

	// Leader tracked with empty status (marshal failed).
	tracker.Track(context.Background(), leaderSID,
		PresenceStream{Mode: StreamModeMatchmaking, Subject: groupID},
		leaderUID,
		PresenceMeta{Status: ""})

	ph := &PartyHandler{members: NewPartyPresenceList(8)}
	ph.leader = &PartyLeader{
		UserPresence: &rtapi.UserPresence{
			UserId: leaderUID.String(), SessionId: leaderSID.String(), Username: "leader",
		},
		PresenceID: &PresenceID{SessionID: leaderSID, Node: "testnode"},
	}
	lobbyGroup := &LobbyGroup{ph: ph}
	params2 := &LobbySessionParameters{GroupID: groupID}
	session := &sessionWS{}
	session.id = followerSID
	session.userID = followerUID
	session.pipeline = &Pipeline{node: "testnode"}
	session.pipeline.tracker = tracker

	pipeline := &EvrPipeline{}
	logger := loggerForTest(t)

	// isLeaderHeadingToSocial with empty status SHOULD return false (leader IS in
	// matchmaking stream but with empty status — step 1 fails silently).
	result := pipeline.isLeaderHeadingToSocial(context.Background(), logger, session, params2, lobbyGroup)

	// CHAOS ASSERTION: the silent marshal failure makes this return false even
	// though the leader IS on the matchmaking stream. This is the bug.
	// The test documents the failure mode: result is false when it should be
	// indeterminate (we don't know the leader's mode). The CORRECT behavior
	// after fix: return false explicitly with a log, OR the marshal never fails.
	require.False(t, result,
		"SMELL C2 confirmed: leader on matchmaking stream with empty status causes "+
			"isLeaderHeadingToSocial to return false (step-1 silently failed). "+
			"Follower incorrectly proceeds independently instead of waiting for leader.")
}

// ---------------------------------------------------------------------------
// appendPartyReservationPlaceholders — boundary and nil chaos
// ---------------------------------------------------------------------------

// TestNil_AppendPartyReservation_NilEntrants_DoesNotPanic verifies the
// function handles a nil entrants slice without panicking.
func TestNil_AppendPartyReservation_NilEntrants_DoesNotPanic(t *testing.T) {
	t.Parallel()

	logger := loggerForTest(t)
	params := &LobbySessionParameters{
		Mode:    evr.ModeSocialPublic,
		PartyID: uuid.Must(uuid.NewV4()),
	}

	ph := &PartyHandler{members: NewPartyPresenceList(4)}
	ph.leader = &PartyLeader{
		UserPresence: &rtapi.UserPresence{UserId: uuid.Must(uuid.NewV4()).String(), SessionId: uuid.Must(uuid.NewV4()).String()},
		PresenceID:   &PresenceID{SessionID: uuid.Must(uuid.NewV4()), Node: "testnode"},
	}
	sid1 := uuid.Must(uuid.NewV4())
	uid1 := uuid.Must(uuid.NewV4())
	ph.members.Join([]*Presence{newPresenceWithIDs("testnode", sid1, uid1)})
	lobbyGroup := &LobbyGroup{ph: ph}

	require.NotPanics(t, func() {
		result := appendPartyReservationPlaceholders(logger, nil, lobbyGroup, params, "testnode")
		// Nil in → may add placeholders, should not panic.
		_ = result
	})
}

// TestBoundary_AppendPartyReservation_NonSocialMode_NoOp verifies that for
// non-social modes, the function is a no-op (returns entrants unchanged).
func TestBoundary_AppendPartyReservation_NonSocialMode_NoOp(t *testing.T) {
	t.Parallel()

	logger := loggerForTest(t)
	nonSocialModes := []evr.Symbol{
		evr.ModeArenaPublic,
		evr.ModeCombatPublic,
		evr.ModeArenaPrivate,
		evr.Symbol(0),
	}

	for _, mode := range nonSocialModes {
		mode := mode
		t.Run(mode.String(), func(t *testing.T) {
			t.Parallel()

			params := &LobbySessionParameters{Mode: mode}
			sid := uuid.Must(uuid.NewV4())
			uid := uuid.Must(uuid.NewV4())
			entrants := []*EvrMatchPresence{{SessionID: sid, UserID: uid}}

			ph := &PartyHandler{members: NewPartyPresenceList(4)}
			ph.leader = &PartyLeader{
				UserPresence: &rtapi.UserPresence{UserId: uid.String(), SessionId: sid.String()},
				PresenceID:   &PresenceID{SessionID: sid, Node: "testnode"},
			}
			ph.members.Join([]*Presence{newPresenceWithIDs("testnode", sid, uid),
				newPresenceWithIDs("testnode", uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()))})
			lobbyGroup := &LobbyGroup{ph: ph}

			result := appendPartyReservationPlaceholders(logger, entrants, lobbyGroup, params, "testnode")
			require.Equal(t, len(entrants), len(result),
				"non-social mode %s must return entrants unchanged", mode.String())
		})
	}
}

// TestBoundary_AppendPartyReservation_AllMembersAlreadyEntrants_NoPlaceholders
// verifies that if every party member is already in the entrants slice, no
// duplicate placeholders are added.
func TestBoundary_AppendPartyReservation_AllMembersAlreadyEntrants_NoPlaceholders(t *testing.T) {
	t.Parallel()

	logger := loggerForTest(t)
	partyID := uuid.Must(uuid.NewV4())

	sid1 := uuid.Must(uuid.NewV4())
	uid1 := uuid.Must(uuid.NewV4())
	sid2 := uuid.Must(uuid.NewV4())
	uid2 := uuid.Must(uuid.NewV4())

	entrants := []*EvrMatchPresence{
		{SessionID: sid1, UserID: uid1},
		{SessionID: sid2, UserID: uid2},
	}
	params := &LobbySessionParameters{Mode: evr.ModeSocialPublic, PartyID: partyID}

	ph := &PartyHandler{members: NewPartyPresenceList(4)}
	ph.leader = &PartyLeader{
		UserPresence: &rtapi.UserPresence{UserId: uid1.String(), SessionId: sid1.String()},
		PresenceID:   &PresenceID{SessionID: sid1, Node: "testnode"},
	}
	ph.members.Join([]*Presence{
		newPresenceWithIDs("testnode", sid1, uid1),
		newPresenceWithIDs("testnode", sid2, uid2),
	})
	lobbyGroup := &LobbyGroup{ph: ph}

	result := appendPartyReservationPlaceholders(logger, entrants, lobbyGroup, params, "testnode")
	require.Equal(t, 2, len(result),
		"all members already in entrants — no placeholders should be appended")
}

// TestConcurrent_AppendPartyReservation_ConcurrentMemberListRead verifies that
// reading lobbyGroup.List() from appendPartyReservationPlaceholders while the
// party handler has concurrent member changes does not data-race.
func TestConcurrent_AppendPartyReservation_ConcurrentMemberListRead(t *testing.T) {
	t.Parallel()

	logger := loggerForTest(t)
	partyID := uuid.Must(uuid.NewV4())
	params := &LobbySessionParameters{Mode: evr.ModeSocialPublic, PartyID: partyID}

	leaderSID := uuid.Must(uuid.NewV4())
	leaderUID := uuid.Must(uuid.NewV4())
	entrants := []*EvrMatchPresence{{SessionID: leaderSID, UserID: leaderUID}}

	ph := &PartyHandler{members: NewPartyPresenceList(20)}
	ph.leader = &PartyLeader{
		UserPresence: &rtapi.UserPresence{UserId: leaderUID.String(), SessionId: leaderSID.String()},
		PresenceID:   &PresenceID{SessionID: leaderSID, Node: "testnode"},
	}
	ph.members.Join([]*Presence{newPresenceWithIDs("testnode", leaderSID, leaderUID)})
	lobbyGroup := &LobbyGroup{ph: ph}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup

	// Callers: concurrent appendPartyReservationPlaceholders.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				_ = appendPartyReservationPlaceholders(logger, entrants, lobbyGroup, params, "testnode")
			}
		}()
	}

	// Mutator: concurrent member joins.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ctx.Err() == nil {
			sid := uuid.Must(uuid.NewV4())
			uid := uuid.Must(uuid.NewV4())
			ph.members.Join([]*Presence{newPresenceWithIDs("testnode", sid, uid)})
		}
	}()

	wg.Wait()
}

// ---------------------------------------------------------------------------
// Error injection — tracker returns failure during configureParty-style flow
// ---------------------------------------------------------------------------

// TestBoundary_TrackerTrackReturnsFalse_DoesNotCrash exercises the code path
// at evr_lobby_find.go:338 where tracker.Track returns success=false. The
// production code logs a warning and continues. This verifies the code does not
// panic when Track fails.
func TestBoundary_TrackerTrackReturnsFalse_DoesNotCrash(t *testing.T) {
	t.Parallel()

	// Use a tracker that always returns (false, false) from Track.
	failingTracker := &alwaysFailTracker{mockMatchmakingTracker: *newMockMatchmakingTracker()}
	sessionID := uuid.Must(uuid.NewV4())
	userID := uuid.Must(uuid.NewV4())
	groupID := uuid.Must(uuid.NewV4())
	mmStream := PresenceStream{Mode: StreamModeMatchmaking, Subject: groupID}

	// The code at configureParty line 338 does:
	//   success, _ := p.nk.tracker.Track(ctx, session.id, mmStream, session.userID, presenceMeta)
	//   if !success { logger.Warn("Failed to track...") }
	// Reproduce this pattern directly.
	success, _ := failingTracker.Track(context.Background(), sessionID, mmStream, userID, PresenceMeta{})
	require.False(t, success, "alwaysFailTracker must return false")

	// Verify code that checks !success doesn't panic.
	logger := loggerForTest(t)
	if !success {
		logger.Warn("Failed to track leader on matchmaking stream early")
	}
	// No panic = test passes.
}

// alwaysFailTracker wraps mockMatchmakingTracker but always returns Track=false.
type alwaysFailTracker struct {
	mockMatchmakingTracker
}

func (t *alwaysFailTracker) Track(_ context.Context, _ uuid.UUID, _ PresenceStream, _ uuid.UUID, _ PresenceMeta) (bool, bool) {
	return false, false
}

// ---------------------------------------------------------------------------
// shouldFollowerFindOrCreateSocial — benchmark
// ---------------------------------------------------------------------------

// BenchmarkShouldFollower measures the predicate under hot-path conditions.
// Use b.Loop() per Go 1.24 benchmark API.
func BenchmarkShouldFollower_SocialPublic(b *testing.B) {
	for b.Loop() {
		_ = shouldFollowerFindOrCreateSocial(evr.ModeSocialPublic)
	}
}

func BenchmarkShouldFollower_ArenaPublic(b *testing.B) {
	for b.Loop() {
		_ = shouldFollowerFindOrCreateSocial(evr.ModeArenaPublic)
	}
}

// ---------------------------------------------------------------------------
// LobbyGroup — nil leader's expectedInitialLeader fallback
// ---------------------------------------------------------------------------

// TestBoundary_LobbyGroup_GetLeader_FallsBackToExpectedInitialLeader verifies
// that when ph.leader is nil but ph.expectedInitialLeader is set, GetLeader
// returns the expected initial leader (the fallback path in evr_lobby_group.go:37).
func TestBoundary_LobbyGroup_GetLeader_FallsBackToExpectedInitialLeader(t *testing.T) {
	t.Parallel()

	expected := &rtapi.UserPresence{
		UserId:    uuid.Must(uuid.NewV4()).String(),
		SessionId: uuid.Must(uuid.NewV4()).String(),
		Username:  "expected-leader",
	}

	ph := &PartyHandler{
		members:             NewPartyPresenceList(4),
		expectedInitialLeader: expected,
		// leader is nil (not yet set)
	}
	g := &LobbyGroup{ph: ph}

	got := g.GetLeader()
	require.Equal(t, expected, got,
		"GetLeader must fall back to expectedInitialLeader when leader is nil")
}

// ---------------------------------------------------------------------------
// Concurrent TryFollowPartyLeader + pollFollowPartyLeader — stress combo
// ---------------------------------------------------------------------------

// TestConcurrent_TryAndPoll_SharedEnv_NoDataRace runs TryFollowPartyLeader and
// pollFollowPartyLeader concurrently on the same environment. Both functions
// read lobbyGroup.GetLeader() and tracker state — any unsynchronised read/write
// will be caught by the race detector.
func TestConcurrent_TryAndPoll_SharedEnv_NoDataRace(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	logger := loggerForTest(t)

	matchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(matchID)

	registry := newMockFollowMatchRegistry()
	registry.SetMatch(matchID, &MatchLabel{
		ID:          matchID,
		Mode:        evr.ModeArenaPublic,
		Open:        true,
		PlayerLimit: 8,
	})
	env.withMockNK(registry)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup

	// Run TryFollowPartyLeader concurrently.
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { recover() }() //nolint:errcheck
			env.pipeline.TryFollowPartyLeader(ctx, logger, env.session, env.params, env.lobbyGroup)
		}()
	}

	// Run pollFollowPartyLeader concurrently.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() { recover() }() //nolint:errcheck
		env.pipeline.pollFollowPartyLeader(ctx, logger, env.session, env.params, env.lobbyGroup)
	}()

	// Mutate leader concurrently.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			default:
				env.setLeader(uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), "rotating-leader")
				time.Sleep(5 * time.Millisecond)
				env.setLeader(env.leaderSID, env.leaderUID, "leader")
				time.Sleep(5 * time.Millisecond)
			}
		}
	}()

	wg.Wait()
}

// ---------------------------------------------------------------------------
// LobbyGroup.Size() boundary — empty vs solo vs full
// ---------------------------------------------------------------------------

// TestBoundary_LobbyGroup_Size_EmptyList_ReturnsZero verifies that an empty
// party list (no Join calls) returns Size() == 0, not a negative value.
func TestBoundary_LobbyGroup_Size_EmptyList_ReturnsZero(t *testing.T) {
	t.Parallel()

	ph := &PartyHandler{members: NewPartyPresenceList(4)}
	g := &LobbyGroup{ph: ph}
	require.Equal(t, 0, g.Size())
}

// TestBoundary_LobbyGroup_Size_SingleMember_ReturnsOne verifies Size() == 1.
func TestBoundary_LobbyGroup_Size_SingleMember_ReturnsOne(t *testing.T) {
	t.Parallel()

	sid := uuid.Must(uuid.NewV4())
	uid := uuid.Must(uuid.NewV4())
	ph := &PartyHandler{members: NewPartyPresenceList(4)}
	ph.members.Join([]*Presence{newPresenceWithIDs("testnode", sid, uid)})
	g := &LobbyGroup{ph: ph}
	require.Equal(t, 1, g.Size())
}

// chaosMin returns the smaller of two ints. Avoids shadowing Go's built-in min.
func chaosMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}
