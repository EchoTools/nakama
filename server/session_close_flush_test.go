package server

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/gorilla/websocket"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/atomic"
	"go.uber.org/zap"
)

// consumeTestSession is a real sessionWS over a real websocket, running Consume as
// the socket acceptor does, so its processOutgoing routine is started by Consume
// itself. It returns once processOutgoing has written a first frame, which the
// client has read. ended receives the reason Close passed to the session-end event.
func consumeTestSession(t *testing.T, format SessionFormat, writeWait time.Duration) (s *sessionWS, client *websocket.Conn, ended <-chan string) {
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
	var conn *websocket.Conn
	select {
	case conn = <-connCh:
	case <-time.After(5 * time.Second):
		t.Fatal("server side of the websocket never arrived")
	}

	config := NewConfig(zap.NewNop())
	config.GetSocket().WriteWaitMs = int(writeWait / time.Millisecond)
	config.GetSocket().OutgoingQueueSize = 64

	endCh := make(chan string, 1)
	rt := &Runtime{eventFunctions: &RuntimeEventFunctions{
		sessionEndFunction: func(_, _ string, _ map[string]string, _ int64, _, _, _, _ string, _ int64, reason string) {
			endCh <- reason
		},
	}}

	ctx, cancel := context.WithCancel(context.Background())
	s = &sessionWS{
		logger: zap.NewNop(), config: config, id: uuid.Must(uuid.NewV4()), userID: uuid.Must(uuid.NewV4()),
		username: atomic.NewString("u"), ctx: ctx, ctxCancelFn: cancel, conn: conn,
		format: format, wsMessageType: websocket.BinaryMessage,
		pingPeriodDuration: time.Hour, pongWaitDuration: time.Hour, writeWaitDuration: writeWait,
		pingTimer: time.NewTimer(time.Hour), pingTimerCAS: atomic.NewUint32(1),
		receivedMessageCounter: config.GetSocket().PingBackoffThreshold,
		outgoingCh:             make(chan []byte, config.GetSocket().OutgoingQueueSize), metrics: &testMetrics{},
		matchmaker: closeTestMatchmaker{}, tracker: &testTracker{}, statusRegistry: closeTestStatusRegistry{},
		sessionRegistry: NewLocalSessionRegistry(&testMetrics{}), runtime: rt,
	}

	consumed := make(chan struct{})
	go func() {
		s.Consume()
		close(consumed)
	}()
	t.Cleanup(func() {
		s.Close("test cleanup", runtime.PresenceReasonDisconnect)
		select {
		case <-consumed:
		case <-time.After(10 * time.Second):
			t.Error("Consume did not return after Close")
		}
	})

	// Wait for processOutgoing: a frame only reaches the client once it runs.
	if err := s.SendBytes(unrequireBytes, true); err != nil {
		t.Fatalf("SendBytes: %v", err)
	}
	if err := client.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	if _, _, err := client.ReadMessage(); err != nil {
		t.Fatalf("processOutgoing never wrote the first frame: %v", err)
	}
	return s, client, endCh
}

// readFailureCodesUntilClose returns the result code of every
// BroadcasterRegistrationFailure frame the client receives before the socket
// ends. The code is read off the wire: the message type only encodes, so
// decoding a frame back into it leaves the code zero.
func readFailureCodesUntilClose(t *testing.T, client *websocket.Conn) []evr.BroadcasterRegistrationFailureCode {
	t.Helper()
	failureSym := uint64(evr.SymbolOf(&evr.BroadcasterRegistrationFailure{}))
	var codes []evr.BroadcasterRegistrationFailureCode
	for {
		if err := client.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatalf("set read deadline: %v", err)
		}
		_, data, err := client.ReadMessage()
		if err != nil {
			return codes
		}
		for _, chunk := range evr.SplitPacket(data) {
			// symbol (8) + length (8) + payload; the failure payload is the one code byte.
			if len(chunk) >= 17 && binary.LittleEndian.Uint64(chunk[:8]) == failureSym {
				codes = append(codes, evr.BroadcasterRegistrationFailureCode(chunk[16]))
			}
		}
	}
}

// A refused game server must be told why. errFailedRegistration queues the
// BroadcasterRegistrationFailure frame and closes the session at once; the frame
// has to reach the server before the socket closes, or the operator sees a bare
// close 1005 and no result code.
func TestErrFailedRegistration_FrameReachesClientBeforeClose(t *testing.T) {
	for _, code := range []evr.BroadcasterRegistrationFailureCode{
		evr.BroadcasterRegistration_Failure,
		evr.BroadcasterRegistration_Unknown,
	} {
		for i := range 25 {
			t.Run(fmt.Sprintf("%v/%d", code, i), func(t *testing.T) {
				s, client, _ := consumeTestSession(t, SessionFormatEVR, 5*time.Second)

				err := errFailedRegistration(s, zap.NewNop(), errors.New("Broadcaster could not be reached"), code)
				if err == nil {
					t.Fatal("errFailedRegistration returned nil")
				}

				got := readFailureCodesUntilClose(t, client)
				if len(got) == 0 {
					t.Fatalf("client got the close but never the BroadcasterRegistrationFailure frame")
				}
				if len(got) != 1 || got[0] != code {
					t.Fatalf("result codes received = %v, want [%v]", got, code)
				}
			})
		}
	}
}

// Frames queued before a Close arrive in the order they were queued, whatever
// else the session is doing when the Close lands.
func TestSessionClose_FlushesQueuedFramesInOrder(t *testing.T) {
	for i := range 25 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s, client, _ := consumeTestSession(t, SessionFormatEVR, 5*time.Second)
			want := []evr.BroadcasterRegistrationFailureCode{
				evr.BroadcasterRegistration_InvalidRequest,
				evr.BroadcasterRegistration_Timeout,
				evr.BroadcasterRegistration_Restricted,
			}
			for _, c := range want {
				if err := s.SendEvr(evr.NewBroadcasterRegistrationFailure(c)); err != nil {
					t.Fatalf("SendEvr: %v", err)
				}
			}
			s.Close("test", runtime.PresenceReasonDisconnect)

			got := readFailureCodesUntilClose(t, client)
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("client received %v, want %v", got, want)
			}
		})
	}
}

// A Close stops the session at once even when its client has stopped reading: a
// kicked, banned or duplicate session must not keep working while its last frames
// wait on a slow socket. The frames queued before the Close still reach the client
// ahead of the close frame once it reads again.
//
// Protobuf sessions write one frame per lock hold, so the Close takes the lock
// between frames with most of the queue still unwritten; EVR sessions batch the
// queue into one write, so there it is the context that the test holds to account.
func TestSessionClose_StalledClient_StopsAtOnceAndStillDelivers(t *testing.T) {
	for _, format := range []SessionFormat{SessionFormatEVR, SessionFormatProtobuf} {
		t.Run(fmt.Sprint(format), func(t *testing.T) {
			s, client, ended := consumeTestSession(t, format, 10*time.Second)

			// Queue far more than the loopback socket buffers hold; the client reads nothing.
			big := append(append([]byte{}, evr.MessageMarker...), make([]byte, 1<<20)...)
			for range 32 {
				if err := s.SendBytes(big, true); err != nil {
					t.Fatalf("SendBytes: %v", err)
				}
			}
			refusal, err := evr.Marshal(evr.NewBroadcasterRegistrationFailure(evr.BroadcasterRegistration_Failure))
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if err := s.SendBytes(refusal, true); err != nil {
				t.Fatalf("SendBytes: %v", err)
			}

			closed := make(chan struct{})
			go func() {
				s.Close("banned", runtime.PresenceReasonDisconnect)
				close(closed)
			}()

			deadline := time.Now().Add(500 * time.Millisecond)
			for s.Context().Err() == nil && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if s.Context().Err() == nil {
				t.Fatal("the session context is still live 500ms after Close: the session keeps working while its queue drains")
			}
			select {
			case <-closed:
				t.Fatal("Close returned while the client was reading nothing: the client never stalled, so this test proves nothing")
			default:
			}

			got := readFailureCodesUntilClose(t, client)
			if len(got) != 1 || got[0] != evr.BroadcasterRegistration_Failure {
				t.Fatalf("result codes received before the close = %v, want [%v]", got, evr.BroadcasterRegistration_Failure)
			}
			select {
			case <-closed:
			case <-time.After(5 * time.Second):
				t.Fatal("Close did not return after the client drained the socket")
			}
			if r := <-ended; r != "banned" {
				t.Fatalf("session end reason = %q, want %q", r, "banned")
			}
		})
	}
}

// When processOutgoing exits because a write failed, the Close it calls must not
// wait for processOutgoing to finish: that is itself, and it would sit out the
// whole write wait on every broken socket.
func TestProcessOutgoing_WriteErrorClosesPromptly(t *testing.T) {
	const writeWait = 5 * time.Second
	s, _, ended := consumeTestSession(t, SessionFormatEVR, writeWait)

	// Shut only the sending half, so the next write fails and the read loop stays up.
	tcp, ok := s.conn.UnderlyingConn().(*net.TCPConn)
	if !ok {
		t.Fatalf("underlying conn is %T, want *net.TCPConn", s.conn.UnderlyingConn())
	}
	if err := tcp.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}

	start := time.Now()
	if err := s.SendBytes(unrequireBytes, true); err != nil {
		t.Fatalf("SendBytes: %v", err)
	}
	select {
	case reason := <-ended:
		if reason == "" || reason == "test cleanup" {
			t.Fatalf("session end reason = %q, want the write error", reason)
		}
		if elapsed := time.Since(start); elapsed > writeWait/5 {
			t.Fatalf("Close took %v after the write error; it waited on its own processOutgoing", elapsed)
		}
	case <-time.After(writeWait / 2):
		t.Fatalf("no Close within %v of the write error; it is waiting on its own processOutgoing", writeWait/2)
	}
}

// Close's wait is bounded by the write wait: if processOutgoing never signals (a
// sender that keeps the queue from emptying, say), Close still goes on to send the
// close frame once the write wait is up.
func TestAwaitOutgoing_BoundedByWriteWait(t *testing.T) {
	const writeWait = 200 * time.Millisecond
	s := &sessionWS{logger: zap.NewNop(), writeWaitDuration: writeWait}

	start := time.Now()
	returned := make(chan struct{})
	go func() {
		s.awaitOutgoing(make(chan struct{})) // never closed
		close(returned)
	}()
	select {
	case <-returned:
		if elapsed := time.Since(start); elapsed < writeWait {
			t.Fatalf("awaitOutgoing returned after %v, before the write wait (%v), with nothing signalled", elapsed, writeWait)
		}
	case <-time.After(10 * writeWait):
		t.Fatalf("awaitOutgoing still waiting after %v; it is not bounded by the write wait (%v)", 10*writeWait, writeWait)
	}
}

// The Close that cancels the session context owns the session-end reason. Once the
// context is cancelled, processOutgoing flushes and exits without a Close of its
// own; one would race the cancelling Close for the session lock and could record
// its empty reason instead (a ban logged as "client closed").
func TestProcessOutgoing_CancelledContextLeavesTheReasonToClose(t *testing.T) {
	s, _, ended := consumeTestSession(t, SessionFormatEVR, 5*time.Second)
	s.Lock()
	done := s.outgoingDone
	s.Unlock()

	// A Close that has cancelled the context and not yet taken the session lock.
	s.ctxCancelFn()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("processOutgoing did not exit after the context was cancelled")
	}
	select {
	case r := <-ended:
		t.Fatalf("processOutgoing closed the session itself, with reason %q", r)
	case <-time.After(200 * time.Millisecond):
	}

	s.Close("banned", runtime.PresenceReasonDisconnect)
	if r := <-ended; r != "banned" {
		t.Fatalf("session end reason = %q, want %q", r, "banned")
	}
}
