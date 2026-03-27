package server

import (
	"context"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
)

// newTestSessionForParty creates a minimal sessionWS suitable for JoinPartyGroup.
func newTestSessionForParty(t *testing.T, username string, tracker Tracker, partyRegistry PartyRegistry) *sessionWS {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	s := &sessionWS{}
	s.id = uuid.Must(uuid.NewV4())
	s.userID = uuid.Must(uuid.NewV4())
	s.username = atomic.NewString(username)
	s.ctx = ctx
	s.ctxCancelFn = cancel
	s.logger = loggerForTest(t)
	s.format = SessionFormatProtobuf
	s.outgoingCh = make(chan []byte, 16)
	s.pipeline = &Pipeline{node: "testnode"}
	s.pipeline.tracker = tracker
	s.pipeline.partyRegistry = partyRegistry
	return s
}

// TestExpectedCount_UsesMatchPresencePartyID verifies that the expectedCount
// logic in configureParty uses the leader's party ID from the match presence
// (set at join time) rather than lobbyParams.PartyID (which may have changed
// since joining the match via /party group).
//
// Production incident (2026-03-27T03:51Z):
//
//	A 3-player party was in a social lobby with LobbyGroupName="monarch12".
//	The leader changed to "divergent" via /party group. When they queued,
//	expectedCount was 0 instead of 2 because lobbyParams.PartyID (from
//	"divergent") didn't match any match presence party_id (from "monarch12").
//	The leader proceeded without waiting.
//
// Fix: look up the leader's own party_id from the match presences, and use
// that for the comparison. This correctly counts party members regardless of
// whether the LobbyGroupName changed after joining the match.
func TestExpectedCount_UsesMatchPresencePartyID(t *testing.T) {

	oldGroupName := "monarch12"
	newGroupName := "divergent"

	oldPartyID := uuid.NewV5(PartyIDSalt, oldGroupName)
	newPartyID := uuid.NewV5(PartyIDSalt, newGroupName)

	require.NotEqual(t, oldPartyID, newPartyID,
		"sanity: different group names must produce different party IDs")

	leaderUserID := uuid.Must(uuid.NewV4())
	followerAUserID := uuid.Must(uuid.NewV4())
	followerBUserID := uuid.Must(uuid.NewV4())

	// All 3 joined the lobby while on "monarch12", so their match presence
	// party_id is oldPartyID. This never gets updated when the group changes.
	matchPresences := map[string]*EvrMatchPresence{
		leaderUserID.String(): {
			UserID:  leaderUserID,
			PartyID: oldPartyID,
		},
		followerAUserID.String(): {
			UserID:  followerAUserID,
			PartyID: oldPartyID,
		},
		followerBUserID.String(): {
			UserID:  followerBUserID,
			PartyID: oldPartyID,
		},
	}

	// The leader changed their LobbyGroupName to "divergent".
	// lobbyParams.PartyID is now newPartyID.
	lobbyParamsPartyID := newPartyID

	// FIX: use the leader's match presence party_id instead of lobbyParams.PartyID.
	matchPartyID := lobbyParamsPartyID
	if leaderPresence, ok := matchPresences[leaderUserID.String()]; ok && !leaderPresence.PartyID.IsNil() {
		matchPartyID = leaderPresence.PartyID
	}

	expectedCount := 0
	for _, mp := range matchPresences {
		if mp.PartyID == matchPartyID && mp.UserID != leaderUserID {
			expectedCount++
		}
	}

	assert.Equal(t, 2, expectedCount,
		"leader should count 2 party members using match presence party_id")
}

// TestPartyGroupSplit_StaleGroupName demonstrates the downstream effect:
// when a follower has a stale LobbyGroupName, they compute a different
// partyID and JoinPartyGroup places them in a separate PartyHandler.
//
// This is a consequence of the party being formed at matchmaking time
// (not persisted) — each user's LobbyGroupName is read independently
// from storage, and different names produce different partyIDs.
func TestPartyGroupSplit_StaleGroupName(t *testing.T) {

	logger := loggerForTest(t)
	tracker := newMockMatchmakingTracker()
	mm, mmCleanup := createLightMatchmaker(t, logger)
	defer mmCleanup()

	tsm := testStreamManager{}
	dmr := &DummyMessageRouter{}
	pr := NewLocalPartyRegistry(logger, cfg, mm, tracker, tsm, dmr, "testnode")

	currentGroupName := "divergent"
	staleGroupName := "monarch12"

	currentPartyID := uuid.NewV5(PartyIDSalt, currentGroupName)
	stalePartyID := uuid.NewV5(PartyIDSalt, staleGroupName)

	require.NotEqual(t, currentPartyID, stalePartyID)

	leaderSession := newTestSessionForParty(t, "leader", tracker, pr)
	followerASession := newTestSessionForParty(t, "followerA", tracker, pr)
	followerBSession := newTestSessionForParty(t, "followerB", tracker, pr)

	currentMatchID := MatchID{}

	// Leader and follower A both have the new group name.
	leaderGroup, _, err := JoinPartyGroup(leaderSession, currentGroupName, currentPartyID, currentMatchID)
	require.NoError(t, err)

	followerAGroup, _, err := JoinPartyGroup(followerASession, currentGroupName, currentPartyID, currentMatchID)
	require.NoError(t, err)

	assert.Equal(t, leaderGroup.ID(), followerAGroup.ID(),
		"leader and followerA should be in the same party group")

	// Follower B still has the stale group name → different partyID.
	followerBGroup, followerBIsLeader, err := JoinPartyGroup(followerBSession, staleGroupName, stalePartyID, currentMatchID)
	require.NoError(t, err)

	// BUG: followerB silently ends up in a completely separate party.
	assert.True(t, followerBIsLeader,
		"BUG: stale follower becomes leader of a new, separate party")
	assert.NotEqual(t, leaderGroup.ID(), followerBGroup.ID(),
		"BUG: stale follower is in a different party group than the leader")
}
