package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// failureSendEnv is a pipeline and a client session whose outgoing queue the test reads: every
// message the session is sent, by SendEvr or by SendEVRMessages, lands there.
type failureSendEnv struct {
	p       *EvrPipeline
	session *sessionWS
}

func newFailureSendEnv(t *testing.T) *failureSendEnv {
	t.Helper()
	sessions := &sessionMapRegistry{sessions: map[uuid.UUID]Session{}}
	p := &EvrPipeline{
		node: "testnode",
		nk:   &RuntimeGoNakamaModule{sessionRegistry: sessions, node: "testnode"},
	}
	session := newPartyMemberSession(t, "player", nil, nil, p)
	sessions.sessions[session.id] = session
	return &failureSendEnv{p: p, session: session}
}

// failures is the LobbySessionFailures the session was sent within window.
func (e *failureSendEnv) failures(t *testing.T, window time.Duration) []*evr.LobbySessionFailurev4 {
	t.Helper()
	var got []*evr.LobbySessionFailurev4
	deadline := time.After(window)
	for {
		select {
		case payload := <-e.session.outgoingCh:
			messages, err := evr.ParsePacket(payload)
			require.NoError(t, err)
			for _, m := range messages {
				if f, ok := m.(*evr.LobbySessionFailurev4); ok {
					got = append(got, f)
				}
			}
		case <-deadline:
			return got
		}
	}
}

// A failed join is answered once. lobbyJoin used to answer it itself, 3 s later, and then its caller
// answered it again, so a client looping on a join it cannot get was sent two failures per attempt.
// The window covers that 3 s delay with room to spare.
func TestFailedLobbyJoinIsAnsweredOnce(t *testing.T) {
	e := newFailureSendEnv(t)
	e.p.lobbyErrorAudit = func(context.Context, string, string) error { return nil }

	label := &MatchLabel{ID: MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}, Mode: evr.ModeArenaPublic}
	presence := &EvrMatchPresence{SessionID: e.session.id, UserID: e.session.userID}

	// The match has no game server: LobbyJoinEntrants refuses before any registry or tracker work.
	joinErr := e.p.lobbyJoinEntrant(loggerForTest(t), e.session, label, presence)
	require.Error(t, joinErr)

	request := &evr.LobbyJoinSessionRequest{}
	lobbyParams := &LobbySessionParameters{GroupID: uuid.Must(uuid.NewV4())}
	e.p.answerLobbySessionRequestError(context.Background(), loggerForTest(t), e.session, request, request, lobbyParams, joinErr)

	got := e.failures(t, 4*time.Second)
	require.Len(t, got, 1, "a failed join must be answered exactly once")
	// The failure code is the one LobbySessionFailureFromError has always given this error.
	want := LobbySessionFailureFromError(request.GetMode(), request.GetGroupID(), joinErr).(*evr.LobbySessionFailurev4)
	require.Equal(t, want.ErrorCode, got[0].ErrorCode)
}

// The client's failure is not held behind the error channel post. The post goes to Discord, which
// rate-limits: in production a member's failures kept arriving for about 13 minutes after its client
// stopped asking. Here the post blocks until the test ends; the failure must arrive anyway, and the
// post must still be made.
func TestLobbyFailureIsNotHeldBehindTheAuditPost(t *testing.T) {
	e := newFailureSendEnv(t)
	auditCalled := make(chan string, 1)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	e.p.lobbyErrorAudit = func(_ context.Context, _ string, message string) error {
		auditCalled <- message
		<-release
		return nil
	}

	request := &evr.LobbyJoinSessionRequest{}
	lobbyParams := &LobbySessionParameters{GroupID: uuid.Must(uuid.NewV4())}
	joinErr := NewLobbyError(ServerIsFull, "join not allowed")

	answered := make(chan struct{})
	go func() {
		defer close(answered)
		e.p.answerLobbySessionRequestError(context.Background(), loggerForTest(t), e.session, request, request, lobbyParams, joinErr)
	}()

	select {
	case payload := <-e.session.outgoingCh:
		messages, err := evr.ParsePacket(payload)
		require.NoError(t, err)
		require.Len(t, messages, 1)
		_, ok := messages[0].(*evr.LobbySessionFailurev4)
		require.True(t, ok, "expected a LobbySessionFailurev4, got %T", messages[0])
	case <-time.After(2 * time.Second):
		t.Fatal("the client's failure was held behind the blocked error channel post")
	}

	select {
	case <-auditCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("the failure was never posted to the error channel")
	}
}

// When the error channel post fails, the failure still reaches the client and the post's error is
// logged at warn.
func TestLobbyFailureAuditErrorIsLogged(t *testing.T) {
	e := newFailureSendEnv(t)
	posted := make(chan struct{})
	e.p.lobbyErrorAudit = func(context.Context, string, string) error {
		defer close(posted)
		return errors.New("discord unavailable")
	}
	core, logs := observer.New(zapcore.DebugLevel)

	request := &evr.LobbyJoinSessionRequest{}
	lobbyParams := &LobbySessionParameters{GroupID: uuid.Must(uuid.NewV4())}
	e.p.answerLobbySessionRequestError(context.Background(), zap.New(core), e.session, request, request, lobbyParams, NewLobbyError(ServerIsFull, "join not allowed"))

	require.Len(t, e.failures(t, 500*time.Millisecond), 1)
	<-posted
	require.Eventually(t, func() bool {
		entries := logs.FilterMessage("Failed to log audit message").All()
		return len(entries) == 1 && entries[0].Level == zapcore.WarnLevel && entries[0].ContextMap()["error"] == "discord unavailable"
	}, 2*time.Second, 10*time.Millisecond)
}

// Past lobbyErrorAuditMaxInFlight posts in flight, a post is skipped with a warn carrying the failure,
// and the client's failure is still sent: the bound caps the goroutines a join loop can start.
func TestLobbyFailureAuditSlotsAreScopedToSession(t *testing.T) {
	e := newFailureSendEnv(t)
	audited := make(chan string, 2)
	e.p.lobbyErrorAudit = func(_ context.Context, groupID, _ string) error {
		audited <- groupID
		return nil
	}
	blockedSession := uuid.Must(uuid.NewV4()).String()
	otherSession := uuid.Must(uuid.NewV4()).String()
	lobbyErrorAuditSessions.Lock()
	slots := make(chan struct{}, lobbyErrorAuditMaxInFlight)
	lobbyErrorAuditSessions.slots[blockedSession] = slots
	for range lobbyErrorAuditMaxInFlight {
		slots <- struct{}{}
	}
	lobbyErrorAuditSessions.Unlock()
	t.Cleanup(func() {
		lobbyErrorAuditSessions.Lock()
		for range lobbyErrorAuditMaxInFlight {
			<-slots
		}
		delete(lobbyErrorAuditSessions.slots, blockedSession)
		lobbyErrorAuditSessions.Unlock()
	})
	core, logs := observer.New(zapcore.DebugLevel)

	lobbyParams := &LobbySessionParameters{GroupID: uuid.Must(uuid.NewV4())}
	e.p.postLobbyErrorAuditAsync(context.Background(), zap.New(core), blockedSession, lobbyParams.GroupID.String(), "blocked", NewLobbyError(ServerIsFull, "join not allowed"))
	e.p.postLobbyErrorAuditAsync(context.Background(), zap.New(core), otherSession, lobbyParams.GroupID.String(), "other", NewLobbyError(ServerIsFull, "join not allowed"))

	select {
	case got := <-audited:
		require.Equal(t, lobbyParams.GroupID.String(), got)
	case <-time.After(time.Second):
		t.Fatal("other session's audit post was blocked by a saturated session")
	}
	entries := logs.FilterMessage("Lobby error audit post skipped: too many posts in flight").All()
	require.Len(t, entries, 1)
	require.Equal(t, zapcore.WarnLevel, entries[0].Level)
}
