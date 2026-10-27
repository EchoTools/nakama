package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/gorilla/websocket"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

type closeTestMatchmaker struct{ Matchmaker }

func (closeTestMatchmaker) RemoveSessionAll(string) error { return nil }

type closeTestStatusRegistry struct{ StatusRegistry }

func (closeTestStatusRegistry) UnfollowAll(uuid.UUID) {}

// serverSideConn returns the server end of a real websocket connection.
func serverSideConn(t *testing.T) *websocket.Conn {
	t.Helper()
	upgrader := websocket.Upgrader{}
	connCh := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		connCh <- c
	}))
	t.Cleanup(srv.Close)
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	select {
	case c := <-connCh:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("server side of the websocket never arrived")
		return nil
	}
}

func closeTestRuntime(endReason *string) *Runtime {
	return &Runtime{eventFunctions: &RuntimeEventFunctions{
		sessionEndFunction: func(_, _ string, _ map[string]string, _ int64, _, _, _, _ string, _ int64, reason string) {
			*endReason = reason
		},
	}}
}

func TestSessionClose_LogsReason(t *testing.T) {
	cases := []struct {
		name       string
		msg        string
		reason     runtime.PresenceReason
		wantReason string
		wantPres   string
	}{
		{"client closed", "", runtime.PresenceReasonDisconnect, "client closed", "disconnect"},
		{"server side error", "error processing message", runtime.PresenceReasonDisconnect, "error processing message", "disconnect"},
		{"banned", closeReasonBanned, runtime.PresenceReasonDisconnect, "banned", "disconnect"},
		{"duplicate", closeReasonDuplicateSession, runtime.PresenceReasonDisconnect, "duplicate session", "disconnect"},
		{"leave presence", closeReasonServerDisconnect, runtime.PresenceReasonLeave, "disconnected by server", "leave"},
	}

	for _, kind := range []string{"sessionWS", "sessionNEVR"} {
		for _, tc := range cases {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				core, logs := observer.New(zapcore.InfoLevel)
				logger := zap.New(core)
				ctx, cancel := context.WithCancel(context.Background())
				t.Cleanup(cancel)
				conn := serverSideConn(t)
				var endReason string
				var s Session
				switch kind {
				case "sessionWS":
					s = &sessionWS{
						logger: logger, id: uuid.Must(uuid.NewV4()), userID: uuid.Must(uuid.NewV4()),
						username: atomic.NewString("u"), ctx: ctx, ctxCancelFn: cancel, conn: conn,
						writeWaitDuration: time.Second, pingTimer: time.NewTimer(time.Hour),
						matchmaker: closeTestMatchmaker{}, tracker: &testTracker{}, statusRegistry: closeTestStatusRegistry{},
						sessionRegistry: NewLocalSessionRegistry(&testMetrics{}), runtime: closeTestRuntime(&endReason),
					}
				case "sessionNEVR":
					s = &sessionNEVR{
						logger: logger, id: uuid.Must(uuid.NewV4()), userID: uuid.Must(uuid.NewV4()),
						username: atomic.NewString("u"), ctx: ctx, ctxCancelFn: cancel, conn: conn,
						writeWaitDuration: time.Second, pingTimer: time.NewTimer(time.Hour),
						matchmaker: closeTestMatchmaker{}, tracker: &testTracker{}, statusRegistry: closeTestStatusRegistry{},
						sessionRegistry: NewLocalSessionRegistry(&testMetrics{}), runtime: closeTestRuntime(&endReason),
					}
				}

				s.Close(tc.msg, tc.reason)
				// The first Close wins: a later caller must not rewrite the record.
				s.Close("late caller", runtime.PresenceReasonLeave)

				recs := logs.FilterMessage("Closed client connection").All()
				if len(recs) != 1 {
					t.Fatalf("want exactly one close record, got %d: %v", len(recs), logs.All())
				}
				fields := recs[0].ContextMap()
				if got := fields["close_reason"]; got != tc.wantReason {
					t.Errorf("close_reason = %v, want %q", got, tc.wantReason)
				}
				if got := fields["presence_reason"]; got != tc.wantPres {
					t.Errorf("presence_reason = %v, want %q", got, tc.wantPres)
				}
				if endReason != tc.msg {
					t.Errorf("EventSessionEnd reason = %q, want the caller's msg %q", endReason, tc.msg)
				}
			})
		}
	}
}

type closeRecordingSession struct {
	Session
	mu     sync.Mutex
	closed []string
}

func (c *closeRecordingSession) Close(msg string, _ runtime.PresenceReason, _ ...*rtapi.Envelope) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = append(c.closed, msg)
}

type closeTestTracker struct {
	testTracker
	ids []uuid.UUID
}

func (c *closeTestTracker) ListLocalSessionIDByStream(PresenceStream) []uuid.UUID { return c.ids }

func TestSessionRegistry_ClosesCarryDistinctReasons(t *testing.T) {
	newReg := func() (SessionRegistry, *closeRecordingSession, uuid.UUID) {
		reg := NewLocalSessionRegistry(&testMetrics{})
		rec := &closeRecordingSession{}
		id := uuid.Must(uuid.NewV4())
		reg.Add(closeRegistered{rec, id})
		return reg, rec, id
	}

	t.Run("disconnect", func(t *testing.T) {
		reg, rec, id := newReg()
		_ = reg.Disconnect(context.Background(), id, false)
		if len(rec.closed) != 1 || rec.closed[0] != closeReasonServerDisconnect {
			t.Fatalf("closed = %v, want [%q]", rec.closed, closeReasonServerDisconnect)
		}
	})
	t.Run("ban", func(t *testing.T) {
		reg, rec, id := newReg()
		_ = reg.Disconnect(context.Background(), id, true)
		if len(rec.closed) != 1 || rec.closed[0] != closeReasonBanned {
			t.Fatalf("closed = %v, want [%q]", rec.closed, closeReasonBanned)
		}
	})
	t.Run("duplicate", func(t *testing.T) {
		reg, rec, id := newReg()
		tr := &closeTestTracker{ids: []uuid.UUID{id}}
		reg.SingleSession(context.Background(), tr, uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()))
		if len(rec.closed) != 1 || rec.closed[0] != closeReasonDuplicateSession {
			t.Fatalf("closed = %v, want [%q]", rec.closed, closeReasonDuplicateSession)
		}
	})
}

// closeRegistered gives the recording session an ID so the registry can key it.
type closeRegistered struct {
	*closeRecordingSession
	id uuid.UUID
}

func (c closeRegistered) ID() uuid.UUID { return c.id }
