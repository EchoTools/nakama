package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"
)

// pendingMarkerRegistry is the duplicate-join script plus the join-marker lookup that LocalMatchRegistry
// provides. The test sets pending before each attempt it is about, the way the marker stands in
// production: absent before the first join, present while the game client has not yet connected.
type pendingMarkerRegistry struct {
	*dupJoinRegistry
	pending bool
	age     time.Duration
}

func (r *pendingMarkerRegistry) JoinMarkerAge(uuid.UUID, uuid.UUID) (time.Duration, bool) {
	return r.age, r.pending
}

// pendingJoinFixture is one match with one entrant on the given client session, and the seat the
// match grants that entrant.
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

// A duplicate join while the seat's join marker is still pending is not answered. The game client is still
// joining on the LobbySessionSuccess it already holds, and a second one would carry keys the game server
// never received. The hold is logged with the marker's age.
func TestDuplicateJoinWhileMarkerPendingSendsNothing(t *testing.T) {
	tracker := newMockMatchmakingTracker()
	client := capturingSession(t, tracker)
	server := capturingSession(t, tracker)
	label, entrant, granted, labelJSON := pendingJoinFixture(t, client)
	registry := &pendingMarkerRegistry{dupJoinRegistry: &dupJoinRegistry{mockFollowMatchRegistry: newMockFollowMatchRegistry(), answers: []func() (bool, bool, bool, string, string){
		func() (bool, bool, bool, string, string) { return true, true, true, string(granted), string(labelJSON) },
		func() (bool, bool, bool, string, string) {
			return true, false, false, ErrJoinRejectReasonDuplicateJoin.Error(), ""
		},
	}}}

	// The first join has no marker before it: the seat is granted and both sides are sent the success.
	require.NoError(t, LobbyJoinEntrants(loggerForTest(t), registry, tracker, client, server, label, entrant))
	require.Len(t, drain(client.outgoingCh), 1, "the game client is sent the success for the granted seat")
	require.NotEmpty(t, drain(server.outgoingCh))

	// The game client asks again while its marker is still pending.
	registry.pending, registry.age = true, 2500*time.Millisecond
	logger, logs := newPingLogObserver()
	require.NoError(t, LobbyJoinEntrants(logger, registry, tracker, client, server, label, entrant))
	require.Empty(t, drain(client.outgoingCh), "a duplicate join with a pending marker is not answered")
	require.Empty(t, drain(server.outgoingCh), "nothing goes to the game server either")

	held := logs.FilterMessage("Join marker pending; not sending a second LobbySessionSuccess").All()
	require.Len(t, held, 1, "the hold is logged once")
	assert.Equal(t, zapcore.InfoLevel, held[0].Level)
	assert.Equal(t, int64(2500), held[0].ContextMap()["marker_age_ms"])
}

// Two joins racing for one seat: the attempt is granted while the entrant's marker from an earlier attempt
// is still pending. The earlier attempt's success is the one whose keys the game server holds, so this one
// is not sent to either side.
func TestGrantedJoinWhileMarkerPendingSendsNothing(t *testing.T) {
	tracker := newMockMatchmakingTracker()
	client := capturingSession(t, tracker)
	server := capturingSession(t, tracker)
	label, entrant, granted, labelJSON := pendingJoinFixture(t, client)
	registry := &pendingMarkerRegistry{dupJoinRegistry: &dupJoinRegistry{mockFollowMatchRegistry: newMockFollowMatchRegistry(), answers: []func() (bool, bool, bool, string, string){
		func() (bool, bool, bool, string, string) { return true, true, true, string(granted), string(labelJSON) },
	}}, pending: true, age: 300 * time.Millisecond}

	logger, logs := newPingLogObserver()
	require.NoError(t, LobbyJoinEntrants(logger, registry, tracker, client, server, label, entrant))
	require.Empty(t, drain(client.outgoingCh), "a second success is not sent to the game client")
	require.Empty(t, drain(server.outgoingCh), "a second success is not sent to the game server")
	require.Len(t, logs.FilterMessage("Join marker pending; not sending a second LobbySessionSuccess").All(), 1)
}
