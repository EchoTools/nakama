package server

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestPollFollowPartyLeader_WaitsForLeaderBetweenMatches proves the patience
// fix. When the leader has no match presence — the normal post-match moment
// where they've left the previous lobby and not yet landed in the next — the
// follow poll must keep waiting for them to reappear (up to its budget) rather
// than bailing to solo on the first tick.
//
// Before the fix, `presence == nil` returned immediately ("Leader left match
// during poll"), so a follower gave up after ~one poll interval. This test uses
// a tiny per-pipeline interval/budget and asserts the poll actually spends
// ~maxPollDuration waiting, which only happens if it keeps polling.
func TestPollFollowPartyLeader_WaitsForLeaderBetweenMatches(t *testing.T) {
	env := newFollowTestEnv(t)

	// Speed the poll up for the test (per-pipeline, no global state / race).
	env.pipeline.pollFollowInterval = 1 * time.Millisecond
	env.pipeline.pollFollowMaxDuration = 30 * time.Millisecond

	// Leader has NO match presence for the whole test (permanently "between
	// matches"): deliberately do NOT call setLeaderMatch. The old code bailed
	// on the first tick; the new code waits the full budget for them to land.

	start := time.Now()
	result := env.pipeline.pollFollowPartyLeader(
		context.Background(), loggerForTest(t), env.session, env.params, env.lobbyGroup)
	elapsed := time.Since(start)

	require.False(t, result, "poll gives up eventually when the leader never lands")
	// Waited its (tiny) budget rather than returning on the first presence==nil.
	require.GreaterOrEqual(t, elapsed, 25*time.Millisecond,
		"poll must keep waiting for a between-matches leader (~maxPollDuration), "+
			"not give up on the first tick")
	// And it honored the fast per-pipeline interval — the pre-fix path waited a
	// hardcoded 3s before bailing on presence==nil, so it could never finish
	// this fast. This is the assertion that fails without the fix.
	require.Less(t, elapsed, 1*time.Second,
		"poll must use the configured interval/budget, not the old hardcoded 3s-then-bail path")
}

// TestPollFollowPartyLeader_StopsAtContextCancel confirms the patient poll
// still terminates promptly when the matchmaking context is canceled (it must
// not ignore cancellation just because it now waits longer for the leader).
func TestPollFollowPartyLeader_StopsAtContextCancel(t *testing.T) {
	env := newFollowTestEnv(t)
	env.pipeline.pollFollowInterval = 5 * time.Millisecond
	env.pipeline.pollFollowMaxDuration = 10 * time.Second // long budget

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled

	start := time.Now()
	result := env.pipeline.pollFollowPartyLeader(
		ctx, loggerForTest(t), env.session, env.params, env.lobbyGroup)
	elapsed := time.Since(start)

	require.False(t, result)
	require.Less(t, elapsed, 1*time.Second,
		"a canceled context must end the poll promptly, not wait the full budget")
}
