package server

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// ---------------------------------------------------------------------------
// recordedJoinAttempt captures calls to MatchRegistry.JoinAttempt for
// assertion in tests.
// ---------------------------------------------------------------------------

type recordedJoinAttempt struct {
	MatchID   uuid.UUID
	Node      string
	UserID    uuid.UUID
	SessionID uuid.UUID
	Username  string
}

// ---------------------------------------------------------------------------
// mockReplayMatchRegistry — extends mockFollowMatchRegistry with:
//   - JoinAttempt recording (returns success)
//   - ListMatches stub (returns empty, no panic)
// ---------------------------------------------------------------------------

type mockReplayMatchRegistry struct {
	mockFollowMatchRegistry
	mu           sync.Mutex
	joinAttempts []recordedJoinAttempt
}

func newMockReplayMatchRegistry() *mockReplayMatchRegistry {
	return &mockReplayMatchRegistry{
		mockFollowMatchRegistry: mockFollowMatchRegistry{
			matches: make(map[string]*MatchLabel),
		},
	}
}

func (r *mockReplayMatchRegistry) JoinAttempt(
	_ context.Context, id uuid.UUID, node string,
	userID, sessionID uuid.UUID, username string,
	_ int64, _ map[string]string, _, _, _ string,
	_ map[string]string,
) (bool, bool, bool, string, string, []*MatchPresence) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.joinAttempts = append(r.joinAttempts, recordedJoinAttempt{
		MatchID:   id,
		Node:      node,
		UserID:    userID,
		SessionID: sessionID,
		Username:  username,
	})

	// Look up the label for this match so we can return it as the label string
	// (matches LobbyJoinEntrants expectations).
	r.mockFollowMatchRegistry.mu.RLock()
	matchKey := id.String() + "." + node
	label, ok := r.mockFollowMatchRegistry.matches[matchKey]
	r.mockFollowMatchRegistry.mu.RUnlock()
	var labelStr string
	if ok {
		data, _ := json.Marshal(label)
		labelStr = string(data)
	} else {
		labelStr = "{}"
	}
	return true, true, true, "", labelStr, nil
}

func (r *mockReplayMatchRegistry) ListMatches(
	_ context.Context, _ int, _ *wrapperspb.BoolValue,
	_ *wrapperspb.StringValue, _ *wrapperspb.Int32Value,
	_ *wrapperspb.Int32Value, _ *wrapperspb.StringValue,
	_ *wrapperspb.StringValue,
) ([]*api.Match, []string, error) {
	return nil, nil, nil
}

// getJoinAttempts returns a snapshot of recorded join attempts.
func (r *mockReplayMatchRegistry) getJoinAttempts() []recordedJoinAttempt {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedJoinAttempt, len(r.joinAttempts))
	copy(out, r.joinAttempts)
	return out
}

// ---------------------------------------------------------------------------
// mockReplaySessionRegistry — extends testSessionRegistry with a Get that
// returns pre-registered sessions.
// ---------------------------------------------------------------------------

type mockReplaySessionRegistry struct {
	testSessionRegistry
	mu       sync.RWMutex
	sessions map[uuid.UUID]Session
}

func newMockReplaySessionRegistry() *mockReplaySessionRegistry {
	return &mockReplaySessionRegistry{
		sessions: make(map[uuid.UUID]Session),
	}
}

func (r *mockReplaySessionRegistry) Get(sessionID uuid.UUID) Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if s, ok := r.sessions[sessionID]; ok {
		return s
	}
	return nil
}

func (r *mockReplaySessionRegistry) Add(session Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[session.ID()] = session
}

// ---------------------------------------------------------------------------
// replayTestEnv — test harness for log-replay pipeline tests
// ---------------------------------------------------------------------------

type replayTestEnv struct {
	t *testing.T

	pipeline        *EvrPipeline
	tracker         *mockMatchmakingTracker
	matchRegistry   *mockReplayMatchRegistry
	sessionRegistry *mockReplaySessionRegistry

	sessions         map[string]*sessionWS        // keyed by player name
	capturedMessages map[uuid.UUID][]evr.Message   // keyed by session ID
	mu               sync.Mutex                    // protects capturedMessages
}

func newReplayTestEnv(t *testing.T) *replayTestEnv {
	t.Helper()

	tracker := newMockMatchmakingTracker()
	matchRegistry := newMockReplayMatchRegistry()
	sessionRegistry := newMockReplaySessionRegistry()

	nk := &RuntimeGoNakamaModule{
		matchRegistry:   matchRegistry,
		tracker:         tracker,
		sessionRegistry: sessionRegistry,
		metrics:         &testMetrics{},
		node:            "testnode",
	}

	pipeline := &EvrPipeline{
		node:   "testnode",
		nk:     nk,
		logger: zap.NewNop(),
	}

	// Prevent panics in LobbyJoinEntrants when checking globalAppBot.
	globalAppBot.Store(nil)

	return &replayTestEnv{
		t:                t,
		pipeline:         pipeline,
		tracker:          tracker,
		matchRegistry:    matchRegistry,
		sessionRegistry:  sessionRegistry,
		sessions:         make(map[string]*sessionWS),
		capturedMessages: make(map[uuid.UUID][]evr.Message),
	}
}

// newTestSession creates a *sessionWS with the minimum fields needed for
// findReservation, lobbyFind helpers, and sendEvrHook capture.
func (env *replayTestEnv) newTestSession(name string, userID, sessionID uuid.UUID) *sessionWS {
	env.t.Helper()

	logger := zap.NewNop()
	ctx, cancel := context.WithCancel(context.Background())

	// Set up session parameters in context so LoadParams works.
	paramsPtr := &atomic.Pointer[SessionParameters]{}
	params := &SessionParameters{
		node:           "testnode",
		MatchLifecycle: NewPlayerMatchLifecycle(logger),
	}
	paramsPtr.Store(params)
	ctx = context.WithValue(ctx, ctxSessionParametersKey{}, paramsPtr)

	username := atomic.NewString(name)

	s := &sessionWS{
		id:          sessionID,
		userID:      userID,
		logger:      logger,
		username:    username,
		ctx:         ctx,
		ctxCancelFn: cancel,
		pipeline:    &Pipeline{node: "testnode", tracker: env.tracker},
		evrPipeline: env.pipeline,
	}

	// Wire up sendEvrHook to capture messages.
	s.sendEvrHook = func(messages []evr.Message) {
		env.mu.Lock()
		defer env.mu.Unlock()
		env.capturedMessages[sessionID] = append(env.capturedMessages[sessionID], messages...)
	}

	// Register in session registry so sessionRegistry.Get works.
	env.sessionRegistry.Add(s)

	// Store by name for test convenience.
	env.sessions[name] = s

	env.t.Cleanup(func() { cancel() })
	return s
}

// getCapturedMessages returns a copy of the captured messages for a session.
func (env *replayTestEnv) getCapturedMessages(sessionID uuid.UUID) []evr.Message {
	env.mu.Lock()
	defer env.mu.Unlock()
	msgs := env.capturedMessages[sessionID]
	out := make([]evr.Message, len(msgs))
	copy(out, msgs)
	return out
}

// ---------------------------------------------------------------------------
// Phase 2a: Smoke test — verify the harness works
// ---------------------------------------------------------------------------

// TestPipelineReplay_SoloSocialJoin verifies the sendEvrHook capture mechanism
// and the replayTestEnv harness with a solo player scenario. The full lobbyFind
// end-to-end path is blocked by configureParty (needs PartyRegistry) and
// lobbyAuthorize (needs GuildGroupRegistry) — both require infrastructure that
// is not mockable without additional production changes. This test proves the
// harness captures messages correctly, which is the foundation for all Phase 2
// pipeline tests.
//
// Blockers for full lobbyFind end-to-end:
//   - configureParty: calls JoinPartyGroup which needs PartyRegistry (server runtime)
//   - lobbyAuthorize: needs GuildGroupRegistry.Get (concrete type, not mockable)
//   - CheckServerPing: needs StreamUserList (full Nakama runtime)
//   - JoinMatchmakingStream: needs s.matchmaker (Matchmaker interface)
func TestPipelineReplay_SoloSocialJoin(t *testing.T) {
	env := newReplayTestEnv(t)
	playerUID := uuid.Must(uuid.NewV4())
	playerSID := uuid.Must(uuid.NewV4())
	session := env.newTestSession("solo_player", playerUID, playerSID)

	// SendEvr will fail on marshal/send (no websocket) but the hook fires
	// before any of that. Call the hook directly to be safe.
	testMessages := []evr.Message{
		evr.NewSTcpConnectionUnrequireEvent(),
	}
	session.sendEvrHook(testMessages)

	captured := env.getCapturedMessages(playerSID)
	require.Len(t, captured, 1, "Expected exactly 1 captured message")
	assert.IsType(t, &evr.STcpConnectionUnrequireEvent{}, captured[0])
}

// TestPipelineReplay_FindReservation_SoloNoParty verifies that findReservation
// returns false for a solo player (no lobby group / no leader).
func TestPipelineReplay_FindReservation_SoloNoParty(t *testing.T) {
	env := newReplayTestEnv(t)
	playerUID := uuid.Must(uuid.NewV4())
	playerSID := uuid.Must(uuid.NewV4())
	session := env.newTestSession("solo", playerUID, playerSID)

	// No party group → findReservation should return (nil, false) because
	// lobbyGroup.GetLeader() returns nil.
	lobbyGroup := &LobbyGroup{ph: &PartyHandler{}}
	matchID, found := env.pipeline.findReservation(context.Background(), zap.NewNop(), session, lobbyGroup)
	assert.False(t, found, "Solo player should not find a reservation")
	assert.True(t, matchID.IsNil(), "MatchID should be nil for solo player")
}

// ---------------------------------------------------------------------------
// Phase 2b: BigDuckII duo party follow — reservation-based convergence
// ---------------------------------------------------------------------------

// TestPipelineReplay_BigDuckII_FollowerNotStuck reproduces the bigduckii
// infinite matchmaking bug and verifies the new reservation system prevents it.
//
// Setup:
//   - Two players: player_A (follower/bigduckii) and player_B (leader/the_noodle_of_doom)
//   - Both in party "code_noodle"
//   - Both were in arena match (match-00001); leader has already transitioned to social
//   - Leader's service stream points to a social lobby (match-00002)
//   - Player_A's service stream still points to the arena match (stale — the bug trigger)
//   - The social lobby has a reservation for player_A (created by leader's lobbyEntrantConnected)
//
// Test:
//   1. findReservation for player_A finds the reservation in the social lobby
//
// Assert:
//   - findReservation returns the social lobby match ID (not the arena match)
//   - The follower would be directed to the social lobby via the reservation path
//
// Note: Full lobbyFind end-to-end is blocked by configureParty (needs party
// registry) and lobbyAuthorize (needs GuildGroupRegistry). These are documented
// blockers. The test exercises the critical code path (findReservation) that
// prevents the infinite matchmaking loop.
func TestPipelineReplay_BigDuckII_FollowerNotStuck(t *testing.T) {
	env := newReplayTestEnv(t)

	// Player identities — anonymized from production logs.
	leaderUID := uuid.Must(uuid.NewV4())  // the_noodle_of_doom
	leaderSID := uuid.Must(uuid.NewV4())
	followerUID := uuid.Must(uuid.NewV4()) // bigduckii
	followerSID := uuid.Must(uuid.NewV4())
	groupID := uuid.Must(uuid.NewV4())

	arenaMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	socialMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	_ = env.newTestSession("player_B", leaderUID, leaderSID)
	followerSession := env.newTestSession("player_A", followerUID, followerSID)

	// --- Tracker state ---

	// Leader's service stream → social lobby (leader already transitioned).
	env.tracker.Track(context.Background(), leaderSID,
		PresenceStream{Mode: StreamModeService, Subject: leaderSID, Label: StreamLabelMatchService},
		leaderUID,
		PresenceMeta{Status: socialMatchID.String()},
	)

	// Follower's service stream → arena match (STALE — the bug trigger).
	env.tracker.Track(context.Background(), followerSID,
		PresenceStream{Mode: StreamModeService, Subject: followerSID, Label: StreamLabelMatchService},
		followerUID,
		PresenceMeta{Status: arenaMatchID.String()},
	)

	// --- Match registry ---

	// Arena match: closed, both players listed.
	arenaLabel := &MatchLabel{
		ID:          arenaMatchID,
		Open:        false,
		Mode:        evr.ModeArenaPublic,
		PlayerLimit: 8,
		Players: []PlayerInfo{
			{UserID: leaderUID.String(), SessionID: leaderSID.String()},
			{UserID: followerUID.String(), SessionID: followerSID.String()},
		},
		GroupID: &groupID,
	}
	env.matchRegistry.SetMatch(arenaMatchID, arenaLabel)

	// Social lobby: open, leader present, follower has a RESERVATION.
	socialLabel := &MatchLabel{
		ID:          socialMatchID,
		Open:        true,
		Mode:        evr.ModeSocialPublic,
		PlayerLimit: 12,
		Players: []PlayerInfo{
			{UserID: leaderUID.String(), SessionID: leaderSID.String()},
			{
				UserID:        followerUID.String(),
				SessionID:     followerSID.String(),
				IsReservation: true, // Created by leader's lobbyEntrantConnected
			},
		},
		GroupID: &groupID,
	}
	env.matchRegistry.SetMatch(socialMatchID, socialLabel)

	// --- Party state ---
	ph := &PartyHandler{members: NewPartyPresenceList(8)}
	ph.leader = &PartyLeader{
		UserPresence: &rtapi.UserPresence{
			UserId:    leaderUID.String(),
			SessionId: leaderSID.String(),
			Username:  "player_B",
		},
		PresenceID: &PresenceID{SessionID: leaderSID, Node: "testnode"},
	}
	lobbyGroup := &LobbyGroup{ph: ph}

	// --- Test: findReservation should find the reservation in the social lobby ---
	reservationMatchID, found := env.pipeline.findReservation(
		context.Background(), loggerForTest(t), followerSession, lobbyGroup)

	require.True(t, found,
		"findReservation should find the reservation in leader's social lobby")
	assert.Equal(t, socialMatchID.UUID, reservationMatchID.UUID,
		"Reservation should point to the social lobby, not the arena match")
	assert.Equal(t, socialMatchID.Node, reservationMatchID.Node)

}

// ---------------------------------------------------------------------------
// Harness validation tests
// ---------------------------------------------------------------------------

// TestPipelineReplay_FindReservation_LeaderIsCallerSkips verifies that
// findReservation correctly returns false when the caller IS the leader
// (guards against self-follow).
func TestPipelineReplay_FindReservation_LeaderIsCallerSkips(t *testing.T) {
	env := newReplayTestEnv(t)

	leaderUID := uuid.Must(uuid.NewV4())
	leaderSID := uuid.Must(uuid.NewV4())

	leaderSession := env.newTestSession("leader", leaderUID, leaderSID)

	// Set up lobby group where the leader is the caller.
	ph := &PartyHandler{
		members: NewPartyPresenceList(8),
	}
	ph.leader = &PartyLeader{
		UserPresence: &rtapi.UserPresence{
			UserId:    leaderUID.String(),
			SessionId: leaderSID.String(),
			Username:  "leader",
		},
		PresenceID: &PresenceID{SessionID: leaderSID, Node: "testnode"},
	}
	lobbyGroup := &LobbyGroup{ph: ph}

	matchID, found := env.pipeline.findReservation(context.Background(), zap.NewNop(), leaderSession, lobbyGroup)

	assert.False(t, found, "Leader calling findReservation for self should return false")
	assert.True(t, matchID.IsNil())
}

// TestPipelineReplay_MockRegistryJoinAttempt verifies that the mock match
// registry's JoinAttempt implementation correctly records calls and returns
// success, proving the harness is ready for end-to-end LobbyJoinEntrants
// testing in future phases.
func TestPipelineReplay_MockRegistryJoinAttempt(t *testing.T) {
	registry := newMockReplayMatchRegistry()

	matchUUID := uuid.Must(uuid.NewV4())
	userID := uuid.Must(uuid.NewV4())
	sessionID := uuid.Must(uuid.NewV4())

	found, allowed, isNew, reason, labelStr, presences := registry.JoinAttempt(
		context.Background(), matchUUID, "testnode",
		userID, sessionID, "testuser",
		0, nil, "", "", "", nil,
	)

	assert.True(t, found)
	assert.True(t, allowed)
	assert.True(t, isNew)
	assert.Empty(t, reason)
	assert.Equal(t, "{}", labelStr)
	assert.Nil(t, presences)

	attempts := registry.getJoinAttempts()
	require.Len(t, attempts, 1)
	assert.Equal(t, matchUUID, attempts[0].MatchID)
	assert.Equal(t, userID, attempts[0].UserID)
	assert.Equal(t, sessionID, attempts[0].SessionID)
	assert.Equal(t, "testuser", attempts[0].Username)
}
