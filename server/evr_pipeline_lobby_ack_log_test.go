package server

import (
	"context"
	"testing"
	"time"

	rtapi "buf.build/gen/go/echotools/nevr-api/protocolbuffers/go/gameservice/v1"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
	"go.uber.org/zap/zapcore"
)

// markerAgeRegistry is the follow-test match registry plus the join-marker
// lookup that LocalMatchRegistry provides in production.
type markerAgeRegistry struct {
	*mockFollowMatchRegistry
	age time.Duration
}

func (r *markerAgeRegistry) JoinMarkerAge(uuid.UUID, uuid.UUID) (time.Duration, bool) {
	return r.age, true
}

// An accepted entrant ack is logged at Info with who was admitted, which game
// server acked, how long after the join attempt, and whether the matchmaking
// stream was untracked.
func TestLobbyEntrantConnected_LogsAcceptedAck(t *testing.T) {
	e := newPushEnv(t)
	e.ep.nk.matchRegistry = &markerAgeRegistry{mockFollowMatchRegistry: e.matches, age: 2500 * time.Millisecond}
	entrant := e.session("entrant", true)
	e.online(entrant)

	lobbyID := uuid.Must(uuid.NewV4())
	matchID, _ := NewMatchID(lobbyID, "testnode")
	e.matches.SetMatch(matchID, &MatchLabel{ID: matchID, Mode: evr.ModeArenaPublic})
	entrantID := uuid.Must(uuid.NewV4())
	presence := &EvrMatchPresence{
		Node:           "testnode",
		SessionID:      entrant.id,
		LoginSessionID: uuid.Must(uuid.NewV4()),
		UserID:         entrant.userID,
		EvrID:          evr.EvrId{PlatformCode: 4, AccountId: 42},
		Username:       "entrant",
		RoleAlignment:  evr.TeamSocial,
		EntrantID:      entrantID,
	}
	e.tracker.Track(context.Background(), entrant.id,
		PresenceStream{Mode: StreamModeEntrant, Subject: entrantID, Label: "testnode"}, entrant.userID,
		PresenceMeta{Status: presence.GetStatus()})

	gs := &sessionWS{}
	gs.id = uuid.Must(uuid.NewV4())
	gs.userID = uuid.Must(uuid.NewV4())
	gs.ctx = context.Background()
	gs.logger = loggerForTest(t)
	gs.format = SessionFormatProtobuf
	gs.outgoingCh = make(chan []byte, 16)
	gs.tracker = e.tracker
	gs.pipeline = &Pipeline{node: "testnode", tracker: e.tracker}

	envelope := &rtapi.Envelope{Message: &rtapi.Envelope_LobbyEntrantConnected{
		LobbyEntrantConnected: &rtapi.LobbyEntrantsConnectedMessage{
			LobbySessionId: lobbyID.String(),
			EntrantIds:     []string{entrantID.String()},
		},
	}}
	logger, logs := newPingLogObserver()
	require.NoError(t, e.ep.lobbyEntrantConnected(logger, gs, envelope))

	got := logs.FilterMessage("Entrant ack accepted").All()
	require.Len(t, got, 1, "one ack line for the one accepted entrant")
	assert.Equal(t, zapcore.InfoLevel, got[0].Level)
	fields := got[0].ContextMap()
	assert.Equal(t, entrant.id.String(), fields["sid"])
	assert.Equal(t, entrant.userID.String(), fields["uid"])
	assert.Equal(t, entrantID.String(), fields["entrant_id"])
	assert.Equal(t, lobbyID.String(), fields["mid"])
	assert.Equal(t, gs.id.String(), fields["gs_sid"])
	assert.Equal(t, int64(2500), fields["join_attempt_ms"])
	assert.Equal(t, true, fields["matchmaking_untracked"])
}

// stubLoopCore lets MatchHandler's loop run without a real match.
type stubLoopCore struct{ RuntimeMatchCore }

func (stubLoopCore) MatchLoop(int64, interface{}, <-chan *MatchDataMessage) (interface{}, error) {
	return struct{}{}, nil
}

// A player whose join marker expired without an ack is removed with an Info
// line that names the player, the marker's age and deadline, and the reason
// code handed to the leave.
func TestMatchHandlerLoop_LogsExpiredJoinMarkerSweep(t *testing.T) {
	logger, logs := newPingLogObserver()
	mh := &MatchHandler{
		logger: logger,
		Core:   stubLoopCore{},
		IDStr:  "00000000-0000-0000-0000-000000000001.testnode",
		Rate:   10,
		JoinMarkerList: &MatchJoinMarkerList{
			expiryDelayMs: 15000,
			tickRate:      10,
			joinMarkers:   map[uuid.UUID]*MatchJoinMarker{},
		},
		PresenceList: NewMatchPresenceList(),
		callCh:       make(chan func(*MatchHandler), 4),
		stopCh:       make(chan struct{}),
		stopped:      atomic.NewBool(false),
		state:        struct{}{},
	}
	presence := &MatchPresence{
		Node:      "testnode",
		UserID:    uuid.Must(uuid.NewV4()),
		SessionID: uuid.Must(uuid.NewV4()),
		Username:  "u",
		Reason:    runtime.PresenceReasonUnknown,
	}
	entrantID := uuid.Must(uuid.NewV4()).String()
	mh.JoinMarkerList.Add(presence, 0, entrantID)
	mh.JoinMarkerList.joinMarkers[presence.SessionID].startedAt = time.Now().Add(-16 * time.Second)

	// The sweep runs on ticks that are a multiple of Rate*30; tick 0 is one,
	// and 150 ticks (15s at 10/s) is past the marker's expiry.
	mh.tick = 300
	loop(mh)

	got := logs.FilterMessage("Join marker expired; removing player").All()
	require.Len(t, got, 1, "one sweep line for the one expired marker")
	assert.Equal(t, zapcore.InfoLevel, got[0].Level)
	fields := got[0].ContextMap()
	assert.Equal(t, mh.IDStr, fields["match_id"])
	assert.Equal(t, presence.UserID.String(), fields["user_id"])
	assert.Equal(t, presence.SessionID.String(), fields["session_id"])
	assert.Equal(t, entrantID, fields["entrant_id"])
	assert.Equal(t, int64(15000), fields["deadline_ms"])
	assert.Equal(t, int32(0), fields["reason"])
	age, ok := fields["marker_age_ms"].(int64)
	require.True(t, ok, "marker_age_ms is an int64 field")
	assert.GreaterOrEqual(t, age, int64(16000))
	assert.Len(t, mh.callCh, 1, "the leave is still queued")
}
