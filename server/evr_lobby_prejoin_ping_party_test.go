package server

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
	"go.uber.org/zap"
)

// pingTestSession is the part of a Session the pre-join ping code touches:
// its context (for the latency history), its logger, and SendBytes. Every
// SendBytes is counted, so a test can assert that a member was never pinged;
// a responsive session answers the ping the way lobbyPingResponse does.
type pingTestSession struct {
	Session // embedded interface: anything else the code reaches for panics
	ctx     context.Context
	id      uuid.UUID
	history *LatencyHistory
	// respondRTT, when > 0, makes the session answer a ping with that RTT.
	respondRTT int
	extIP      net.IP

	mu    sync.Mutex
	sends int
}

func (s *pingTestSession) Context() context.Context { return s.ctx }
func (s *pingTestSession) Logger() *zap.Logger      { return zap.NewNop() }

func (s *pingTestSession) SendBytes(payload []byte, reliable bool) error {
	s.mu.Lock()
	s.sends++
	s.mu.Unlock()
	if s.respondRTT > 0 {
		s.history.Add(s.extIP, s.respondRTT, 10, time.Now().Add(-time.Minute))
		notifyPreJoinPingWaiter(s.id)
	}
	return nil
}

func (s *pingTestSession) sendCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sends
}

type pingTestWorld struct {
	pipeline *EvrPipeline
	endpoint evr.Endpoint
	label    *MatchLabel
	registry *sessionMapRegistry
	sessions map[uuid.UUID]*pingTestSession
}

func newPingTestWorld(t *testing.T) *pingTestWorld {
	t.Helper()

	required := true
	ServiceSettingsUpdate(&ServiceSettingsData{
		Matchmaking: GlobalMatchmakingSettings{RequirePreMatchPing: &required},
	})
	t.Cleanup(func() { ServiceSettingsUpdate(nil) })

	endpoint := evr.Endpoint{
		InternalIP: net.ParseIP("10.0.0.5"),
		ExternalIP: net.ParseIP("203.0.113.7"),
		Port:       6792,
	}
	groupID := uuid.Must(uuid.NewV4())
	w := &pingTestWorld{
		endpoint: endpoint,
		sessions: make(map[uuid.UUID]*pingTestSession),
		label: &MatchLabel{
			ID:         MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"},
			Mode:       evr.ModeSocialPublic,
			GroupID:    &groupID,
			GameServer: &GameServerPresence{SessionID: uuid.Must(uuid.NewV4()), Endpoint: endpoint},
		},
	}
	registry := &sessionMapRegistry{sessions: map[uuid.UUID]Session{}}
	w.pipeline = &EvrPipeline{
		node:   "testnode",
		appBot: &DiscordAppBot{guildGroupRegistry: seatTestGuildGroupRegistry(nil)},
		nk: &RuntimeGoNakamaModule{
			logger:          zap.NewNop(),
			sessionRegistry: registry,
		},
	}
	w.registry = registry
	return w
}

// addSession registers a live session with an empty latency history. rtt > 0
// makes it answer pings; rtt == 0 makes it silent, like a client that is online
// but not in a match.
func (w *pingTestWorld) addSession(sid uuid.UUID, rtt int) *pingTestSession {
	history := NewLatencyHistory()
	params := &SessionParameters{latencyHistory: atomic.NewPointer(history)}
	s := &pingTestSession{
		ctx:        context.WithValue(context.Background(), ctxSessionParametersKey{}, atomic.NewPointer(params)),
		id:         sid,
		history:    history,
		respondRTT: rtt,
		extIP:      w.endpoint.ExternalIP,
	}
	w.sessions[sid] = s
	w.registry.sessions[sid] = s
	return s
}

// partyOfTwo is the production shape of #714: the follower is the session doing
// the find, the leader is online but not in a match. The entrants are built by
// appendPartyReservationPlaceholders, as lobbyFindOrCreateSocial builds them.
func (w *pingTestWorld) partyOfTwo(t *testing.T, followerRTT, leaderRTT int) (entrants []*EvrMatchPresence, follower, leader *pingTestSession) {
	t.Helper()
	leaderSID, leaderUID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	followerSID, followerUID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())

	follower = w.addSession(followerSID, followerRTT)
	leader = w.addSession(leaderSID, leaderRTT)

	lobbyGroup := makeMatchmakeTestParty(leaderSID, leaderUID, followerSID, followerUID, nil)
	lobbyParams := makeMatchmakeTestLobbyParams(followerUID, *w.label.GroupID, evr.ModeSocialPublic, 2)

	entrants = []*EvrMatchPresence{{
		SessionID: followerSID,
		UserID:    followerUID,
		Username:  "follower",
	}}
	entrants = appendPartyReservationPlaceholders(zap.NewNop(), entrants, lobbyGroup, lobbyParams, "testnode")
	require.Len(t, entrants, 2, "the leader must be added as a reservation placeholder")
	require.Equal(t, leaderSID, entrants[1].SessionID)
	return entrants, follower, leader
}

// #714: a party member who is online but not in a match never answers the ping
// request. The leader is only a reservation placeholder here (they have not
// started their own find), so the check must not ping them and must not fail
// the follower who is actually joining.
func TestValidatePreJoinPing_AbsentLeaderPlaceholderDoesNotFailFollower(t *testing.T) {
	w := newPingTestWorld(t)
	entrants, follower, leader := w.partyOfTwo(t, 79, 0)

	start := time.Now()
	err := w.pipeline.validatePreJoinPing(context.Background(), zap.NewNop(), w.label, entrants)

	require.NoError(t, err, "a silent reservation placeholder must not fail the joining member's check")
	assert.Equal(t, 0, leader.sendCount(), "a placeholder is not joining and must not be pinged")
	assert.Greater(t, follower.sendCount(), 0, "the joining member is pinged")
	assert.Less(t, time.Since(start), preJoinPingTimeout/2, "must not wait out the ping timeout for a placeholder")
}

// The prewarm pass runs before validation and waits on every entrant it pings.
// It must leave placeholders alone for the same reason.
func TestPrewarmEntrantPings_SkipsReservationPlaceholders(t *testing.T) {
	w := newPingTestWorld(t)
	entrants, follower, leader := w.partyOfTwo(t, 79, 0)

	start := time.Now()
	w.pipeline.prewarmEntrantPings(context.Background(), zap.NewNop(), entrants, []evr.Endpoint{w.endpoint})

	assert.Equal(t, 0, leader.sendCount(), "a placeholder is not joining and must not be pinged")
	assert.Greater(t, follower.sendCount(), 0, "the joining member is pinged")
	assert.Less(t, time.Since(start), preJoinPingTimeout/2, "must not wait out the ping timeout for a placeholder")
}

// The check still has teeth: a member who IS joining and does not answer fails
// it. When that member is not the one running the find, the player is told it
// is their party, not "internal error"; when it is the player themself, the
// code is unchanged.
func TestValidatePreJoinPing_FailureAttribution(t *testing.T) {
	// Each subtest builds its own world and runs serially: the world writes to
	// shared maps and newPingTestWorld sets global service settings.
	t.Run("joining party member silent: tells the player it is the party", func(t *testing.T) {
		w := newPingTestWorld(t)
		entrants, _, _ := w.partyOfTwo(t, 79, 0)
		// Both members are joining: the leader is a real entrant, not a placeholder.
		entrants[1] = &EvrMatchPresence{SessionID: entrants[1].SessionID, UserID: entrants[1].UserID, Username: "leader"}

		err := w.pipeline.validatePreJoinPing(context.Background(), zap.NewNop(), w.label, entrants)

		require.ErrorIs(t, err, ErrPreJoinPingFailed, "the caller still skips the server on this error")
		assert.NotEqual(t, InternalError, LobbyErrorCode(err), "a party member's failure is not an internal error")
		assert.Contains(t, err.Error(), "party", "the message must say it is the party")

		msg, ok := LobbySessionFailureFromError(evr.ModeSocialPublic, uuid.Nil, err).(*evr.LobbySessionFailurev4)
		require.True(t, ok, "the outgoing message must be a LobbySessionFailure v4")
		assert.NotEqual(t, evr.LobbySessionFailure_InternalError, msg.ErrorCode, "the player must not be told internal error")
		assert.Contains(t, msg.Message, "party", "the outgoing message must say it is the party")
	})

	t.Run("the player themself silent: coded, plain message, never internal error", func(t *testing.T) {
		w := newPingTestWorld(t)
		entrants, _, _ := w.partyOfTwo(t, 0, 79)
		entrants[1] = &EvrMatchPresence{SessionID: entrants[1].SessionID, UserID: entrants[1].UserID, Username: "leader"}

		err := w.pipeline.validatePreJoinPing(context.Background(), zap.NewNop(), w.label, entrants)

		require.ErrorIs(t, err, ErrPreJoinPingFailed)
		assert.Equal(t, ServerFindFailed, LobbyErrorCode(err), "the initiator's own failure is a coded server-find failure")
		assert.NotEqual(t, InternalError, LobbyErrorCode(err), "the initiator's own failure is never an internal error")

		msg, ok := LobbySessionFailureFromError(evr.ModeSocialPublic, uuid.Nil, err).(*evr.LobbySessionFailurev4)
		require.True(t, ok, "the outgoing message must be a LobbySessionFailure v4")
		assert.NotEqual(t, evr.LobbySessionFailure_InternalError, msg.ErrorCode, "the player must not be told internal error")
		assert.Equal(t, evr.LobbySessionFailure_ServerFindFailed, msg.ErrorCode)
		assert.Equal(t, "your connection to that server is too slow or unreachable", msg.Message, "the player gets the plain message only")
	})
}
