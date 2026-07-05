package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTryFollowPartyLeader_DoesNotSelfFollow proves the self-follow guard.
//
// When the party "leader" is the SAME user as the follower on a DIFFERENT
// session — a stale/ghost session (e.g. a dropped connection whose presence
// lingered) still holding party leadership — TryFollowPartyLeader must fall
// through to normal matchmaking WITHOUT chasing that ghost. Concretely it must
// short-circuit before ever consulting the tracker to look up the leader's
// match; otherwise it would follow a session that is not in a real match and
// get released to solo (the production self-follow "split").
func TestTryFollowPartyLeader_DoesNotSelfFollow(t *testing.T) {
	env := newFollowTestEnv(t)

	// Make the party leader this player's own account on a different session.
	// (leaderSID != followerSID by construction — a distinct, ghost session.)
	env.ph.leader.UserPresence.UserId = env.followerUID.String()
	require.NotEqual(t, env.session.id.String(), env.ph.leader.UserPresence.SessionId,
		"leader must be a different session than the follower")

	// No tracker state is set up on purpose: WITHOUT the guard the code advances
	// past the early checks and queries the tracker for the leader's streams
	// (incrementing getLocalCount). WITH the guard it must return before any of
	// that. So the assertion below distinguishes the two regardless of what the
	// tracker would have returned.
	before := env.tracker.getLocalCount.Load()
	result := env.pipeline.TryFollowPartyLeader(
		context.Background(), loggerForTest(t), env.session, env.params, env.lobbyGroup)

	require.False(t, result, "must not follow your own other session")
	require.Equal(t, before, env.tracker.getLocalCount.Load(),
		"self-follow guard must short-circuit before consulting the tracker")
}

// TestTryFollowPartyLeader_RealLeaderStillConsultsTracker is the control: a
// genuine party (leader is a DIFFERENT user) must NOT be short-circuited by the
// self-follow guard — it proceeds to consult the tracker exactly as before.
func TestTryFollowPartyLeader_RealLeaderStillConsultsTracker(t *testing.T) {
	env := newFollowTestEnv(t) // leader is a different user by default

	// Put the (real) leader on the matchmaking stream. The guard does not fire
	// for a different user, so the code proceeds to consult the tracker and then
	// returns cleanly at the "leader is currently matchmaking" check — no nil-nk
	// panic, so no recover() is needed to observe the tracker consultation.
	env.setLeaderMatchmaking()

	before := env.tracker.getLocalCount.Load()
	result := env.pipeline.TryFollowPartyLeader(
		context.Background(), loggerForTest(t), env.session, env.params, env.lobbyGroup)

	require.False(t, result, "leader is matchmaking → follower falls through")
	require.Greater(t, env.tracker.getLocalCount.Load(), before,
		"a real (different-user) leader must still be looked up via the tracker")
}
