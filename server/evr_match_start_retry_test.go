package server

// Regression tests for the MatchStart retry path in MatchLoop.
//
// The start branch in MatchLoop sits ABOVE every idle/empty timer and returns
// unconditionally, so whatever it does is the last word for that tick. Two
// opposite failure modes have to be pinned at once:
//
//  1. It must not KILL the match on a transient dispatch failure (a bare
//     `return nil` makes the handler Stop() without MatchTerminate).
//
//  2. It must not KEEP the match forever either. MatchStart used to mutate the
//     caller's label (StartTime, GameState) *before* the dispatch that could
//     fail, so a failed start left Started() == true with levelLoaded == false.
//     That combination disables the 15-minute "did not start on time" reclaim
//     (it requires !Started()), makes the start branch's own guard
//     `len(presenceMap) != 0 || state.Started()` true forever, and so starves
//     every timer below it. The match then re-dispatched MatchStart at tick
//     rate for the life of the process.
//
// So: the label may only be mutated by a start that actually dispatched, and
// the retries must be bounded (MatchStartMaxAttempts) and throttled.

import (
	"context"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// startFailDispatcher fails every broadcast (so MatchStart's dispatchMessages,
// and therefore MatchStart, fails) and counts the attempts.
type startFailDispatcher struct {
	runtime.MatchDispatcher
	broadcasts int
}

func (d *startFailDispatcher) BroadcastMessage(_ int64, _ []byte, _ []runtime.Presence, _ runtime.Presence, _ bool) error {
	d.broadcasts++
	return errTerminationTestDispatch
}

func (d *startFailDispatcher) BroadcastMessageDeferred(_ int64, _ []byte, _ []runtime.Presence, _ runtime.Presence, _ bool) error {
	d.broadcasts++
	return errTerminationTestDispatch
}

func (d *startFailDispatcher) MatchKick(_ []runtime.Presence) error { return nil }
func (d *startFailDispatcher) MatchLabelUpdate(_ string) error      { return nil }

// startRetryState builds an assigned lobby with a game server presence that has
// not yet loaded its level, with `players` players in the presence map.
func startRetryState(mode evr.Symbol, players int) *MatchLabel {
	state := reconnectTestState(mode)
	state.levelLoaded = false
	for i := 0; i < players; i++ {
		p := reconnectTestPlayer("start-retry-"+string(rune('a'+i)), evr.TeamBlue)
		state.presenceMap[p.GetSessionId()] = p
		state.presenceByEvrID[p.EvrID] = p
		state.joinTimestamps[p.GetSessionId()] = time.Now()
	}
	state.rebuildCache()
	return state
}

// TestMatchStart_FailedStartDoesNotMutateTheLabel pins the root cause. A start
// that never dispatched must leave StartTime and GameState exactly as it found
// them, so the label keeps telling the truth: this match has NOT started.
//
// The preserved StartTime matters beyond Started(): SignalPrepareSession
// (evr_match.go, "case SignalPrepareSession") schedules a future StartTime, and
// clobbering it with time.Now() on a failed start would silently reschedule the
// session.
func TestMatchStart_FailedStartDoesNotMutateTheLabel(t *testing.T) {
	// ModeArenaPublic is one of the modes whose MatchStart rebuilds
	// GameState.SessionScoreboard, i.e. resets the round clock.
	state := startRetryState(evr.ModeArenaPublic, 1)

	scheduled := time.Now().UTC().Add(10 * time.Minute)
	state.StartTime = scheduled
	originalGameState := state.GameState

	nk := &reconnectTestNakamaModule{}
	dispatcher := &startFailDispatcher{}
	m := &EvrMatch{}

	got, err := m.MatchStart(context.Background(), reconnectTestLogger(), nk, dispatcher, state)
	if err == nil {
		t.Fatal("test did not exercise the failing-start path")
	}
	if got == nil {
		t.Fatal("MatchStart returned a nil label on a dispatch failure; the caller has nothing to keep running with")
	}

	if !state.StartTime.Equal(scheduled) {
		t.Errorf("a failed start moved StartTime from the scheduled %v to %v; Started() now reports %v with levelLoaded=%v, which disables the 15-minute reclaim and traps MatchLoop in the start branch",
			scheduled, state.StartTime, state.Started(), state.levelLoaded)
	}
	if state.GameState != originalGameState {
		t.Error("a failed start replaced GameState (and its SessionScoreboard), resetting the round clock for an attempt that never reached the game server")
	}
	if state.levelLoaded {
		t.Error("a failed start marked the level as loaded")
	}
}

// TestMatchStart_SuccessfulStartCommitsTheLabel is the other half: the deferred
// commit must still happen when the dispatch succeeds. Without this, the
// "don't mutate on failure" fix would silently break every normal start.
func TestMatchStart_SuccessfulStartCommitsTheLabel(t *testing.T) {
	state := startRetryState(evr.ModeArenaPublic, 1)
	state.StartTime = time.Now().UTC().Add(10 * time.Minute)
	originalGameState := state.GameState
	before := time.Now().UTC()

	nk := &reconnectTestNakamaModule{}
	dispatcher := &reconnectTestDispatcher{}
	m := &EvrMatch{}

	if _, err := m.MatchStart(context.Background(), reconnectTestLogger(), nk, dispatcher, state); err != nil {
		t.Fatalf("MatchStart failed unexpectedly: %v", err)
	}

	if !state.levelLoaded {
		t.Error("a successful start did not mark the level as loaded")
	}
	if state.StartTime.Before(before) {
		t.Errorf("a successful start did not commit StartTime: got %v, want >= %v", state.StartTime, before)
	}
	if !state.Started() {
		t.Error("a successful start left Started() false")
	}
	if state.GameState == originalGameState {
		t.Error("a successful arena start did not install the new GameState/SessionScoreboard")
	}
	if state.GameState == nil || state.GameState.SessionScoreboard == nil {
		t.Fatal("a successful arena start left no SessionScoreboard")
	}
}

// TestMatchLoop_FailedStartIsBoundedAndShutsDownOrderly: a game server session
// that dies without a clean presence-leave (node partition, dropped socket)
// leaves state.server set while every dispatch to it fails. The match must give
// up after MatchStartMaxAttempts and run the orderly shutdown, not retry until
// the process dies.
func TestMatchLoop_FailedStartIsBoundedAndShutsDownOrderly(t *testing.T) {
	state := startRetryState(evr.ModeArenaPublic, 2)

	nk := &reconnectTestNakamaModule{}
	dispatcher := &startFailDispatcher{}
	m := &EvrMatch{}
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")

	// MatchStartMaxAttempts attempts, throttled to one per second after the
	// first, so the deadline lands around tick MatchStartMaxAttempts*tickRate.
	maxIterations := int((MatchStartMaxAttempts + 5) * state.tickRate)

	shutdownAt := 0
	for i := 1; i <= maxIterations; i++ {
		out := m.MatchLoop(ctx, reconnectTestLogger(), nil, nk, dispatcher, int64(i), state, nil)
		if out == nil {
			t.Fatalf("MatchLoop bare-returned nil on iteration %d: the handler Stop()s without MatchTerminate, so the two players get no shutdown sequence at all", i)
		}
		if state.terminateTick != 0 {
			shutdownAt = i
			break
		}
	}

	if shutdownAt == 0 {
		t.Fatalf("a permanently failing MatchStart was never reclaimed: after %d ticks the loop had re-dispatched the start %d times, startAttempts=%d, and no timer can ever fire because the start branch returns above all of them",
			maxIterations, dispatcher.broadcasts, state.startAttempts)
	}
	if state.startAttempts > MatchStartMaxAttempts {
		t.Errorf("start was attempted %d times, want at most %d", state.startAttempts, MatchStartMaxAttempts)
	}
	if state.Open {
		t.Error("expected the match to be closed to new joins after the shutdown")
	}

	// The retries must be throttled, not run at tick rate: one attempt per
	// second means far fewer attempts than ticks elapsed.
	if state.startAttempts >= int64(shutdownAt) {
		t.Errorf("start was retried every tick (%d attempts in %d ticks); retries after the first must be throttled to one per second", state.startAttempts, shutdownAt)
	}
}

// TestMatchLoop_FailedStartRestoresTheDidNotStartOnTimeReclaim: because a
// failed start no longer forges Started(), the 15-minute reclaim for a match
// that never started is reachable again. This is the path that recovers an
// empty prepared lobby whose game server went away.
func TestMatchLoop_FailedStartRestoresTheDidNotStartOnTimeReclaim(t *testing.T) {
	state := startRetryState(evr.ModeArenaPublic, 1)
	state.CreatedAt = time.Now().UTC().Add(-16 * time.Minute)

	nk := &reconnectTestNakamaModule{}
	dispatcher := &startFailDispatcher{}
	m := &EvrMatch{}
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")

	// Tick once with a player present: the start is attempted and fails.
	if out := m.MatchLoop(ctx, reconnectTestLogger(), nil, nk, dispatcher, 1, state, nil); out == nil {
		t.Fatal("MatchLoop returned nil on the first failed start")
	}
	if state.Started() {
		t.Fatal("a failed start still reports Started(); the 15-minute reclaim below is unreachable")
	}

	// The player leaves; the match is now an empty, never-started lobby that
	// was created 16 minutes ago.
	for sid := range state.presenceMap {
		delete(state.presenceMap, sid)
	}
	state.rebuildCache()

	out := m.MatchLoop(ctx, reconnectTestLogger(), nil, nk, dispatcher, 2, state, nil)
	if out == nil {
		t.Fatal("MatchLoop bare-returned nil for the 15-minute reclaim: the handler Stop()s without MatchTerminate")
	}
	if state.terminateTick == 0 {
		t.Fatal("the 15-minute \"did not start on time\" reclaim never fired for a match whose start had failed")
	}
	if state.Open {
		t.Error("expected the match to be closed to new joins after the reclaim")
	}
}

// TestMatchLoop_PostStartLabelUpdateFailureKeepsMatchAlive covers the
// `updateLabel` call that runs immediately AFTER a successful MatchStart.
// labelFailDispatcher lets the start dispatch succeed and fails only
// MatchLabelUpdate, which is the only way to reach that branch.
func TestMatchLoop_PostStartLabelUpdateFailureKeepsMatchAlive(t *testing.T) {
	state := startRetryState(evr.ModeSocialPublic, 1)

	nk := &reconnectTestNakamaModule{}
	dispatcher := &labelFailDispatcher{}
	m := &EvrMatch{}
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")

	got := m.MatchLoop(ctx, reconnectTestLogger(), nil, nk, dispatcher, 1, state, nil)
	if got == nil {
		t.Fatal("MatchLoop returned nil after the post-start label update failed: the match handler Stop()s without MatchTerminate, killing a match that had just successfully started")
	}
	if _, ok := got.(*MatchLabel); !ok {
		t.Fatalf("MatchLoop returned %T, want *MatchLabel", got)
	}
	if !state.levelLoaded {
		t.Fatal("test did not exercise the post-start path: the start never succeeded")
	}
	if dispatcher.calls == 0 {
		t.Fatal("test did not exercise the label-update failure path")
	}
	if state.terminateTick != 0 {
		t.Error("a transient label-update failure must not shut the match down")
	}
}
