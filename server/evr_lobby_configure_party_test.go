package server

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	uatomic "go.uber.org/atomic"
)

// kickTrackingStreamManager wraps testStreamManager and records UserLeave calls
// so tests can assert whether StreamUserKick was invoked.
type kickTrackingStreamManager struct {
	testStreamManager
	kickCount atomic.Int32
}

func (m *kickTrackingStreamManager) UserLeave(stream PresenceStream, userID, sessionID uuid.UUID) error {
	m.kickCount.Add(1)
	return nil
}

// TestConfigureParty_FollowerNotOnMatchmakingStream_NotKicked verifies that
// when the leader re-queues for matchmaking, followers who are party members
// but not yet on the matchmaking stream (e.g. transitioning between queue
// cycles) are NOT kicked from the stream.
//
// Reproduces the production bug where:
//  1. Leader's matchmaking times out
//  2. Leader re-queues, calling configureParty
//  3. Follower's matchmaking stream was cleaned up during the timeout
//  4. configureParty sees follower not on stream → kicks them
//  5. Kick cancels follower's context → follower ends up in solo lobby
func TestConfigureParty_FollowerNotOnMatchmakingStream_NotKicked(t *testing.T) {
	logger := loggerForTest(t)
	tracker := newMockMatchmakingTracker()
	mm, mmCleanup := createLightMatchmaker(t, logger)
	defer mmCleanup()

	ksm := &kickTrackingStreamManager{}
	dmr := &DummyMessageRouter{}
	pr := NewLocalPartyRegistry(logger, cfg, mm, tracker, ksm, dmr, "testnode")

	groupName := "testgroup"

	// Create the leader session.
	leaderSession := newTestSessionForParty(t, "leader", tracker, pr)

	// Create the follower session.
	followerSession := newTestSessionForParty(t, "follower", tracker, pr)

	// Both join the same party group (leader joins first, becomes leader).
	_, leaderIsLeader, err := JoinPartyGroup(leaderSession, groupName, MatchID{})
	require.NoError(t, err)
	require.True(t, leaderIsLeader)

	_, followerIsLeader, err := JoinPartyGroup(followerSession, groupName, MatchID{})
	require.NoError(t, err)
	require.False(t, followerIsLeader)

	// Set up the EvrPipeline with tracker and stream manager so that
	// nk.StreamUserGet and nk.StreamUserKick work.
	pipeline := &EvrPipeline{
		node: "testnode",
		nk: &RuntimeGoNakamaModule{
			tracker:       tracker,
			streamManager: ksm,
		},
	}

	groupID := uuid.Must(uuid.NewV4())
	lobbyParams := &LobbySessionParameters{
		PartyGroupName: groupName,
		GroupID:        groupID,
		PartySize:      uatomic.NewInt64(1),
	}

	// Leader IS on the matchmaking stream (they just re-queued).
	tracker.Track(context.Background(), leaderSession.id,
		PresenceStream{Mode: StreamModeMatchmaking, Subject: groupID},
		leaderSession.userID,
		PresenceMeta{Status: "matchmaking"})

	// Follower is NOT on the matchmaking stream. This simulates the
	// re-queue race: the follower's stream was cleaned up when the
	// previous matchmaking cycle ended, and they haven't re-joined yet.
	// (Deliberately not tracking follower on matchmaking stream.)

	// Call configureParty as the leader.
	lobbyGroup, memberSessionIDs, isLeader, err := pipeline.configureParty(
		context.Background(), logger, leaderSession, lobbyParams)
	require.NoError(t, err)
	require.True(t, isLeader)

	// The follower should still be in the party group.
	assert.Equal(t, 2, lobbyGroup.Size(),
		"party should still have 2 members (leader + follower)")

	// The follower's session ID should be in the returned member list.
	assert.Contains(t, memberSessionIDs, followerSession.id,
		"follower should be included in memberSessionIDs")

	// CRITICAL: StreamUserKick should NOT have been called.
	// The current buggy code DOES call it, which cancels the follower's
	// matchmaking context and causes them to end up in a solo lobby.
	assert.Equal(t, int32(0), ksm.kickCount.Load(),
		"follower should NOT be kicked from matchmaking stream — "+
			"they are a party member transitioning between queue cycles")
}

// TestLeavePartyStream_RemovesPartyStreamPresence verifies that
// LeavePartyStream removes the player's party stream tracking from the
// tracker. This documents why LeavePartyStream must NOT be called on
// matchmaking errors — it destroys the party stream presence.
func TestLeavePartyStream_RemovesPartyStreamPresence(t *testing.T) {
	tracker := newMockMatchmakingTracker()

	sessionID := uuid.Must(uuid.NewV4())
	userID := uuid.Must(uuid.NewV4())
	partyID := uuid.Must(uuid.NewV4())

	partyStream := PresenceStream{Mode: StreamModeParty, Subject: partyID, Label: "testnode"}

	// Manually track the session on the party stream.
	tracker.Track(context.Background(), sessionID, partyStream, userID, PresenceMeta{})
	require.True(t, tracker.hasPresence(sessionID, partyStream, userID),
		"setup: session should be on party stream")

	// Create a minimal session with the tracker.
	s := &sessionWS{}
	s.id = sessionID
	s.userID = userID
	s.tracker = tracker

	// LeavePartyStream should remove the party stream presence.
	LeavePartyStream(s)

	assert.False(t, tracker.hasPresence(sessionID, partyStream, userID),
		"LeavePartyStream should remove party stream presence")
}

// TestHandleMatchmakingError_PreservesPartyStream calls handleMatchmakingError
// directly and verifies the party stream is NOT destroyed.
//
// The production bug: matchmaking error → handleMatchmakingError calls
// LeavePartyStream → party destroyed → player re-queues as solo leader.
func TestHandleMatchmakingError_PreservesPartyStream(t *testing.T) {
	tracker := newMockMatchmakingTracker()

	sessionID := uuid.Must(uuid.NewV4())
	userID := uuid.Must(uuid.NewV4())
	partyID := uuid.Must(uuid.NewV4())

	partyStream := PresenceStream{Mode: StreamModeParty, Subject: partyID, Label: "testnode"}

	// Player is on the party stream (in a party).
	tracker.Track(context.Background(), sessionID, partyStream, userID, PresenceMeta{})

	s := &sessionWS{}
	s.id = sessionID
	s.userID = userID
	s.tracker = tracker

	groupID := uuid.Must(uuid.NewV4())
	lobbyParams := &LobbySessionParameters{
		GroupID:   groupID,
		PartySize: uatomic.NewInt64(1),
	}
	lobbyParams.Mode = evr.ModeArenaPublic

	// Call handleMatchmakingError with a generic lobby error.
	someError := NewLobbyError(InternalError, "test error")
	_ = handleMatchmakingError(loggerForTest(t), s, lobbyParams, &testMetrics{}, someError)

	// Party stream must survive — players don't leave parties on matchmaking errors.
	assert.True(t, tracker.hasPresence(sessionID, partyStream, userID),
		"party stream should survive matchmaking error — "+
			"players don't leave parties just because matchmaking failed")
}

// TestConfigureParty_AllFollowersOnStream_NoKick is a baseline test:
// when all followers ARE on the matchmaking stream, no kicks should occur.
func TestConfigureParty_AllFollowersOnStream_NoKick(t *testing.T) {
	logger := loggerForTest(t)
	tracker := newMockMatchmakingTracker()
	mm, mmCleanup := createLightMatchmaker(t, logger)
	defer mmCleanup()

	ksm := &kickTrackingStreamManager{}
	dmr := &DummyMessageRouter{}
	pr := NewLocalPartyRegistry(logger, cfg, mm, tracker, ksm, dmr, "testnode")

	groupName := "testgroup"

	leaderSession := newTestSessionForParty(t, "leader", tracker, pr)
	followerSession := newTestSessionForParty(t, "follower", tracker, pr)

	_, _, err := JoinPartyGroup(leaderSession, groupName, MatchID{})
	require.NoError(t, err)
	_, _, err = JoinPartyGroup(followerSession, groupName, MatchID{})
	require.NoError(t, err)

	pipeline := &EvrPipeline{
		node: "testnode",
		nk: &RuntimeGoNakamaModule{
			tracker:       tracker,
			streamManager: ksm,
		},
	}

	groupID := uuid.Must(uuid.NewV4())
	lobbyParams := &LobbySessionParameters{
		PartyGroupName: groupName,
		GroupID:        groupID,
		PartySize:      uatomic.NewInt64(1),
	}

	// Both leader and follower are on the matchmaking stream.
	tracker.Track(context.Background(), leaderSession.id,
		PresenceStream{Mode: StreamModeMatchmaking, Subject: groupID},
		leaderSession.userID,
		PresenceMeta{Status: "matchmaking"})

	tracker.Track(context.Background(), followerSession.id,
		PresenceStream{Mode: StreamModeMatchmaking, Subject: groupID},
		followerSession.userID,
		PresenceMeta{Status: "matchmaking"})

	lobbyGroup, memberSessionIDs, isLeader, err := pipeline.configureParty(
		context.Background(), logger, leaderSession, lobbyParams)
	require.NoError(t, err)
	require.True(t, isLeader)

	assert.Equal(t, 2, lobbyGroup.Size())
	assert.Contains(t, memberSessionIDs, followerSession.id)
	assert.Equal(t, int32(0), ksm.kickCount.Load(),
		"no kicks should occur when all followers are on the stream")
}
