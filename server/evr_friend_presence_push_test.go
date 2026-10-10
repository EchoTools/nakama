package server

import (
	"context"
	"sync"
	"testing"
	"time"

	rtapi "buf.build/gen/go/echotools/nevr-api/protocolbuffers/go/gameservice/v1"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
)

func TestFriendPresenceLimiterCoalescesABurst(t *testing.T) {
	clock := time.Unix(1000, 0)
	l := &friendPresenceLimiter{interval: 2 * time.Second, now: func() time.Time { return clock }}
	a, b := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())

	now, _ := l.admit(a)
	require.True(t, now, "the first change goes at once")

	clock = clock.Add(500 * time.Millisecond)
	now, delay := l.admit(a)
	require.False(t, now)
	require.Equal(t, 1500*time.Millisecond, delay, "the trailing push is due when the window ends")

	for i := 0; i < 20; i++ {
		clock = clock.Add(10 * time.Millisecond)
		now, delay = l.admit(a)
		require.False(t, now)
		require.Zero(t, delay, "the trailing push is already scheduled; a burst adds no more")
	}

	now, _ = l.admit(b)
	require.True(t, now, "another user is not held by a's window")

	clock = clock.Add(2 * time.Second)
	l.fired(a)
	now, delay = l.admit(a)
	require.False(t, now, "the trailing push counts as a push: the window restarts")
	require.Equal(t, 2*time.Second, delay)
	clock = clock.Add(2 * time.Second)
	l.fired(a)
	clock = clock.Add(3 * time.Second)
	now, _ = l.admit(a)
	require.True(t, now)
}

// pushEnv is the presence fixture with the push's friend list and scheduler replaced, so a test names
// who a user's friends are and runs the delayed push by hand.
type pushEnv struct {
	*presenceEnv
	friends map[uuid.UUID][]uuid.UUID
	clock   time.Time
	mu      sync.Mutex
	delayed []func()
}

func newPushEnv(t *testing.T) *pushEnv {
	e := &pushEnv{presenceEnv: newPresenceEnv(t), friends: map[uuid.UUID][]uuid.UUID{}, clock: time.Unix(5000, 0)}
	e.ep.friendPush.limiter = friendPresenceLimiter{interval: friendPresencePushInterval, now: func() time.Time { return e.clock }}
	e.ep.friendPush.friends = func(_ context.Context, subject uuid.UUID) ([]uuid.UUID, error) {
		return e.friends[subject], nil
	}
	e.ep.friendPush.accountID = func(context.Context, uuid.UUID) (uint64, error) { return 7777, nil }
	e.ep.friendPush.schedule = func(delay time.Duration, f func()) {
		if delay <= 0 {
			f()
			return
		}
		e.mu.Lock()
		e.delayed = append(e.delayed, f)
		e.mu.Unlock()
	}
	return e
}

// An online runtime friend is sent the changer's presence; a stock-client friend (level 0) is sent
// nothing, an offline friend has no session to send to, and the changer is not sent their own.
func TestFriendPresenceChangeIsPushedToRuntimeFriendsOnly(t *testing.T) {
	e := newPushEnv(t)
	subject := e.session("subject", true)
	e.online(subject)
	e.inMatch(subject, evr.ModeArenaPublic)
	runtime := e.session("runtime", true)
	e.online(runtime)
	stock := e.session("stock", false)
	e.online(stock)
	offline := e.session("offline", true)
	e.friends[subject.userID] = []uuid.UUID{runtime.userID, stock.userID, offline.userID}

	e.ep.friendPresenceChanged(context.Background(), loggerForTest(t), subject.userID, "test")

	got := sentPresence(t, runtime)
	require.Len(t, got, 1)
	require.Equal(t, uint64(7777), got[0].FriendID)
	require.Equal(t, uint8(0), got[0].StatusCode)
	require.Equal(t, "Public Arena Match", string(got[0].Text))
	require.Empty(t, drain(stock.outgoingCh), "a stock client does not parse presence")
	require.Empty(t, drain(offline.outgoingCh))
	require.Empty(t, drain(subject.outgoingCh))
}

// Many changes inside the window cost one push now and one trailing push, and the trailing push says
// where the user is at the time it runs.
func TestFriendPresenceBurstIsOnePushPlusOneTrailing(t *testing.T) {
	e := newPushEnv(t)
	subject := e.session("subject", true)
	e.online(subject)
	e.inMatch(subject, evr.ModeSocialPublic)
	friend := e.session("friend", true)
	e.online(friend)
	e.friends[subject.userID] = []uuid.UUID{friend.userID}

	for i := 0; i < 10; i++ {
		e.ep.friendPresenceChanged(context.Background(), loggerForTest(t), subject.userID, "test")
	}
	require.Len(t, sentPresence(t, friend), 1, "the burst's first change is pushed at once, the rest wait")
	require.Len(t, e.delayed, 1, "one trailing push for the whole burst")

	e.inMatch(subject, evr.ModeArenaPrivate)
	e.clock = e.clock.Add(friendPresencePushInterval)
	e.delayed[0]()
	got := sentPresence(t, friend)
	require.Len(t, got, 1)
	require.Equal(t, "Private Arena Match", string(got[0].Text), "the trailing push reads the state when it runs")
}

// Locking the party changes whether a friend may join it: the leader's friend is told at once.
func TestPartyLockAndUnlockPushJoinableToFriends(t *testing.T) {
	e := newPushEnv(t)
	leader := e.session("leader", true)
	e.online(leader)
	e.tabletParty(901, leader)
	friend := e.session("friend", true)
	e.online(friend)
	e.friends[leader.userID] = []uuid.UUID{friend.userID}
	ctx := leader.Context()

	require.NoError(t, e.ep.snsPartyLockRequest(ctx, loggerForTest(t), leader, nil))
	got := sentPresence(t, friend)
	require.Len(t, got, 1)
	require.Zero(t, got[0].Joinable, "locked")
	require.Zero(t, got[0].PartyID)

	e.clock = e.clock.Add(friendPresencePushInterval)
	require.NoError(t, e.ep.snsPartyUnlockRequest(ctx, loggerForTest(t), leader, nil))
	got = sentPresence(t, friend)
	require.Len(t, got, 1)
	require.Equal(t, uint8(1), got[0].Joinable, "unlocked")
	require.Equal(t, uint64(901), got[0].PartyID)
}

// A match admission pushes the entrant's presence beside the party-data relay.
func TestMatchEntryPushesPresence(t *testing.T) {
	e := newPushEnv(t)
	entrant := e.session("entrant", true)
	e.online(entrant)
	e.inMatch(entrant, evr.ModeCombatPublic)
	friend := e.session("friend", true)
	e.online(friend)
	e.friends[entrant.userID] = []uuid.UUID{friend.userID}

	e.ep.friendPresenceMatchEntered(context.Background(), loggerForTest(t), entrant)
	got := sentPresence(t, friend)
	require.Len(t, got, 1)
	require.Equal(t, "Public Combat Match", string(got[0].Text))
}

// A match admission through the real lobbyEntrantConnected handler pushes the entrant's presence: the
// push is not a side effect of a helper but of the game server's "entrant connected" message.
func TestLobbyEntrantConnectedPushesPresenceToTheEntrantsFriends(t *testing.T) {
	e := newPushEnv(t)
	entrant := e.session("entrant", true)
	e.online(entrant)
	friend := e.session("friend", true)
	e.online(friend)
	e.friends[entrant.userID] = []uuid.UUID{friend.userID}

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
	require.NoError(t, e.ep.lobbyEntrantConnected(loggerForTest(t), gs, envelope))

	got := sentPresence(t, friend)
	require.Len(t, got, 1)
	require.Equal(t, uint64(7777), got[0].FriendID)
	require.Equal(t, "Public Arena Match", string(got[0].Text))
}

// A subject who has no live session is not online: a push about them (a trailing one that waited past
// their disconnect, or a party leave on session close) tells their friends nothing.
func TestFriendPresencePushIsSkippedForAnOfflineSubject(t *testing.T) {
	e := newPushEnv(t)
	subject := e.session("subject", true) // no status presence: never went online
	friend := e.session("friend", true)
	e.online(friend)
	e.friends[subject.userID] = []uuid.UUID{friend.userID}

	e.ep.friendPresenceChanged(context.Background(), loggerForTest(t), subject.userID, "test")
	require.Empty(t, drain(friend.outgoingCh))
}

// The limiter's idle windows are dropped on a sweep, once per few intervals, so a server that sees many
// users over time holds a bounded number, and no admit scans the map more than once per period.
func TestFriendPresenceLimiterForgetsIdleUsers(t *testing.T) {
	clock := time.Unix(1000, 0)
	l := &friendPresenceLimiter{interval: time.Second, now: func() time.Time { return clock }}
	for i := 0; i < 10000; i++ {
		clock = clock.Add(10 * time.Millisecond)
		l.admit(uuid.Must(uuid.NewV4()))
	}
	l.mu.Lock()
	held := len(l.state)
	l.mu.Unlock()
	// 10000 users over 100s, swept every 4s: the map holds the last period or two, not all 10000.
	require.Less(t, held, 1000, "idle windows must be swept without waiting for a size threshold")
	require.Greater(t, held, 0)
}
