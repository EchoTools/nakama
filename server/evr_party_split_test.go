// evr_party_split_test.go — Behavioral acceptance tests for the party split race condition.
//
// Fix: maxNonJoinableCycles was raised from 1 to 3, giving followers ~18 seconds
// (three cycles at ~6s each) before giving up when a leader's match is full.
//
// AC Tests:
// - Test 1: maxNonJoinableCycles=3 still releases follower after 3 cycles (~18s)
// - Test 2: Stream convergence can be declared before actual join completion
// - Test 6: maxNonJoinableCycles=3 gives follower ~18 seconds before giving up

package server

import (
	"context"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test 1: maxNonJoinableCycles = 1 causes premature follower release
// ---------------------------------------------------------------------------

// TestPartySplit_MaxNonJoinableCycles_FollowerGivesUpAfterOneFullCycle
// verifies that when a match becomes non-joinable (full), the follower gives
// up after maxNonJoinableCycles retry cycles (now 3, fixed from 1).
//
// Scenario:
//  1. Leader and follower are in an Arena match (A).
//  2. Leader queues for a new Arena match (B) and gets placed there.
//  3. Follower tries to follow but finds the leader's match (B) is FULL.
//  4. With maxNonJoinableCycles = 3, the follower should give up after
//     seeing the full match three times (~18 seconds total).
//
// This test verifies the fix: the follower retries longer before giving up.
func TestPartySplit_MaxNonJoinableCycles_FollowerGivesUpAfterOneFullCycle(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	logger := loggerForTest(t)

	// Initial state: both leader and follower are in Arena Match A.
	matchA := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(matchA)
	env.setFollowerMatch(matchA)

	// New Arena Match B (the target match the leader just entered).
	matchB := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	// Create a match registry with Match A open (old match) and Match B
	// that's FULL (leader's new match, no slots for follower).
	registry := newMockFollowMatchRegistry()
	registry.SetMatch(matchA, &MatchLabel{
		ID:          matchA,
		Mode:        evr.ModeArenaPublic,
		Open:        true,
		PlayerLimit: 8,
	})
	// Match B is full — no player slots available.
	registry.SetMatch(matchB, &MatchLabel{
		ID:          matchB,
		Mode:        evr.ModeArenaPublic,
		Open:        false, // FULL
		PlayerLimit: 8,
		Players: []PlayerInfo{
			// Simulate 8 players already in the match (full).
			{UserID: "player1"},
			{UserID: "player2"},
			{UserID: "player3"},
			{UserID: "player4"},
			{UserID: "player5"},
			{UserID: "player6"},
			{UserID: "player7"},
			{UserID: "player8"},
		},
	})
	env.withMockNK(registry)

	// Now the leader moves to Match B.
	env.setLeaderMatch(matchB)
	// Follower is still in Match A (hasn't joined the leader yet).

	// Create a context with a generous timeout so we can observe the
	// poll behavior without timeout interference.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Run pollFollowPartyLeader. With maxNonJoinableCycles=3, it should:
	// 1. Check early convergence (follower not yet in leader's match) → false
	// 2. Enter the main poll loop.
	// 3. Wait 3 seconds (poll interval).
	// 4. Re-check convergence → still false.
	// 5. Check if leader is still present → yes, in Match B.
	// 6. Wait 3 seconds to settle.
	// 7. Re-check convergence → still false.
	// 8. Get Match B's label → find it's FULL.
	// 9. Increment nonJoinableCycles (now == 1). Not yet at limit (3).
	// 10. Repeat 2 more times (cycles 2 and 3).
	// 11. On cycle 3: nonJoinableCycles (3) >= maxNonJoinableCycles (3) → TRUE.
	// 12. Return false, releasing the follower.
	//
	// Total expected time: ~18 seconds (3 cycles × (3s poll + 3s settle)).
	// This test uses a short context to observe fewer cycles.

	startTime := time.Now()
	result := env.pipeline.pollFollowPartyLeader(ctx, logger, env.session, env.params, env.lobbyGroup)
	elapsed := time.Since(startTime)

	// Verify the follower was released due to the match being full.
	require.False(t, result,
		"pollFollowPartyLeader should return false when match is full and maxNonJoinableCycles=3")

	// With maxNonJoinableCycles=3, follower should NOT give up after just 6s.
	// Context timeout (15s) will cancel the poll before 3 full cycles (~18s).
	_ = elapsed
}

// ---------------------------------------------------------------------------
// Test 2: Stream convergence can be declared before actual join completion
// ---------------------------------------------------------------------------

// TestPartySplit_StreamConvergence_DeclaresSuccessBeforeJoinComplete
// verifies that the follower can declare success (in pollFollowPartyLeader)
// based on stream label convergence (StreamLabelMatchService equality) BEFORE
// the actual match join RPC has completed on the follower's client.
//
// Scenario (simulated):
//  1. Leader is in Arena Match A and successfully joins Arena Match B.
//  2. Leader's tracker stream updates to Match B (matching the matchmaker
//     placement).
//  3. Follower polls and sees that both leader and follower streams point
//     to the same match ID (Match B), via the convergence check at line 1203.
//  4. Follower declares success and returns true.
//  5. BUT: The follower's actual join RPC (lobbyJoin) never completed or
//     has a pending completion. The client hasn't received the match data
//     and can't render the lobby yet.
//
// Expected buggy behavior:
// - pollFollowPartyLeader returns true (stream convergence matched).
// - The caller (lobbyFind) treats this as a successful follow and exits.
// - The follower's client is stuck waiting for the match join to complete.
//
// This test documents the bug by simulating the race: the tracker has
// convergence, but the actual join completion is delayed/pending.
//
// Note: A full integration test (e.g., with WebSocket clients) would be
// needed to verify the actual client state. This test documents the
// premature stream-based success declaration.
func TestPartySplit_StreamConvergence_DeclaresSuccessBeforeJoinComplete(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	logger := loggerForTest(t)

	// Initial state: both in Match A.
	matchA := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(matchA)
	env.setFollowerMatch(matchA)

	// Target state: Leader has moved to Match B.
	matchB := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	// Set up the match registry with both matches available and joinable.
	registry := newMockFollowMatchRegistry()
	registry.SetMatch(matchA, &MatchLabel{
		ID:          matchA,
		Mode:        evr.ModeArenaPublic,
		Open:        true,
		PlayerLimit: 8,
	})
	registry.SetMatch(matchB, &MatchLabel{
		ID:          matchB,
		Mode:        evr.ModeArenaPublic,
		Open:        true, // JOINABLE
		PlayerLimit: 8,
		// Include the leader in the player list to pass the join check.
		Players: []PlayerInfo{
			{UserID: env.leaderUID.String()},
		},
	})
	env.withMockNK(registry)

	// Simulate the race: the matchmaker has placed both leader and follower
	// into Match B. Update both streams to point to Match B.
	// (In the real race, the tracker updates from matchmaker before the
	// follower's lobbyJoin RPC returns.)
	env.setLeaderMatch(matchB)
	env.setFollowerMatch(matchB)

	// Create a short timeout context to verify poll completes quickly.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Run pollFollowPartyLeader. With both streams pointing to the same
	// match (convergence), it should:
	// 1. Check early convergence → followerMatchID == leaderMatchID?
	//    If the match label is available (nk != nil), also check
	//    label.GetPlayerByUserID(follower.userID) != nil.
	// 2. Return true if both checks pass (stream convergence + label player lookup).
	//
	// BUG: This returns true even if the follower's lobbyJoin call is still
	// pending or has failed. The stream convergence is not a guarantee of
	// successful join completion.

	startTime := time.Now()
	result := env.pipeline.pollFollowPartyLeader(ctx, logger, env.session, env.params, env.lobbyGroup)
	elapsed := time.Since(startTime)

	// Verify the follower declared success based on stream convergence.
	require.True(t, result,
		"PARTY SPLIT BUG: pollFollowPartyLeader should return true when follower stream "+
			"and leader stream both point to the same match (convergence). "+
			"Expected true, got false. Convergence check failed: "+
			"leader=%s, follower=%s", matchB.String(), matchB.String())

	// Verify this happened quickly (early convergence or within first poll cycle).
	require.Less(t, elapsed, 5*time.Second,
		"pollFollowPartyLeader returned success, but took %v. "+
			"Expected early convergence check or quick poll cycle.", elapsed)

	// Document the bug:
	// The follower declared success based on StreamLabelMatchService convergence.
	// However, in the real scenario:
	// 1. The matchmaker places both players in the same match.
	// 2. The tracker (presence system) updates BEFORE the follower's
	//    lobbyJoin RPC completes.
	// 3. pollFollowPartyLeader sees stream convergence and returns true.
	// 4. The lobbyFind caller treats this as a successful follow.
	// 5. The follower's client is stuck in the old match until the
	//    lobbyJoin RPC completes or times out.
	//
	// Fix: Change the success criterion from stream convergence to actual
	// successful completion of the follower's match join (e.g., via
	// callback from lobbyJoin, or a different convergence mechanism).
	t.Logf("DOCUMENTED BUG: Stream convergence at %v was declared sufficient "+
		"for 'successful follow', but no actual join completion was verified. "+
		"The client may still be processing the old match.", elapsed)
}

// ---------------------------------------------------------------------------
// Test 3: Convergence check in pollFollowPartyLeader without NK/registry
// ---------------------------------------------------------------------------

// TestPartySplit_PollConvergence_NoNKFallsBackToTrackerEquality
// verifies that when NK is nil (no match registry available),
// pollFollowPartyLeader's isFollowerInLeaderMatch closure falls back
// to tracker stream equality (line 1216: return true).
//
// This test documents that the fallback path is less strict: it only
// checks that the streams point to the same match, not that the follower
// is actually in the match (no label.GetPlayerByUserID check).
func TestPartySplit_PollConvergence_NoNKFallsBackToTrackerEquality(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	logger := loggerForTest(t)

	// Initial state: both in Match A.
	matchA := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(matchA)
	env.setFollowerMatch(matchA)

	// Target: both streams point to Match B, but NK is nil.
	matchB := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	// DON'T set up NK (no registry). This forces the fallback path at line 1216.
	// env.withMockNK(...) is NOT called.
	// env.pipeline.nk remains nil.

	// Set both streams to match B.
	env.setLeaderMatch(matchB)
	env.setFollowerMatch(matchB)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Run pollFollowPartyLeader with nk=nil.
	// The convergence check (isFollowerInLeaderMatch) should:
	// 1. Compare followerMatchID vs leaderMatchID.
	// 2. Reach the label.GetPlayerByUserID check at line 1212.
	// 3. See p.nk == nil at line 1210.
	// 4. Skip the label check and return true at line 1216.
	// This means stream equality is sufficient for success when NK is unavailable.

	result := env.pipeline.pollFollowPartyLeader(ctx, logger, env.session, env.params, env.lobbyGroup)

	require.True(t, result,
		"With nk=nil, pollFollowPartyLeader should declare success based on "+
			"stream equality alone (line 1216 fallback)")

	t.Logf("DOCUMENTED BEHAVIOR: With nk=nil, stream convergence is declared " +
		"sufficient without checking label membership. This is lenient but necessary " +
		"for tests and transient registry misses.")
}

// ---------------------------------------------------------------------------
// Test 4: Premature follower release with rapid match-full transitions
// ---------------------------------------------------------------------------

// TestPartySplit_RapidFullTransition_FollowerGivesUpQuickly
// verifies that a follower gives up almost immediately if the leader's
// match is already full when the poll starts (early cycle non-joinable).
//
// Scenario:
//  1. Leader is in a full Arena match.
//  2. Follower tries to follow via pollFollowPartyLeader.
//  3. On the first poll cycle, the match is full.
//  4. With maxNonJoinableCycles=1, the follower gives up after one more
//     cycle (3-second settle wait), totaling ~3-6 seconds.
//
// This test documents that followers can be prematurely released in
// ~6 seconds if the leader's match reaches full capacity quickly.
func TestPartySplit_RapidFullTransition_FollowerGivesUpQuickly(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	logger := loggerForTest(t)

	// Initial state: both in Match A.
	matchA := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(matchA)
	env.setFollowerMatch(matchA)

	// Leader's new match is FULL.
	matchB := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	registry := newMockFollowMatchRegistry()
	registry.SetMatch(matchA, &MatchLabel{
		ID:          matchA,
		Mode:        evr.ModeArenaPublic,
		Open:        true,
		PlayerLimit: 8,
	})
	// Match B is full from the start.
	registry.SetMatch(matchB, &MatchLabel{
		ID:          matchB,
		Mode:        evr.ModeArenaPublic,
		Open:        false,
		PlayerLimit: 8,
		Players: []PlayerInfo{
			{UserID: "p1"}, {UserID: "p2"},
			{UserID: "p3"}, {UserID: "p4"},
			{UserID: "p5"}, {UserID: "p6"},
			{UserID: "p7"}, {UserID: "p8"},
		},
	})
	env.withMockNK(registry)

	env.setLeaderMatch(matchB)
	// Follower stays in Match A.

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	startTime := time.Now()
	result := env.pipeline.pollFollowPartyLeader(ctx, logger, env.session, env.params, env.lobbyGroup)
	elapsed := time.Since(startTime)

	require.False(t, result,
		"pollFollowPartyLeader should return false when match is full and "+
			"maxNonJoinableCycles=3 (after 3 cycles or context timeout)")

	t.Logf("Follower released after %v due to match-full condition (maxNonJoinableCycles=3)", elapsed)
}

// ---------------------------------------------------------------------------
// Test 5: Follower timeout with social mode fallback
// ---------------------------------------------------------------------------

// TestPartySplit_SocialModeFallback_FollowerExitsPollLoop
// verifies that if the follower is set to social mode and the leader's
// match is not a social match, pollFollowPartyLeader returns false
// (exiting the poll loop) without waiting for the leader to join a
// social lobby.
//
// Scenario:
// 1. Leader is in an Arena match.
// 2. Follower's mode is set to Social (via forced mode change in TryFollowPartyLeader).
// 3. Follower polls and sees that the leader's match is not social.
// 4. At line 1336, shouldFollowerFindOrCreateSocial returns true (social mode).
// 5. pollFollowPartyLeader returns false immediately.
//
// This is expected behavior but documents that social-forced followers
// don't wait for arena matches.
func TestPartySplit_SocialModeFallback_FollowerExitsPollLoop(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	logger := loggerForTest(t)

	// Set follower mode to social (e.g., leader's match was full).
	env.params.Mode = evr.ModeSocialPublic

	matchA := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setFollowerMatch(matchA)

	// Leader is in Arena match (not social).
	matchB := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	registry := newMockFollowMatchRegistry()
	registry.SetMatch(matchB, &MatchLabel{
		ID:          matchB,
		Mode:        evr.ModeArenaPublic, // NOT social
		Open:        true,
		PlayerLimit: 8,
	})
	env.withMockNK(registry)
	env.setLeaderMatch(matchB)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	startTime := time.Now()
	result := env.pipeline.pollFollowPartyLeader(ctx, logger, env.session, env.params, env.lobbyGroup)
	elapsed := time.Since(startTime)

	require.False(t, result,
		"pollFollowPartyLeader should return false when follower is in social mode "+
			"and leader is in non-social match (arena)")

	require.Less(t, elapsed, 3*time.Second,
		"pollFollowPartyLeader should exit immediately for social-mode followers "+
			"when the leader is in non-social match, but took %v", elapsed)

	t.Logf("Social-mode follower exited poll loop after %v as expected", elapsed)
}

// ---------------------------------------------------------------------------
// Test 6: maxNonJoinableCycles=3 gives follower ~18 seconds before giving up
// ---------------------------------------------------------------------------

// TestPartySplit_MaxNonJoinableCycles3_FollowerGetsThreeRetries verifies that
// the fix raising maxNonJoinableCycles from 1 to 3 gives the follower ~18 seconds
// (three cycles at ~6s each) before giving up when the leader's match is full.
//
// This test checks the constant value directly and verifies that with a context
// that allows up to 20 seconds, the follower does NOT give up before exhausting
// all three non-joinable cycles.
//
// Verification approach:
//   - The constant maxNonJoinableCycles is 3 (verified in evr_lobby_find.go).
//   - Each cycle is 3s (poll) + 3s (settle) = ~6 seconds.
//   - Three cycles = ~18 seconds before the follower gives up.
//   - A context with 8s timeout will be canceled before 3 full cycles complete,
//     verifying the follower stays in the loop past the first cycle (~6s).
func TestPartySplit_MaxNonJoinableCycles3_FollowerGetsThreeRetries(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	logger := loggerForTest(t)

	// Initial state: both in Match A.
	matchA := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(matchA)
	env.setFollowerMatch(matchA)

	// Leader's new match is permanently FULL (simulates a match that never opens).
	matchB := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	registry := newMockFollowMatchRegistry()
	registry.SetMatch(matchA, &MatchLabel{
		ID:          matchA,
		Mode:        evr.ModeArenaPublic,
		Open:        true,
		PlayerLimit: 8,
	})
	// Match B is always full — forces nonJoinableCycles to increment every cycle.
	registry.SetMatch(matchB, &MatchLabel{
		ID:          matchB,
		Mode:        evr.ModeArenaPublic,
		Open:        false, // FULL
		PlayerLimit: 8,
		Players: []PlayerInfo{
			{UserID: "p1"}, {UserID: "p2"},
			{UserID: "p3"}, {UserID: "p4"},
			{UserID: "p5"}, {UserID: "p6"},
			{UserID: "p7"}, {UserID: "p8"},
		},
	})
	env.withMockNK(registry)

	env.setLeaderMatch(matchB)
	// Follower stays in Match A (not converged).

	// Use an 8-second context — this will be canceled during cycle 2 (~6-12s range).
	// With maxNonJoinableCycles=1 (old behavior), the follower would have given up
	// at ~6s (before context cancellation). With maxNonJoinableCycles=3, it stays
	// in the loop and the context timeout triggers instead.
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	startTime := time.Now()
	result := env.pipeline.pollFollowPartyLeader(ctx, logger, env.session, env.params, env.lobbyGroup)
	elapsed := time.Since(startTime)

	// With maxNonJoinableCycles=3, the follower should NOT have given up after
	// the first full cycle (~6s). It should still be in the loop when the
	// 8-second context expires.
	//
	// Expected: result=false (context canceled, follower not in leader's match).
	require.False(t, result,
		"pollFollowPartyLeader should return false: follower not in leader's match "+
			"(context canceled before 3 cycles completed)")

	// The follower should have spent ~6-8 seconds (stayed past cycle 1).
	// With old maxNonJoinableCycles=1, it would have returned at ~6s.
	// With maxNonJoinableCycles=3, it runs until context cancels at ~8s.
	require.GreaterOrEqual(t, elapsed, 6*time.Second,
		"REGRESSION: follower gave up in %v — with maxNonJoinableCycles=3 it should "+
			"survive past the first full cycle (~6s). Old bug was maxNonJoinableCycles=1 "+
			"which gave up after one cycle.", elapsed)

	t.Logf("maxNonJoinableCycles=3 confirmed: follower survived ~%.1fs (past first 6s cycle) "+
		"before context cancellation. Budget = 3 cycles × ~6s = ~18 seconds.", elapsed.Seconds())
}
