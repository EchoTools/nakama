package server

// Regression test for the MatchStart retry THROTTLE (as opposed to the retry
// BOUND covered in evr_match_start_retry_test.go).
//
// The throttle's job is to keep a dead game server from being re-dispatched at
// tick rate. A modulo gate (`tick % tickRate == 0`) does not do that: it aligns
// retries to absolute tick boundaries rather than enforcing a gap since the
// last attempt. With tickRate 10, a first attempt at tick 9 is followed by a
// second at tick 10 — 0.1s later, i.e. the full tick rate for that instant.
//
// This is the same class of defect this PR fixes elsewhere: a timer whose state
// is not its own. The gate must be a function of "when did I last attempt",
// which means the attempt tick has to be tracked per-match, in its own field
// that no other timer touches.

import (
	"context"
	"testing"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// TestMatchLoop_StartRetryEnforcesMinimumGap drives MatchLoop from a tick that
// sits just below a tickRate boundary and asserts the second attempt is at
// least tickRate ticks after the first, regardless of where the first landed.
func TestMatchLoop_StartRetryEnforcesMinimumGap(t *testing.T) {
	state := startRetryState(evr.ModeArenaPublic, 1)

	nk := &reconnectTestNakamaModule{}
	dispatcher := &startFailDispatcher{}
	m := &EvrMatch{}
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")

	// Start one tick below a tickRate boundary: the worst case for a modulo
	// gate. tickRate is 10, so the first attempt is at tick 9 and the next
	// boundary is tick 10.
	const firstTick = int64(9)

	if out := m.MatchLoop(ctx, reconnectTestLogger(), nil, nk, dispatcher, firstTick, state, nil); out == nil {
		t.Fatal("MatchLoop returned nil on the first failed start")
	}
	if state.startAttempts != 1 {
		t.Fatalf("test did not exercise the start branch: startAttempts=%d after the first tick, want 1", state.startAttempts)
	}

	// Walk forward until the second attempt happens.
	secondTick := int64(0)
	for tick := firstTick + 1; tick <= firstTick+3*state.tickRate; tick++ {
		if out := m.MatchLoop(ctx, reconnectTestLogger(), nil, nk, dispatcher, tick, state, nil); out == nil {
			t.Fatalf("MatchLoop returned nil at tick %d", tick)
		}
		if state.startAttempts > 1 {
			secondTick = tick
			break
		}
	}

	if secondTick == 0 {
		t.Fatalf("no second start attempt within %d ticks of the first; the retry is not happening at all", 3*state.tickRate)
	}

	if gap := secondTick - firstTick; gap < state.tickRate {
		t.Errorf("second MatchStart attempt came %d ticks (%.1fs) after the first at tick %d; the throttle must enforce at least %d ticks (1s) between attempts, not align them to absolute tick boundaries",
			gap, float64(gap)/float64(state.tickRate), firstTick, state.tickRate)
	}
}

// TestMatchLoop_StartRetryThrottleIsIndependentOfTheIdleTimers pins the
// counter-aliasing hazard: the throttle's bookkeeping must live in its own
// field. If it shared storage with emptyTicks, a reset on that counter would
// silently re-open the throttle.
func TestMatchLoop_StartRetryThrottleIsIndependentOfTheIdleTimers(t *testing.T) {
	state := startRetryState(evr.ModeArenaPublic, 1)

	nk := &reconnectTestNakamaModule{}
	dispatcher := &startFailDispatcher{}
	m := &EvrMatch{}
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")

	if out := m.MatchLoop(ctx, reconnectTestLogger(), nil, nk, dispatcher, 1, state, nil); out == nil {
		t.Fatal("MatchLoop returned nil on the first failed start")
	}

	// The idle timers must be untouched by a start attempt: the start branch
	// returns above all of them, and a match with a server present has nothing
	// to count.
	if state.emptyTicks != 0 {
		t.Errorf("emptyTicks=%d after a start attempt; the start throttle must not share a counter with the empty-match timer", state.emptyTicks)
	}

	// Clearing the idle counter (what a presence change does) must not let
	// the next tick re-attempt the start ahead of the throttle window.
	state.emptyTicks = 0

	attemptsBefore := state.startAttempts
	if out := m.MatchLoop(ctx, reconnectTestLogger(), nil, nk, dispatcher, 2, state, nil); out == nil {
		t.Fatal("MatchLoop returned nil on the tick after the failed start")
	}
	if state.startAttempts != attemptsBefore {
		t.Errorf("start was re-attempted on the very next tick (attempts %d -> %d); the throttle window must survive an idle-counter reset", attemptsBefore, state.startAttempts)
	}
}
