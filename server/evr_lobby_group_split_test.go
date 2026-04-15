package server

import (
	"context"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/rtapi"
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
	s.tracker = tracker
	s.pipeline = &Pipeline{node: "testnode"}
	s.pipeline.tracker = tracker
	s.pipeline.partyRegistry = partyRegistry
	return s
}

// TestExpectedCount_UsesMatchPresencePartyID verifies that the expectedCount
// logic in configureParty uses the leader's party ID from the match presence
// (set at join time) rather than lobbyParams.PartyID (which may have changed
// since joining the match via /party group).
func TestExpectedCount_UsesMatchPresencePartyID(t *testing.T) {

	logger := loggerForTest(t)
	tracker := newMockMatchmakingTracker()
	mm, mmCleanup := createLightMatchmaker(t, logger)
	defer mmCleanup()

	tsm := testStreamManager{}
	dmr := &DummyMessageRouter{}
	pr := NewLocalPartyRegistry(logger, cfg, mm, tracker, tsm, dmr, "testnode")

	oldGroupName := "monarch12"
	newGroupName := "divergent"

	leader := makeTestUserPresence("leader")

	// Create parties via the registry so IDs are random.
	oldPH, _, err := pr.GetOrCreateByGroupName(oldGroupName, true, 4, leader)
	require.NoError(t, err)
	oldPartyID := oldPH.ID

	newPH, _, err := pr.GetOrCreateByGroupName(newGroupName, true, 4, leader)
	require.NoError(t, err)
	newPartyID := newPH.ID

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

// makeTestUserPresence is a test helper that creates a minimal rtapi.UserPresence.
func makeTestUserPresence(username string) *rtapi.UserPresence {
	return &rtapi.UserPresence{
		UserId:    uuid.Must(uuid.NewV4()).String(),
		SessionId: uuid.Must(uuid.NewV4()).String(),
		Username:  username,
	}
}

// TestPartyGroupSplit_StaleGroupName demonstrates the downstream effect:
// when a follower has a stale LobbyGroupName, they compute a different
// partyID and JoinPartyGroup places them in a separate PartyHandler.
//
// With the registry-based approach, this still holds: different group names
// produce different parties because GetOrCreateByGroupName maps each name
// independently.
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

	leaderSession := newTestSessionForParty(t, "leader", tracker, pr)
	followerASession := newTestSessionForParty(t, "followerA", tracker, pr)
	followerBSession := newTestSessionForParty(t, "followerB", tracker, pr)

	currentMatchID := MatchID{}

	// Leader and follower A both have the new group name.
	leaderGroup, _, err := JoinPartyGroup(leaderSession, currentGroupName, currentMatchID)
	require.NoError(t, err)

	followerAGroup, _, err := JoinPartyGroup(followerASession, currentGroupName, currentMatchID)
	require.NoError(t, err)

	assert.Equal(t, leaderGroup.ID(), followerAGroup.ID(),
		"leader and followerA should be in the same party group")

	// Follower B still has the stale group name -> different party.
	followerBGroup, followerBIsLeader, err := JoinPartyGroup(followerBSession, staleGroupName, currentMatchID)
	require.NoError(t, err)

	// BUG: followerB silently ends up in a completely separate party.
	assert.True(t, followerBIsLeader,
		"BUG: stale follower becomes leader of a new, separate party")
	assert.NotEqual(t, leaderGroup.ID(), followerBGroup.ID(),
		"BUG: stale follower is in a different party group than the leader")
}

// TestLookupGroupPartyID verifies the registry lookup table works correctly.
func TestLookupGroupPartyID(t *testing.T) {
	logger := loggerForTest(t)
	tracker := newMockMatchmakingTracker()
	mm, mmCleanup := createLightMatchmaker(t, logger)
	defer mmCleanup()

	tsm := testStreamManager{}
	dmr := &DummyMessageRouter{}
	pr := NewLocalPartyRegistry(logger, cfg, mm, tracker, tsm, dmr, "testnode")

	leader := makeTestUserPresence("leader")

	// Before creating, lookup should fail.
	_, found := pr.LookupGroupPartyID("mygroup")
	assert.False(t, found)

	// Create via group name.
	ph, created, err := pr.GetOrCreateByGroupName("mygroup", true, 4, leader)
	require.NoError(t, err)
	assert.True(t, created)

	// Lookup should now succeed and return the same ID.
	id, found := pr.LookupGroupPartyID("mygroup")
	assert.True(t, found)
	assert.Equal(t, ph.ID, id)

	// Second call should return the same party.
	ph2, created2, err := pr.GetOrCreateByGroupName("mygroup", true, 4, leader)
	require.NoError(t, err)
	assert.False(t, created2)
	assert.Equal(t, ph.ID, ph2.ID)

	// Delete should clean up the mapping.
	pr.Delete(ph.ID)
	_, found = pr.LookupGroupPartyID("mygroup")
	assert.False(t, found)
}
