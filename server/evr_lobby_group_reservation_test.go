package server

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
)

// ===========================================================================
// Party-reservation unification (production: group-name parties only)
// ===========================================================================
//
// Real clients form parties via LobbyGroupName -> JoinPartyGroup, which never
// set params.currentPartyID. That left the signal-based reservation machinery
// dead for the only party type in use. These tests pin the unification:
//   - A (D2): JoinPartyGroup populates params.currentPartyID (BAC-1).
//   - E1 gate resolves for group parties once currentPartyID is set (BAC-2).
//   - D (E2): a member joining a party whose leader is already in a social
//     lobby gets a reservation via createReservationForNewPartyMember (BAC-3).
//   - Disconnected members are skipped, not mis-seated (BAC-4b).
//   - E1+E2 for one member yields exactly one reservation, incl. reconnect,
//     inherited from #512's UserID-keyed dedup (BAC-5).
//   - Matchmaker-cancel by any group member cancels all members (BAC-6).
//
// Reconciliation note: the E2 dispatch lives in configureParty (plan step D1),
// not in JoinPartyGroup (PR #511's draft site). These tests exercise the
// function directly (T3) plus the configureParty wiring, reusing #511's
// recording-registry harness.

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

// partyStreamTracker extends mockMatchmakingTracker with a real ListByStream
// over its tracked presences (the base testTracker returns nil), so tests can
// drive the party-stream member enumeration in createPartyReservations and
// lobbyPendingSessionCancel.
type partyStreamTracker struct {
	*mockMatchmakingTracker
}

func newPartyStreamTracker() *partyStreamTracker {
	return &partyStreamTracker{mockMatchmakingTracker: newMockMatchmakingTracker()}
}

func (t *partyStreamTracker) ListByStream(stream PresenceStream, _ bool, _ bool) []*Presence {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var out []*Presence
	for key, meta := range t.presences {
		// Match on Mode+Subject so callers that rebuild the stream with the
		// node Label still resolve the party-stream presences.
		if key.stream.Mode != stream.Mode || key.stream.Subject != stream.Subject {
			continue
		}
		m := *meta
		out = append(out, &Presence{
			ID:     PresenceID{SessionID: key.sessionID, Node: "testnode"},
			UserID: key.userID,
			Meta:   m,
		})
	}
	return out
}

// reservationRecordingRegistry records SignalCreatePartyReservations payloads so
// a test can observe whether (and for whom) a reservation was created. It embeds
// mockFollowMatchRegistry for the GetMatch/SetMatch label plumbing MatchLabelByID
// needs.
type reservationRecordingRegistry struct {
	*mockFollowMatchRegistry

	mu      sync.Mutex
	signals []SignalCreatePartyReservationsPayload
}

func (r *reservationRecordingRegistry) Signal(_ context.Context, _ string, data string) (string, error) {
	env := SignalEnvelope{}
	if err := json.Unmarshal([]byte(data), &env); err == nil && env.OpCode == SignalCreatePartyReservations {
		payload := SignalCreatePartyReservationsPayload{}
		if err := json.Unmarshal(env.Payload, &payload); err == nil {
			r.mu.Lock()
			r.signals = append(r.signals, payload)
			r.mu.Unlock()
		}
	}
	return SignalResponse{Success: true}.String(), nil
}

func (r *reservationRecordingRegistry) reservedSessionIDs() []uuid.UUID {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []uuid.UUID
	for _, sig := range r.signals {
		for _, m := range sig.Members {
			out = append(out, m.SessionID)
		}
	}
	return out
}

func (r *reservationRecordingRegistry) signalCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.signals)
}

// sessionMapRegistry is defined in evr_lobby_follower_social_reservation_test.go.

// newPartyMemberSession builds a sessionWS whose context carries
// SessionParameters (so LoadParams/StoreParams work) and whose pipeline and
// registries are wired for the reservation path.
func newPartyMemberSession(t *testing.T, username string, tracker Tracker, pr PartyRegistry, ep *EvrPipeline) *sessionWS {
	t.Helper()

	params := &SessionParameters{
		xpID:    evr.EvrId{},
		profile: &EVRProfile{}, // non-nil: DisplayName() dereferences the pointer
	}
	baseCtx := context.WithValue(context.Background(), ctxSessionParametersKey{}, atomic.NewPointer(params))
	ctx, cancel := context.WithCancel(baseCtx)
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
	s.pipeline = &Pipeline{node: "testnode", tracker: tracker, partyRegistry: pr}
	s.evrPipeline = ep
	return s
}

// groupReservationEnv bundles the wired dependencies for a group-party
// reservation test with a promoted leader.
type groupReservationEnv struct {
	registry  *reservationRecordingRegistry
	tracker   *partyStreamTracker
	pr        PartyRegistry
	ep        *EvrPipeline
	sessions  *sessionMapRegistry
	ph        *PartyHandler
	leaderSID uuid.UUID
	leaderUID uuid.UUID
}

// mkGroupReservationEnv creates a party in the registry, promotes the leader
// (mirroring the party-stream Join callback that fires in production), and wires
// an EvrPipeline whose match registry records reservation signals.
func mkGroupReservationEnv(t *testing.T, groupName string) *groupReservationEnv {
	t.Helper()

	logger := loggerForTest(t)
	tracker := newPartyStreamTracker()
	mm, mmCleanup := createLightMatchmaker(t, logger)
	t.Cleanup(mmCleanup)

	tsm := testStreamManager{}
	dmr := &DummyMessageRouter{}
	pr := NewLocalPartyRegistry(logger, cfg, mm, tracker, tsm, dmr, "testnode")

	leaderSID := uuid.Must(uuid.NewV4())
	leaderUID := uuid.Must(uuid.NewV4())
	leaderUP := &rtapi.UserPresence{
		UserId:    leaderUID.String(),
		SessionId: leaderSID.String(),
		Username:  "leader",
	}
	ph, created, err := pr.GetOrCreateByGroupName(groupName, true, 4, leaderUP)
	require.NoError(t, err)
	require.True(t, created)

	// Promote the leader from expectedInitialLeader -> ph.leader, as the
	// party-stream Join callback does in production. createReservationForNewPartyMember
	// and createPartyReservations both read ph.leader / ph.members.
	ph.Join([]*Presence{{
		ID:     PresenceID{SessionID: leaderSID, Node: "testnode"},
		UserID: leaderUID,
		Meta:   PresenceMeta{Username: "leader"},
	}})

	registry := &reservationRecordingRegistry{mockFollowMatchRegistry: newMockFollowMatchRegistry()}
	sessions := &sessionMapRegistry{sessions: map[uuid.UUID]Session{}}

	ep := &EvrPipeline{
		node: "testnode",
		nk: &RuntimeGoNakamaModule{
			logger:          logger,
			matchRegistry:   registry,
			partyRegistry:   pr,
			tracker:         tracker,
			sessionRegistry: sessions,
			node:            "testnode",
		},
	}
	return &groupReservationEnv{
		registry: registry, tracker: tracker, pr: pr, ep: ep,
		sessions: sessions, ph: ph, leaderSID: leaderSID, leaderUID: leaderUID,
	}
}

// trackLeaderInMatch tracks the leader on the service stream with the given
// match ID as its status (what createReservationForNewPartyMember reads).
func (e *groupReservationEnv) trackLeaderInMatch(matchID MatchID) {
	e.tracker.Track(context.Background(), e.leaderSID,
		PresenceStream{Mode: StreamModeService, Subject: e.leaderSID, Label: StreamLabelMatchService},
		e.leaderUID,
		PresenceMeta{Status: matchID.String()})
}

func mkGroupSocialLabel(id MatchID, groupID uuid.UUID) *MatchLabel {
	gid := groupID
	return &MatchLabel{
		ID:          id,
		Open:        true,
		LobbyType:   PublicLobby,
		Mode:        evr.ModeSocialPublic,
		Level:       evr.LevelSocial,
		GroupID:     &gid,
		MaxSize:     SocialLobbyMaxSize,
		PlayerLimit: SocialLobbyMaxSize,
		Players:     make([]PlayerInfo, 0),
	}
}

// ---------------------------------------------------------------------------
// BAC-1 — JoinPartyGroup sets currentPartyID (D2 / step A)
// ---------------------------------------------------------------------------

func TestJoinPartyGroup_SetsCurrentPartyID(t *testing.T) {
	logger := loggerForTest(t)
	tracker := newMockMatchmakingTracker()
	mm, mmCleanup := createLightMatchmaker(t, logger)
	defer mmCleanup()
	tsm := testStreamManager{}
	dmr := &DummyMessageRouter{}
	pr := NewLocalPartyRegistry(logger, cfg, mm, tracker, tsm, dmr, "testnode")

	session := newPartyMemberSession(t, "leader", tracker, pr, nil)

	params, ok := LoadParams(session.Context())
	require.True(t, ok)
	require.Equal(t, uuid.Nil, params.currentPartyID, "precondition: currentPartyID starts nil")

	lobbyGroup, _, err := JoinPartyGroup(session, "monarch12", MatchID{})
	require.NoError(t, err)
	require.NotNil(t, lobbyGroup)

	params, ok = LoadParams(session.Context())
	require.True(t, ok)
	assert.Equal(t, lobbyGroup.ID(), params.currentPartyID,
		"JoinPartyGroup must populate currentPartyID with the registry party ID")
	assert.NotEqual(t, uuid.Nil, params.currentPartyID)
	assert.Equal(t, uint64(0), params.currentSNSPartyID,
		"currentSNSPartyID must stay 0 for group parties")

	// Idempotency: a repeat join re-assigns the same ph.ID (no-op).
	firstID := params.currentPartyID
	_, _, err = JoinPartyGroup(session, "monarch12", MatchID{})
	require.NoError(t, err)
	params, _ = LoadParams(session.Context())
	assert.Equal(t, firstID, params.currentPartyID, "repeat JoinPartyGroup must leave currentPartyID unchanged")
}

// ---------------------------------------------------------------------------
// BAC-2 — E1 gate resolves for group parties (D1 / E1)
// ---------------------------------------------------------------------------

func TestGroupParty_LeaderReservationGateResolves(t *testing.T) {
	logger := loggerForTest(t)
	tracker := newMockMatchmakingTracker()
	mm, mmCleanup := createLightMatchmaker(t, logger)
	defer mmCleanup()
	tsm := testStreamManager{}
	dmr := &DummyMessageRouter{}
	pr := NewLocalPartyRegistry(logger, cfg, mm, tracker, tsm, dmr, "testnode")

	groupName := "monarch12"
	leader := newPartyMemberSession(t, "leader", tracker, pr, nil)
	follower := newPartyMemberSession(t, "follower", tracker, pr, nil)

	leaderGroup, leaderIsLeader, err := JoinPartyGroup(leader, groupName, MatchID{})
	require.NoError(t, err)
	require.True(t, leaderIsLeader)
	_, followerIsLeader, err := JoinPartyGroup(follower, groupName, MatchID{})
	require.NoError(t, err)
	require.False(t, followerIsLeader)

	// The exact gate that lobbyEntrantConnected evaluates for the leader:
	//   LoadParams(...).currentPartyID != Nil && partyRegistry.Get(...) resolves
	//   && ph.leader.SessionId == leader.SessionId.
	params, ok := LoadParams(leader.Context())
	require.True(t, ok)
	require.NotEqual(t, uuid.Nil, params.currentPartyID, "leader currentPartyID must be set (gate precondition)")

	ph, ok := pr.Get(params.currentPartyID)
	require.True(t, ok, "party must resolve from the registry by currentPartyID")
	require.Equal(t, leaderGroup.ID(), ph.ID)

	// Promote the leader from expectedInitialLeader -> ph.leader, as the
	// party-stream Join callback does in production (the mock tracker does not
	// fire it). The gate reads ph.leader directly.
	ph.Join([]*Presence{{
		ID:     PresenceID{SessionID: leader.id, Node: "testnode"},
		UserID: leader.userID,
		Meta:   PresenceMeta{Username: "leader"},
	}})

	ph.RLock()
	phLeader := ph.leader
	ph.RUnlock()
	require.NotNil(t, phLeader)
	assert.Equal(t, leader.id.String(), phLeader.UserPresence.SessionId,
		"the registry leader must be this leader session — the gate would fire createPartyReservations")
}

// TestCreatePartyReservations_GroupParty_ReservesFollower proves the E1
// reservation-builder is party-type-agnostic: given a group party, it reserves
// the follower (and not the leader).
func TestCreatePartyReservations_GroupParty_ReservesFollower(t *testing.T) {
	env := mkGroupReservationEnv(t, "monarch12")

	follower := newPartyMemberSession(t, "follower", env.tracker, env.pr, env.ep)
	env.sessions.sessions[follower.id] = follower

	// Both leader and follower present on the party stream.
	env.tracker.Track(context.Background(), env.leaderSID, env.ph.Stream, env.leaderUID, PresenceMeta{Username: "leader"})
	env.tracker.Track(context.Background(), follower.id, env.ph.Stream, follower.userID, PresenceMeta{Username: "follower"})

	socialMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.ep.createPartyReservations(context.Background(), loggerForTest(t), socialMatchID, env.leaderSID, env.ph.ID)

	reserved := env.registry.reservedSessionIDs()
	assert.Contains(t, reserved, follower.id, "follower must be reserved")
	assert.NotContains(t, reserved, env.leaderSID, "leader must NOT be reserved (already in the match)")
}

// ---------------------------------------------------------------------------
// BAC-3 — E2 for a member joining after the leader settled (D1 / E2)
// ---------------------------------------------------------------------------

func TestCreateReservationForNewPartyMember_GroupParty(t *testing.T) {
	env := mkGroupReservationEnv(t, "monarch12")
	groupID := uuid.Must(uuid.NewV4())

	socialMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.trackLeaderInMatch(socialMatchID)
	env.registry.SetMatch(socialMatchID, mkGroupSocialLabel(socialMatchID, groupID))

	member := newPartyMemberSession(t, "joiner", env.tracker, env.pr, env.ep)

	env.ep.createReservationForNewPartyMember(context.Background(), loggerForTest(t), member, env.ph.ID)

	assert.Contains(t, env.registry.reservedSessionIDs(), member.id,
		"a member joining after the leader is in a social lobby must be reserved")
}

func TestCreateReservationForNewPartyMember_LeaderInArena_NoReservation(t *testing.T) {
	env := mkGroupReservationEnv(t, "monarch12")
	groupID := uuid.Must(uuid.NewV4())

	arenaMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.trackLeaderInMatch(arenaMatchID)
	arenaLabel := mkGroupSocialLabel(arenaMatchID, groupID)
	arenaLabel.Mode = evr.ModeArenaPublic // not social
	arenaLabel.Level = evr.LevelUnspecified
	env.registry.SetMatch(arenaMatchID, arenaLabel)

	member := newPartyMemberSession(t, "joiner", env.tracker, env.pr, env.ep)
	env.ep.createReservationForNewPartyMember(context.Background(), loggerForTest(t), member, env.ph.ID)

	assert.Equal(t, 0, env.registry.signalCount(),
		"no reservation when the leader is in a non-social (arena) match")
}

func TestCreateReservationForNewPartyMember_LeaderNotInLobby_NoReservation(t *testing.T) {
	env := mkGroupReservationEnv(t, "monarch12")
	// Deliberately do NOT track the leader on any service stream.

	member := newPartyMemberSession(t, "joiner", env.tracker, env.pr, env.ep)
	env.ep.createReservationForNewPartyMember(context.Background(), loggerForTest(t), member, env.ph.ID)

	assert.Equal(t, 0, env.registry.signalCount(),
		"no reservation when the leader is not in any match")
}

// TestConfigureParty_Follower_DispatchesReservation is the wiring assertion for
// step D1: configureParty dispatches createReservationForNewPartyMember for a
// non-leader. Drives configureParty as the follower and observes the recorded
// reservation signal (the dispatch is a goroutine, hence Eventually).
func TestConfigureParty_Follower_DispatchesReservation(t *testing.T) {
	env := mkGroupReservationEnv(t, "monarch12")
	groupID := uuid.Must(uuid.NewV4())

	socialMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.trackLeaderInMatch(socialMatchID)
	env.registry.SetMatch(socialMatchID, mkGroupSocialLabel(socialMatchID, groupID))

	follower := newPartyMemberSession(t, "follower", env.tracker, env.pr, env.ep)
	env.sessions.sessions[follower.id] = follower

	lobbyParams := &LobbySessionParameters{
		PartyGroupName: "monarch12",
		GroupID:        groupID,
		PartySize:      atomic.NewInt64(1),
	}

	_, _, isLeader, err := env.ep.configureParty(context.Background(), loggerForTest(t), follower, lobbyParams)
	require.NoError(t, err)
	require.False(t, isLeader, "the follower must not be the leader")

	require.Eventually(t, func() bool {
		for _, sid := range env.registry.reservedSessionIDs() {
			if sid == follower.id {
				return true
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond,
		"configureParty must dispatch a reservation for the joining follower")
}

// ---------------------------------------------------------------------------
// BAC-4 — spectator group-leave clears currentPartyID (D3 / step B2)
// ---------------------------------------------------------------------------

// Drives the real spectator branch of handleLobbySessionRequest. A social-mode
// spectator request runs LeavePartyStream + clearPartyParams then returns
// without touching db/nk, so it exercises step B2 end-to-end.
func TestSpectatorLeave_ClearsCurrentPartyID(t *testing.T) {
	logger := loggerForTest(t)
	tracker := newMockMatchmakingTracker()
	mm, mmCleanup := createLightMatchmaker(t, logger)
	defer mmCleanup()
	tsm := testStreamManager{}
	dmr := &DummyMessageRouter{}
	pr := NewLocalPartyRegistry(logger, cfg, mm, tracker, tsm, dmr, "testnode")

	session := newPartyMemberSession(t, "spectator", tracker, pr, nil)
	lobbyGroup, _, err := JoinPartyGroup(session, "monarch12", MatchID{})
	require.NoError(t, err)

	params, _ := LoadParams(session.Context())
	require.Equal(t, lobbyGroup.ID(), params.currentPartyID, "setup: in a group party")
	require.True(t, tracker.hasPresence(session.id, lobbyGroup.ph.Stream, session.userID),
		"setup: on the party stream")

	ep := &EvrPipeline{node: "testnode"}
	lobbyParams := &LobbySessionParameters{
		Role:      evr.TeamSpectator,
		Mode:      evr.ModeSocialPublic, // non arena/combat -> spectator error branch, no lobbyFindSpectate
		PartySize: atomic.NewInt64(1),
	}
	in := &evr.LobbyFindSessionRequest{}

	_ = ep.handleLobbySessionRequest(session.Context(), logger, session, in, lobbyParams)

	params, _ = LoadParams(session.Context())
	assert.Equal(t, uuid.Nil, params.currentPartyID,
		"spectator leave must clear currentPartyID (BAC-4)")
	assert.False(t, tracker.hasPresence(session.id, lobbyGroup.ph.Stream, session.userID),
		"spectator leave must remove the party stream presence")
}

// ---------------------------------------------------------------------------
// BAC-4b — a disconnected member is skipped, not mis-seated (D3 hook #2 / B3)
// ---------------------------------------------------------------------------

// Encodes B3's finding: createPartyReservations enumerates via the party stream
// and per-member session lookups; a member whose session is gone (registry
// returns nil) is skipped, never reading its post-close params.
func TestCreatePartyReservations_SkipsDisconnectedMember(t *testing.T) {
	env := mkGroupReservationEnv(t, "monarch12")

	// A member is still on the party stream but its session is gone from the
	// registry (disconnected): registry.Get returns nil for it.
	deadSID := uuid.Must(uuid.NewV4())
	deadUID := uuid.Must(uuid.NewV4())

	env.tracker.Track(context.Background(), env.leaderSID, env.ph.Stream, env.leaderUID, PresenceMeta{Username: "leader"})
	env.tracker.Track(context.Background(), deadSID, env.ph.Stream, deadUID, PresenceMeta{Username: "dead"})
	// env.sessions intentionally has no entry for deadSID.

	socialMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.ep.createPartyReservations(context.Background(), loggerForTest(t), socialMatchID, env.leaderSID, env.ph.ID)

	assert.NotContains(t, env.registry.reservedSessionIDs(), deadSID,
		"a disconnected member must NOT be reserved")
	assert.Equal(t, 0, env.registry.signalCount(),
		"no reservation signal when the only non-leader member is disconnected")
}

// ---------------------------------------------------------------------------
// BAC-5 — E1 + E2 for one member yields one reservation (idempotent, #512)
// ---------------------------------------------------------------------------

// Fires the create-reservation signal for one member three ways — the E1 shape
// (member in a Members slice), the E2 shape (single member), and a reconnect
// (new session ID, same user ID) — and asserts exactly one reservation. This is
// inherited from PR #512's UserID-keyed create-side dedup; the plan adds no
// dedup of its own.
func TestGroupParty_E1PlusE2_SingleReservation_WithReconnect(t *testing.T) {
	m := &EvrMatch{}
	state := newDedupTestState()

	userID := uuid.Must(uuid.NewV4())
	sidA := uuid.Must(uuid.NewV4())
	sidB := uuid.Must(uuid.NewV4())

	mk := func(sid uuid.UUID) *EvrMatchPresence {
		return &EvrMatchPresence{
			UserID: userID, SessionID: sid, RoleAlignment: evr.TeamSocial,
			Username: "M", DisplayName: "M", EvrID: evr.EvrId{PlatformCode: 1, AccountId: 1},
		}
	}

	// E1 shape: leader-connect reserves the member as part of a member set.
	state = signalCreatePartyReservations(t, m, state, mk(sidA))
	// E2 shape: on-join reserves the same member again (same session).
	state = signalCreatePartyReservations(t, m, state, mk(sidA))
	// Reconnect: same user, new session ID.
	state = signalCreatePartyReservations(t, m, state, mk(sidB))

	assert.Len(t, reservationsForUser(state, userID), 1,
		"E1+E2+reconnect for one member must yield exactly one reservation")
	assert.Equal(t, 1, state.ReservationCount, "ReservationCount must reflect one held seat")
	_, hasB := state.reservationMap[sidB.String()]
	assert.True(t, hasB, "the surviving reservation must be keyed under the latest (reconnect) session ID")
	_, hasA := state.reservationMap[sidA.String()]
	assert.False(t, hasA, "the stale session-A reservation must be reclaimed")
}

// ---------------------------------------------------------------------------
// BAC-6 — matchmaker-cancel by any group member cancels all members (F8)
// ---------------------------------------------------------------------------

func TestLobbyCancel_GroupParty_CancelsAllMembers(t *testing.T) {
	logger := loggerForTest(t)
	tracker := newPartyStreamTracker()
	mm, mmCleanup := createLightMatchmaker(t, logger)
	defer mmCleanup()
	tsm := testStreamManager{}
	dmr := &DummyMessageRouter{}
	pr := NewLocalPartyRegistry(logger, cfg, mm, tracker, tsm, dmr, "testnode")

	groupName := "monarch12"
	leader := newPartyMemberSession(t, "leader", tracker, pr, nil)
	follower := newPartyMemberSession(t, "follower", tracker, pr, nil)

	leaderGroup, leaderIsLeader, err := JoinPartyGroup(leader, groupName, MatchID{})
	require.NoError(t, err)
	require.True(t, leaderIsLeader)
	_, _, err = JoinPartyGroup(follower, groupName, MatchID{})
	require.NoError(t, err)

	// The follower is actively matchmaking.
	mmGroupID := uuid.Must(uuid.NewV4())
	followerMMStream := PresenceStream{Mode: StreamModeMatchmaking, Subject: mmGroupID}
	tracker.Track(context.Background(), follower.id, followerMMStream, follower.userID, PresenceMeta{Status: "matchmaking"})
	require.True(t, tracker.hasPresence(follower.id, followerMMStream, follower.userID), "setup: follower is matchmaking")

	sessions := &sessionMapRegistry{sessions: map[uuid.UUID]Session{
		leader.id:   leader,
		follower.id: follower,
	}}
	ep := &EvrPipeline{
		node: "testnode",
		nk: &RuntimeGoNakamaModule{
			logger:          logger,
			partyRegistry:   pr,
			tracker:         tracker,
			sessionRegistry: sessions,
			node:            "testnode",
		},
	}

	// The leader cancels matchmaking; per F8 the whole party is cancelled.
	require.NoError(t, ep.lobbyPendingSessionCancel(context.Background(), logger, leader, nil))

	assert.False(t, tracker.hasPresence(follower.id, followerMMStream, follower.userID),
		"a group member's cancel must cancel ALL members — the follower's matchmaking stream must be closed")
	_ = leaderGroup
}
