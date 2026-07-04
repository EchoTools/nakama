package server

import (
	"context"
	"testing"

	"github.com/gofrs/uuid/v5"
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

	// Put the ghost leader "in a match" so that, WITHOUT the guard, the code
	// would advance past the early checks and consult the tracker's service
	// stream to try to follow it.
	env.setLeaderMatch(MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"})

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
	env.setLeaderMatch(MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"})

	before := env.tracker.getLocalCount.Load()
	// Past the guard, the test's nil nk may panic downstream (MatchLabelByID);
	// we only assert the guard did NOT short-circuit — i.e. the tracker WAS
	// consulted for a real leader.
	func() {
		defer func() { _ = recover() }()
		env.pipeline.TryFollowPartyLeader(
			context.Background(), loggerForTest(t), env.session, env.params, env.lobbyGroup)
	}()
	require.Greater(t, env.tracker.getLocalCount.Load(), before,
		"a real (different-user) leader must still be looked up via the tracker")
}
