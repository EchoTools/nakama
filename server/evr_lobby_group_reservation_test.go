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
// Group-party reservation unification (production: group-name parties only)
// ===========================================================================
//
// Real clients form parties via LobbyGroupName -> JoinPartyGroup, which never
// set params.currentPartyID. That left two reservation mechanisms dead for the
// only party type in use:
//
//  1. The leader-connect trigger in lobbyEntrantConnected fires only when the
//     connecting player's params.currentPartyID != uuid.Nil.
//  2. The matchmaker cancel path (lobbyPendingSessionCancel) cancels the whole
//     party only when params.currentPartyID != uuid.Nil.
//
// These tests pin the two entry points that unify group parties onto the
// existing reservation machinery:
//   - JoinPartyGroup populates params.currentPartyID.
//   - JoinPartyGroup creates a reservation for a member that joins AFTER the
//     leader is already sitting in a social lobby (the one-shot leader-connect
//     trigger cannot cover this member).

// reservationRecordingRegistry records SignalCreatePartyReservations payloads
// so a test can observe whether (and for whom) a reservation was created. It
// embeds mockFollowMatchRegistry for the GetMatch/SetMatch label plumbing that
// MatchLabelByID needs.
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

// reservedSessionIDs returns the flattened set of member session IDs across all
// recorded reservation signals.
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

// newPartyMemberSession builds a sessionWS whose context carries
// SessionParameters (so LoadParams/StoreParams work) and whose evrPipeline and
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
	s.pipeline.partyRegistry = pr
	s.evrPipeline = ep
	return s
}

// mkGroupReservationEnv creates a party in the registry, promotes the leader
// (mirroring the party-stream Join callback that fires in production when the
// leader is tracked on the party stream), and returns the wired EvrPipeline.
func mkGroupReservationEnv(t *testing.T, groupName string, leaderSID, leaderUID uuid.UUID) (*reservationRecordingRegistry, *mockMatchmakingTracker, PartyRegistry, *EvrPipeline) {
	t.Helper()

	logger := loggerForTest(t)
	tracker := newMockMatchmakingTracker()
	mm, mmCleanup := createLightMatchmaker(t, logger)
	t.Cleanup(mmCleanup)

	tsm := testStreamManager{}
	dmr := &DummyMessageRouter{}
	pr := NewLocalPartyRegistry(logger, cfg, mm, tracker, tsm, dmr, "testnode")

	leaderUP := &rtapi.UserPresence{
		UserId:    leaderUID.String(),
		SessionId: leaderSID.String(),
		Username:  "leader",
	}
	ph, created, err := pr.GetOrCreateByGroupName(groupName, true, 4, leaderUP)
	require.NoError(t, err)
	require.True(t, created)

	// Promote the leader from expectedInitialLeader -> ph.leader, as the
	// party-stream Join callback does in production when the leader is tracked
	// on the party stream. createReservationForNewPartyMember reads ph.leader.
	ph.Join([]*Presence{{
		ID:     PresenceID{SessionID: leaderSID, Node: "testnode"},
		UserID: leaderUID,
		Meta:   PresenceMeta{Username: "leader"},
	}})

	registry := &reservationRecordingRegistry{mockFollowMatchRegistry: newMockFollowMatchRegistry()}

	ep := &EvrPipeline{
		node: "testnode",
		nk: &RuntimeGoNakamaModule{
			logger:        logger,
			matchRegistry: registry,
			partyRegistry: pr,
			tracker:       tracker,
			node:          "testnode",
		},
	}
	return registry, tracker, pr, ep
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

// TestJoinPartyGroup_PopulatesCurrentPartyID pins entry point #1: after a
// group-name join, the session's params.currentPartyID equals the registry
// party ID. Before the fix this stayed uuid.Nil, so the leader-connect and
// matchmaker-cancel paths never engaged for group parties.
func TestJoinPartyGroup_PopulatesCurrentPartyID(t *testing.T) {
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

	lobbyGroup, _, err := JoinPartyGroup(session, "reservation_group", MatchID{})
	require.NoError(t, err)
	require.NotNil(t, lobbyGroup)

	params, ok = LoadParams(session.Context())
	require.True(t, ok)
	assert.Equal(t, lobbyGroup.ID(), params.currentPartyID,
		"JoinPartyGroup must populate currentPartyID with the registry party ID")
	assert.NotEqual(t, uuid.Nil, params.currentPartyID)
}

// TestJoinPartyGroup_LeaderInSocial_CreatesReservationForJoiner pins entry
// point #2: a member that joins the group AFTER the leader is already in a
// social lobby gets a reservation created for them in the leader's match.
func TestJoinPartyGroup_LeaderInSocial_CreatesReservationForJoiner(t *testing.T) {
	groupName := "reservation_group"
	leaderSID := uuid.Must(uuid.NewV4())
	leaderUID := uuid.Must(uuid.NewV4())
	groupID := uuid.Must(uuid.NewV4())

	registry, tracker, pr, ep := mkGroupReservationEnv(t, groupName, leaderSID, leaderUID)

	// Leader is sitting in a social lobby: service stream -> match, social label.
	socialMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	tracker.Track(context.Background(), leaderSID,
		PresenceStream{Mode: StreamModeService, Subject: leaderSID, Label: StreamLabelMatchService},
		leaderUID,
		PresenceMeta{Status: socialMatchID.String()})
	registry.SetMatch(socialMatchID, mkGroupSocialLabel(socialMatchID, groupID))

	member := newPartyMemberSession(t, "joiner", tracker, pr, ep)

	_, isLeader, err := JoinPartyGroup(member, groupName, MatchID{})
	require.NoError(t, err)
	require.False(t, isLeader, "the joining member must not be the leader")

	require.Eventually(t, func() bool {
		for _, sid := range registry.reservedSessionIDs() {
			if sid == member.id {
				return true
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond,
		"a reservation must be created for the member joining after the leader landed in a social lobby")
}

// TestJoinPartyGroup_LeaderInArena_NoReservation: no reservation when the
// leader is in a non-social (arena) match.
func TestJoinPartyGroup_LeaderInArena_NoReservation(t *testing.T) {
	groupName := "reservation_group"
	leaderSID := uuid.Must(uuid.NewV4())
	leaderUID := uuid.Must(uuid.NewV4())
	groupID := uuid.Must(uuid.NewV4())

	registry, tracker, pr, ep := mkGroupReservationEnv(t, groupName, leaderSID, leaderUID)

	arenaMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	tracker.Track(context.Background(), leaderSID,
		PresenceStream{Mode: StreamModeService, Subject: leaderSID, Label: StreamLabelMatchService},
		leaderUID,
		PresenceMeta{Status: arenaMatchID.String()})
	arenaLabel := mkGroupSocialLabel(arenaMatchID, groupID)
	arenaLabel.Mode = evr.ModeArenaPublic // not social
	arenaLabel.Level = evr.LevelUnspecified
	registry.SetMatch(arenaMatchID, arenaLabel)

	member := newPartyMemberSession(t, "joiner", tracker, pr, ep)

	_, _, err := JoinPartyGroup(member, groupName, MatchID{})
	require.NoError(t, err)

	require.Never(t, func() bool { return registry.signalCount() > 0 },
		300*time.Millisecond, 20*time.Millisecond,
		"no reservation should be created when the leader is in a non-social match")
}

// TestJoinPartyGroup_LeaderNotInLobby_NoReservation: no reservation when the
// leader is not in any match (matchmaking / at menu).
func TestJoinPartyGroup_LeaderNotInLobby_NoReservation(t *testing.T) {
	groupName := "reservation_group"
	leaderSID := uuid.Must(uuid.NewV4())
	leaderUID := uuid.Must(uuid.NewV4())

	registry, tracker, pr, ep := mkGroupReservationEnv(t, groupName, leaderSID, leaderUID)
	// Deliberately do NOT track the leader on any service stream.

	member := newPartyMemberSession(t, "joiner", tracker, pr, ep)

	_, _, err := JoinPartyGroup(member, groupName, MatchID{})
	require.NoError(t, err)

	require.Never(t, func() bool { return registry.signalCount() > 0 },
		300*time.Millisecond, 20*time.Millisecond,
		"no reservation should be created when the leader is not in any match")
}
