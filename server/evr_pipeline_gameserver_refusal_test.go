package server

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/gorilla/websocket"
	"go.uber.org/atomic"
	"go.uber.org/zap"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

// refusalTestSession returns a sessionWS over a real websocket whose outgoing
// loop is running, and the game server's (client) end of that socket.
func refusalTestSession(t *testing.T, writeWait time.Duration) (*sessionWS, *websocket.Conn) {
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
	var serverConn *websocket.Conn
	select {
	case serverConn = <-connCh:
	case <-time.After(5 * time.Second):
		t.Fatal("server side of the websocket never arrived")
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var endReason string
	s := &sessionWS{
		logger: zap.NewNop(), id: uuid.Must(uuid.NewV4()), userID: uuid.Must(uuid.NewV4()),
		username: atomic.NewString("u"), ctx: ctx, ctxCancelFn: cancel, conn: serverConn,
		format: SessionFormatEVR, wsMessageType: websocket.BinaryMessage,
		writeWaitDuration: writeWait, pingTimer: time.NewTimer(time.Hour),
		outgoingCh: make(chan []byte, 16), metrics: &testMetrics{},
		matchmaker: closeTestMatchmaker{}, tracker: &testTracker{}, statusRegistry: closeTestStatusRegistry{},
		sessionRegistry: NewLocalSessionRegistry(&testMetrics{}), runtime: closeTestRuntime(&endReason),
	}
	go s.processOutgoing()
	return s, client
}

// readFrames reads from the game server's end until the socket errors, and
// returns every frame it received and how the read ended.
func readFrames(client *websocket.Conn) (frames [][]byte, endErr error) {
	for {
		_ = client.SetReadDeadline(time.Now().Add(10 * time.Second))
		_, data, err := client.ReadMessage()
		if err != nil {
			return frames, err
		}
		frames = append(frames, data)
	}
}

// A refused game server must receive the BroadcasterRegistrationFailure frame,
// with its result code, before the socket closes. Closing the session at once
// cancels the outgoing loop and drops the queued frame, so the server saw only
// close 1005 and never learned why it was refused.
func TestErrFailedRegistration_DeliversFailureFrameBeforeClose(t *testing.T) {
	// Several rounds: on the old behaviour the drop is a race the close usually wins.
	for round := range 5 {
		session, client := refusalTestSession(t, 200*time.Millisecond)

		// The wire form of the failure with its result code. (Decoding it back with
		// evr.ParsePacket loses the code, so the frame is compared as bytes.)
		want, err := evr.Marshal(evr.NewBroadcasterRegistrationFailure(evr.BroadcasterRegistration_Failure))
		if err != nil {
			t.Fatal(err)
		}
		type result struct {
			frames [][]byte
			err    error
		}
		done := make(chan result, 1)
		go func() {
			frames, err := readFrames(client)
			done <- result{frames, err}
		}()

		refusal := errors.New("game server version is below minimum required version")
		err = errFailedRegistration(session, zap.NewNop(), refusal, evr.BroadcasterRegistration_Failure)
		if !errors.Is(err, refusal) {
			t.Fatalf("round %d: errFailedRegistration did not wrap the refusal: %v", round, err)
		}

		select {
		case r := <-done:
			if !bytes.Contains(bytes.Join(r.frames, nil), want) {
				t.Fatalf("round %d: game server got %d frames before the close, none with the failure code (read ended: %v)",
					round, len(r.frames), r.err)
			}
			if !websocket.IsCloseError(r.err, websocket.CloseNoStatusReceived, websocket.CloseNormalClosure) {
				t.Fatalf("round %d: socket did not end in a close: %v", round, r.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("round %d: socket never closed after the refusal", round)
		}
		select {
		case <-session.Context().Done():
		default:
			t.Fatalf("round %d: session context not cancelled after the refusal", round)
		}
	}
}

// A session that closes on its own while the deferred close is waiting is
// closed once, and the refusal does not wait out the delay.
func TestErrFailedRegistration_SessionClosesFirst(t *testing.T) {
	session, client := refusalTestSession(t, 30*time.Second)
	go func() { _, _ = readFrames(client) }()

	returned := make(chan struct{})
	go func() {
		_ = errFailedRegistration(session, zap.NewNop(), errors.New("refused"), evr.BroadcasterRegistration_Unknown)
		close(returned)
	}()

	time.Sleep(100 * time.Millisecond)
	session.Close("closed on its own", 0)

	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("errFailedRegistration still waiting after the session closed on its own")
	}
}
