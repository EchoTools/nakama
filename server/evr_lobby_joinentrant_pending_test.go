package server

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const joinHeldForMarkerMessage = "Join marker pending; not sending a second LobbySessionSuccess"

// pendingMarkerRegistry is the join script plus the join-marker lookup that LocalMatchRegistry provides.
// The test sets pending before each attempt it is about, the way the marker stands in production: absent
// before the first join, present while the game client has not yet connected.
type pendingMarkerRegistry struct {
	*dupJoinRegistry
	pending bool
	age     time.Duration
}

func (r *pendingMarkerRegistry) JoinMarkerAge(uuid.UUID, uuid.UUID) (time.Duration, bool) {
	return r.age, r.pending
}

// pendingJoinFixture is one match with one entrant on the given client session, and the seat the match
// grants that entrant.
func pendingJoinFixture(t *testing.T, client *sessionWS) (*MatchLabel, *EvrMatchPresence, []byte, []byte) {
	t.Helper()
	matchUUID := uuid.Must(uuid.NewV4())
	groupID := uuid.Must(uuid.NewV4())
	label := &MatchLabel{ID: MatchID{UUID: matchUUID, Node: "testnode"}, Mode: evr.ModeArenaPublic, GroupID: &groupID,
		GameServer: &GameServerPresence{}, RequiredFeatures: []string{}}
	labelJSON, err := json.Marshal(label)
	require.NoError(t, err)
	entrant := &EvrMatchPresence{SessionID: client.id, UserID: client.userID, Node: "testnode",
		EvrID: evr.EvrId{PlatformCode: 4, AccountId: 900000000000000202}, RoleAlignment: evr.TeamBlue,
		EntrantID: uuid.Must(uuid.NewV4())}
	granted, err := json.Marshal(entrant)
	require.NoError(t, err)
	return label, entrant, granted, labelJSON
}

// pendingJoinRegistry answers the grant, then the given follow-up attempts, from a script.
func pendingJoinRegistry(answers ...func() (bool, bool, bool, string, string)) *pendingMarkerRegistry {
	return &pendingMarkerRegistry{dupJoinRegistry: &dupJoinRegistry{mockFollowMatchRegistry: newMockFollowMatchRegistry(), answers: answers}}
}

// heldPlacementSessions resolves the game server session a backfill placement sends to, which is the only
// lookup the backfill executor makes in its session registry.
type heldPlacementSessions struct {
	SessionRegistry
	server Session
}

func (r heldPlacementSessions) Get(uuid.UUID) Session { return r.server }

// placeByBackfill runs the backfill executor's join of client into label's match, the path a periodic
// backfill cycle takes. The client carries the login parameters an entrant presence is built from.
func placeByBackfill(t *testing.T, logger *zap.Logger, registry MatchRegistry, tracker Tracker, client, server *sessionWS, label *MatchLabel) int {
	t.Helper()
	login := capturingSession(t, tracker)
	params := &SessionParameters{node: "testnode", loginSession: login, profile: &EVRProfile{},
		xpID: evr.EvrId{PlatformCode: 4, AccountId: 900000000000000303}}
	ptr := &atomic.Pointer[SessionParameters]{}
	ptr.Store(params)
	client.ctx = context.WithValue(context.Background(), ctxSessionParametersKey{}, ptr)
	client.username = atomic.NewString("entrant")

	b := &PostMatchmakerBackfill{logger: loggerForTest(t), sessionRegistry: heldPlacementSessions{server: server},
		matchRegistry: registry, tracker: tracker, metrics: &testMetrics{}}
	candidate := &BackfillCandidate{Mode: label.Mode, Entries: []*MatchmakerEntry{{
		Presence:          &MatchmakerPresence{SessionId: client.id.String(), UserId: client.userID.String()},
		NumericProperties: map[string]float64{"rating_mu": 25, "rating_sigma": 8},
	}}}
	result := &BackfillResult{Candidate: candidate, Match: &BackfillMatch{Label: label,
		OpenSlots: map[int]int{evr.TeamBlue: 1}, TeamCounts: map[int]int{}}, Team: evr.TeamBlue}
	return b.executeBackfillResultOptimized(context.Background(), logger, result, []Session{client}, label.GroupID.String())
}

// A backfill cycle that re-places an entrant whose join marker is still pending sends nothing, to either
// side. The seat was granted by an ordinary join, which sent the success.
func TestBackfillReplacementWhileMarkerPendingSendsNothing(t *testing.T) {
	tracker := newMockMatchmakingTracker()
	client := capturingSession(t, tracker)
	server := capturingSession(t, tracker)
	label, entrant, granted, labelJSON := pendingJoinFixture(t, client)
	registry := pendingJoinRegistry(
		func() (bool, bool, bool, string, string) { return true, true, true, string(granted), string(labelJSON) },
		func() (bool, bool, bool, string, string) {
			return true, false, false, ErrJoinRejectReasonDuplicateJoin.Error(), ""
		},
	)
	require.NoError(t, LobbyJoinEntrants(loggerForTest(t), registry, tracker, client, server, label, entrant))
	require.Len(t, drain(client.outgoingCh), 1)
	require.NotEmpty(t, drain(server.outgoingCh))

	registry.pending, registry.age = true, 2500*time.Millisecond
	logger, logs := newPingLogObserver()
	placeByBackfill(t, logger, registry, tracker, client, server, label)
	require.Empty(t, drain(client.outgoingCh), "a backfill re-placement with a pending marker is not answered")
	require.Empty(t, drain(server.outgoingCh), "nothing goes to the game server either")

	held := logs.FilterMessage(joinHeldForMarkerMessage).All()
	require.Len(t, held, 1, "the hold is logged once")
	assert.Equal(t, zapcore.InfoLevel, held[0].Level)
	assert.Equal(t, int64(2500), held[0].ContextMap()["marker_age_ms"])
}

// A backfill cycle whose grant lands while the entrant's marker is still pending (two joins racing for one
// seat) sends nothing either: the first success is the one whose keys the game server holds.
func TestBackfillGrantWhileMarkerPendingSendsNothing(t *testing.T) {
	tracker := newMockMatchmakingTracker()
	client := capturingSession(t, tracker)
	server := capturingSession(t, tracker)
	label, _, granted, labelJSON := pendingJoinFixture(t, client)
	registry := pendingJoinRegistry(
		func() (bool, bool, bool, string, string) { return true, true, true, string(granted), string(labelJSON) },
	)
	registry.pending, registry.age = true, 300*time.Millisecond

	logger, logs := newPingLogObserver()
	placeByBackfill(t, logger, registry, tracker, client, server, label)
	require.Empty(t, drain(client.outgoingCh), "a second success is not sent to the game client")
	require.Empty(t, drain(server.outgoingCh), "a second success is not sent to the game server")
	require.Len(t, logs.FilterMessage(joinHeldForMarkerMessage).All(), 1)
}

// A party member's duplicate join inside the marker window still gets the kept LobbySessionSuccess, byte for
// byte, to the game client only. Party follows are not held: the follower is answered as before.
func TestPartyDuplicateJoinWhileMarkerPendingGetsKeptSuccess(t *testing.T) {
	tracker := newMockMatchmakingTracker()
	client := capturingSession(t, tracker)
	server := capturingSession(t, tracker)
	label, entrant, granted, labelJSON := pendingJoinFixture(t, client)
	registry := pendingJoinRegistry(
		func() (bool, bool, bool, string, string) { return true, true, true, string(granted), string(labelJSON) },
		func() (bool, bool, bool, string, string) {
			return true, false, false, ErrJoinRejectReasonDuplicateJoin.Error(), ""
		},
	)
	require.NoError(t, LobbyJoinEntrants(loggerForTest(t), registry, tracker, client, server, label, entrant))
	first := drain(client.outgoingCh)
	require.Len(t, first, 1)
	drain(server.outgoingCh)

	registry.pending, registry.age = true, 2500*time.Millisecond
	logger, logs := newPingLogObserver()
	require.NoError(t, LobbyJoinEntrants(logger, registry, tracker, client, server, label, entrant))
	again := drain(client.outgoingCh)
	require.Len(t, again, 1, "the duplicate join inside the window is answered")
	require.True(t, bytes.Equal(first[0], again[0]), "the kept success is re-sent byte for byte")
	require.Empty(t, drain(server.outgoingCh), "the game server already has this success")
	require.Empty(t, logs.FilterMessage(joinHeldForMarkerMessage).All(), "a party join is not held")
}

// A granted join from any caller other than backfill, while the entrant's marker is pending, is sent as it
// was before the hold existed.
func TestOtherCallersGrantWhileMarkerPendingIsSent(t *testing.T) {
	tracker := newMockMatchmakingTracker()
	client := capturingSession(t, tracker)
	server := capturingSession(t, tracker)
	label, entrant, granted, labelJSON := pendingJoinFixture(t, client)
	registry := pendingJoinRegistry(
		func() (bool, bool, bool, string, string) { return true, true, true, string(granted), string(labelJSON) },
	)
	registry.pending, registry.age = true, 300*time.Millisecond

	logger, logs := newPingLogObserver()
	require.NoError(t, LobbyJoinEntrants(logger, registry, tracker, client, server, label, entrant))
	require.Len(t, drain(client.outgoingCh), 1, "the game client is sent the success")
	require.NotEmpty(t, drain(server.outgoingCh), "the game server is sent the success")
	require.Empty(t, logs.FilterMessage(joinHeldForMarkerMessage).All(), "a non-backfill join is not held")
}
