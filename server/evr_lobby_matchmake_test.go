package server

// Reproduces: "failed to add ticket: non-leader party member cannot submit matchmaking ticket"
// Logged at server/evr_lobby_session.go:126 as "Unexpected error while finding match".
//
// Root cause: when a non-leader party member is released to independent matchmaking
// in lobbyFind (TryFollowPartyLeader and pollFollowPartyLeader both fail), lobbyFind
// correctly sets lobbyParams.SetPartySize(1) but still passes the original non-nil
// lobbyGroup (where this session is NOT the leader) down to lobbyMatchMakeWithFallback,
// which forwards it to addTicket. addTicket sees lobbyGroup.Size() > 1 and rejects
// the ticket because the calling session is not the leader.
//
// The fix is to pass nil (or a solo-sized group) to lobbyMatchMakeWithFallback when
// the follower has been released to independent matchmaking.

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama/v3/server/evr"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	uatomic "go.uber.org/atomic"
)

// stubDB returns a non-nil *sql.DB that will fail gracefully (with an error,
// not a panic) when queries are attempted. Used to satisfy addTicket's
// LoadArchetypeStats call without requiring a live database.
func stubDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", "host=invalid-test-host-for-unit-tests user=nobody dbname=nobody")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// makeMatchmakeTestLobbyParams returns a minimal LobbySessionParameters suitable
// for calling addTicket. All atomic pointer fields are initialized to avoid nil panics.
func makeMatchmakeTestLobbyParams(userID, groupID uuid.UUID, mode evr.Symbol, partySize int64) *LobbySessionParameters {
	return &LobbySessionParameters{
		UserID:               userID,
		GroupID:              groupID,
		Mode:                 mode,
		PartySize:            uatomic.NewInt64(partySize),
		MatchmakingTimestamp: time.Now(),
		MatchmakingTimeout:   5 * time.Second,
		latencyHistory:       new(uatomic.Pointer[LatencyHistory]),
		unreachableServers:   new(uatomic.Pointer[UnreachableServers]),
	}
}

// makeMatchmakeTestParty constructs a LobbyGroup with two members where leaderSID is the
// leader. Pass mm=nil when the test does not need MatchmakerAdd to succeed (e.g. the
// non-leader test exits before that call). Pass a real Matchmaker for the leader test.
func makeMatchmakeTestParty(leaderSID, leaderUID, followerSID, followerUID uuid.UUID, mm Matchmaker) *LobbyGroup {
	leaderPresence := &rtapi.UserPresence{
		UserId:    leaderUID.String(),
		SessionId: leaderSID.String(),
		Username:  "leader",
	}

	ph := &PartyHandler{
		members:         NewPartyPresenceList(8),
		matchmaker:      mm,
		ctx:             context.Background(),
		ticketRebuildCh: make(chan struct{}, 1),
	}
	ph.leader = &PartyLeader{
		UserPresence: leaderPresence,
		PresenceID:   &PresenceID{SessionID: leaderSID, Node: "testnode"},
	}

	// Add both leader and follower to members so Size() == 2.
	_, _ = ph.members.Join([]*Presence{
		{
			ID:     PresenceID{SessionID: leaderSID, Node: "testnode"},
			UserID: leaderUID,
			Meta:   PresenceMeta{Username: "leader"},
		},
		{
			ID:     PresenceID{SessionID: followerSID, Node: "testnode"},
			UserID: followerUID,
			Meta:   PresenceMeta{Username: "follower"},
		},
	})

	return &LobbyGroup{name: "test_group", ph: ph}
}

// TestAddTicket_NonLeaderPartyMember_ReturnsError verifies that addTicket
// returns an error when the calling session is a non-leader member of a party
// (lobbyGroup.Size() > 1 but session is not the leader).
//
// This is the immediate mechanism for the production warn:
//
//	{"level":"warn","msg":"Unexpected error while finding match",
//	 "error":"failed to add ticket: non-leader party member cannot submit matchmaking ticket"}
//
// Observed request: echo_arena, Channel=00000000-0000-0000-0000-000000000000, solo entrant.
// The player has a PartyGroupName set, joined the party as a non-leader, both follow
// attempts failed, and was released to independent matchmaking — but the original
// lobbyGroup reference (with the real leader still in it) was forwarded to addTicket.
func TestAddTicket_NonLeaderPartyMember_ReturnsError(t *testing.T) {
	logger := loggerForTest(t)
	tracker := newMockMatchmakingTracker()
	mm, mmCleanup := createLightMatchmaker(t, logger)
	defer mmCleanup()

	leaderSID := uuid.Must(uuid.NewV4())
	leaderUID := uuid.Must(uuid.NewV4())
	followerSID := uuid.Must(uuid.NewV4())
	followerUID := uuid.Must(uuid.NewV4())
	groupID := uuid.Must(uuid.NewV4())

	// Party with 2 members where leaderSID is the leader.
	// mm=nil: the non-leader guard fires before MatchmakerAdd is ever reached.
	lobbyGroup := makeMatchmakeTestParty(leaderSID, leaderUID, followerSID, followerUID, nil)
	require.Equal(t, 2, lobbyGroup.Size())

	leader := lobbyGroup.GetLeader()
	require.NotNil(t, leader)
	require.Equal(t, leaderSID.String(), leader.SessionId)
	require.NotEqual(t, followerSID.String(), leader.SessionId, "follower must not be the leader")

	// Follower session — the one being released to independent matchmaking.
	followerSession := &sessionWS{}
	followerSession.id = followerSID
	followerSession.userID = followerUID
	followerSession.username = uatomic.NewString("follower")
	followerSession.matchmaker = mm
	followerSession.pipeline = &Pipeline{node: "testnode", tracker: tracker}

	lobbyParams := makeMatchmakeTestLobbyParams(followerUID, groupID, evr.ModeArenaPublic, 1)

	p := &EvrPipeline{
		node:   "testnode",
		config: cfg,
		db:     stubDB(t),
		nk: &RuntimeGoNakamaModule{
			tracker:       tracker,
			streamManager: testStreamManager{},
		},
	}

	ticketConfig := DefaultMatchmakerTicketConfigs[evr.ModeArenaPublic]

	// This is the bug: addTicket is called with the original lobbyGroup where
	// followerSession is NOT the leader. It must return the non-leader error.
	_, err := p.addTicket(context.Background(), logger, followerSession, lobbyParams, lobbyGroup, ticketConfig)
	require.Error(t, err, "addTicket must reject a non-leader party member")
	require.Contains(t, err.Error(), "non-leader party member cannot submit matchmaking ticket")
}

// TestAddTicket_SoloPlayer_NoNonLeaderError verifies that addTicket does NOT
// return a non-leader error for a solo player (lobbyGroup == nil). This is the
// control case confirming the check is gate-guarded by lobbyGroup.Size() > 1.
func TestAddTicket_SoloPlayer_NoNonLeaderError(t *testing.T) {
	logger := loggerForTest(t)
	tracker := newMockMatchmakingTracker()
	mm, mmCleanup := createLightMatchmaker(t, logger)
	defer mmCleanup()

	soloUID := uuid.Must(uuid.NewV4())
	soloSID := uuid.Must(uuid.NewV4())
	groupID := uuid.Must(uuid.NewV4())

	soloSession := &sessionWS{}
	soloSession.id = soloSID
	soloSession.userID = soloUID
	soloSession.username = uatomic.NewString("solo")
	soloSession.matchmaker = mm
	soloSession.pipeline = &Pipeline{node: "testnode", tracker: tracker}

	lobbyParams := makeMatchmakeTestLobbyParams(soloUID, groupID, evr.ModeArenaPublic, 1)

	p := &EvrPipeline{
		node:   "testnode",
		config: cfg,
		db:     stubDB(t),
		nk: &RuntimeGoNakamaModule{
			tracker:       tracker,
			streamManager: testStreamManager{},
		},
	}

	ticketConfig := DefaultMatchmakerTicketConfigs[evr.ModeArenaPublic]

	// nil lobbyGroup = solo player. Must not hit the non-leader guard.
	_, err := p.addTicket(context.Background(), logger, soloSession, lobbyParams, nil, ticketConfig)
	if err != nil {
		require.NotContains(t, err.Error(), "non-leader party member cannot submit matchmaking ticket",
			"solo player must not hit the non-leader guard")
	}
}

// TestAddTicket_LeaderCanSubmitTicket verifies that the party leader is not
// blocked by the non-leader guard (lobbyGroup.Size() > 1, session == leader).
func TestAddTicket_LeaderCanSubmitTicket(t *testing.T) {
	logger := loggerForTest(t)
	tracker := newMockMatchmakingTracker()
	mm, mmCleanup := createLightMatchmaker(t, logger)
	defer mmCleanup()

	leaderSID := uuid.Must(uuid.NewV4())
	leaderUID := uuid.Must(uuid.NewV4())
	followerSID := uuid.Must(uuid.NewV4())
	followerUID := uuid.Must(uuid.NewV4())
	groupID := uuid.Must(uuid.NewV4())

	lobbyGroup := makeMatchmakeTestParty(leaderSID, leaderUID, followerSID, followerUID, mm)
	require.Equal(t, 2, lobbyGroup.Size())

	// Leader session.
	leaderSession := &sessionWS{}
	leaderSession.id = leaderSID
	leaderSession.userID = leaderUID
	leaderSession.username = uatomic.NewString("leader")
	leaderSession.matchmaker = mm
	leaderSession.pipeline = &Pipeline{node: "testnode", tracker: tracker}

	lobbyParams := makeMatchmakeTestLobbyParams(leaderUID, groupID, evr.ModeArenaPublic, 2)

	p := &EvrPipeline{
		node:   "testnode",
		config: cfg,
		db:     stubDB(t),
		nk: &RuntimeGoNakamaModule{
			tracker:       tracker,
			streamManager: testStreamManager{},
		},
	}

	ticketConfig := DefaultMatchmakerTicketConfigs[evr.ModeArenaPublic]

	_, err := p.addTicket(context.Background(), logger, leaderSession, lobbyParams, lobbyGroup, ticketConfig)
	// May fail for other reasons (e.g. MatchmakerAdd internals in test env).
	// Must NOT fail with the non-leader guard message.
	if err != nil {
		require.NotContains(t, err.Error(), "non-leader party member cannot submit matchmaking ticket",
			"leader must not be blocked by the non-leader guard")
	}
}
