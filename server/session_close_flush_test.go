package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/gorilla/websocket"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

// rawTap records every byte the client reads off the network, so a test can check
// the frames the server sent in their wire order, which the client's websocket
// reader hides once it has seen a close frame.
type rawTap struct {
	net.Conn
	mu  sync.Mutex
	buf bytes.Buffer
}

func (r *rawTap) Read(p []byte) (int, error) {
	n, err := r.Conn.Read(p)
	r.mu.Lock()
	r.buf.Write(p[:n])
	r.mu.Unlock()
	return n, err
}

// opcodes returns the opcode of each frame the server sent, in order. Server
// frames are unmasked; a frame cut short at the end still counts.
func (r *rawTap) opcodes() []byte {
	r.mu.Lock()
	b := append([]byte(nil), r.buf.Bytes()...)
	r.mu.Unlock()
	// Frames start after the HTTP upgrade response.
	if i := bytes.Index(b, []byte("\r\n\r\n")); i >= 0 {
		b = b[i+4:]
	}
	var ops []byte
	for len(b) >= 2 {
		op, n, h := b[0]&0x0f, int(b[1]&0x7f), 2
		switch n {
		case 126:
			if len(b) < 4 {
				return append(ops, op)
			}
			n, h = int(binary.BigEndian.Uint16(b[2:4])), 4
		case 127:
			if len(b) < 10 {
				return append(ops, op)
			}
			n, h = int(binary.BigEndian.Uint64(b[2:10])), 10
		}
		ops = append(ops, op)
		if len(b) < h+n {
			return ops
		}
		b = b[h+n:]
	}
	return ops
}

type testSocket struct {
	s        *sessionWS
	client   *websocket.Conn
	raw      *rawTap
	ended    <-chan string // the reason Close passed to the session-end event
	consumed chan struct{} // closed when Consume returns; nil until consume
}

// newTestSession is a real sessionWS over a real websocket, not yet consumed.
func newTestSession(t *testing.T, format SessionFormat, writeWait time.Duration) *testSocket {
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
	raw := &rawTap{}
	dialer := websocket.Dialer{NetDialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		raw.Conn = c
		return raw, err
	}}
	client, _, err := dialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
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
	s := &sessionWS{
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
	return &testSocket{s: s, client: client, raw: raw, ended: endCh}
}

// startConsume runs Consume as the socket acceptor does.
func (ts *testSocket) startConsume(t *testing.T) {
	t.Helper()
	ts.consumed = make(chan struct{})
	go func() {
		ts.s.Consume()
		close(ts.consumed)
	}()
	t.Cleanup(func() {
		ts.s.Close("test cleanup", runtime.PresenceReasonDisconnect)
		ts.client.Close()
		select {
		case <-ts.consumed:
		case <-time.After(10 * time.Second):
			t.Error("Consume did not return after Close")
		}
	})
}

// consumeTestSession is newTestSession running Consume, so its processOutgoing is
// started by Consume itself. It returns once processOutgoing has written a first
// frame, which the client has read.
func consumeTestSession(t *testing.T, format SessionFormat, writeWait time.Duration) *testSocket {
	t.Helper()
	ts := newTestSession(t, format, writeWait)
	ts.startConsume(t)
	if err := ts.s.SendBytes(unrequireBytes, true); err != nil {
		t.Fatalf("SendBytes: %v", err)
	}
	if err := ts.client.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	if _, _, err := ts.client.ReadMessage(); err != nil {
		t.Fatalf("processOutgoing never wrote the first frame: %v", err)
	}
	return ts
}

func (ts *testSocket) outgoingDone() chan struct{} {
	ts.s.Lock()
	defer ts.s.Unlock()
	return ts.s.outgoingDone
}

// readUntilClose returns every message the client receives before the server's
// close frame. It fails the test if the socket is not closed within 5s of the last
// message, or if the server sent any data frame after its close frame (RFC 6455
// §5.5.1: the close frame is the last frame an endpoint sends).
func (ts *testSocket) readUntilClose(t *testing.T) [][]byte {
	t.Helper()
	var msgs [][]byte
	for {
		if err := ts.client.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatalf("set read deadline: %v", err)
		}
		_, data, err := ts.client.ReadMessage()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				t.Fatalf("the server never closed the socket: %v", err)
			}
			break
		}
		msgs = append(msgs, data)
	}
	// Read whatever the server sent after its close frame, up to the socket closing.
	_ = ts.raw.Conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64<<10)
	for {
		if _, err := ts.raw.Read(buf); err != nil {
			break
		}
	}
	ops := ts.raw.opcodes()
	for i, op := range ops {
		if op != websocket.CloseMessage {
			continue
		}
		for _, later := range ops[i+1:] {
			if later == 0 || later == websocket.TextMessage || later == websocket.BinaryMessage {
				t.Fatalf("the server sent a data frame (opcode %d) after its close frame; frames: %v", later, ops)
			}
		}
		break
	}
	return msgs
}

// failureCodes returns the result code of every BroadcasterRegistrationFailure in
// msgs. The code is read off the wire: the message type only encodes, so decoding
// a frame back into it leaves the code zero.
func failureCodes(msgs [][]byte) []evr.BroadcasterRegistrationFailureCode {
	failureSym := uint64(evr.SymbolOf(&evr.BroadcasterRegistrationFailure{}))
	var codes []evr.BroadcasterRegistrationFailureCode
	for _, data := range msgs {
		for _, chunk := range evr.SplitPacket(data) {
			// symbol (8) + length (8) + payload; the failure payload is the one code byte.
			if len(chunk) >= 17 && binary.LittleEndian.Uint64(chunk[:8]) == failureSym {
				codes = append(codes, evr.BroadcasterRegistrationFailureCode(chunk[16]))
			}
		}
	}
	return codes
}

// readSlowly reads one message every interval until the socket ends.
func readSlowly(client *websocket.Conn, interval time.Duration) {
	for {
		_ = client.SetReadDeadline(time.Now().Add(30 * time.Second))
		if _, _, err := client.ReadMessage(); err != nil {
			return
		}
		time.Sleep(interval)
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
				ts := consumeTestSession(t, SessionFormatEVR, 5*time.Second)

				err := errFailedRegistration(ts.s, zap.NewNop(), errors.New("Broadcaster could not be reached"), code)
				if err == nil {
					t.Fatal("errFailedRegistration returned nil")
				}

				got := failureCodes(ts.readUntilClose(t))
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
			ts := consumeTestSession(t, SessionFormatEVR, 5*time.Second)
			want := []evr.BroadcasterRegistrationFailureCode{
				evr.BroadcasterRegistration_InvalidRequest,
				evr.BroadcasterRegistration_Timeout,
				evr.BroadcasterRegistration_Restricted,
			}
			for _, c := range want {
				if err := ts.s.SendEvr(evr.NewBroadcasterRegistrationFailure(c)); err != nil {
					t.Fatalf("SendEvr: %v", err)
				}
			}
			ts.s.Close("test", runtime.PresenceReasonDisconnect)

			got := failureCodes(ts.readUntilClose(t))
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
func TestSessionClose_StalledClient_StopsAtOnceAndStillDelivers(t *testing.T) {
	for _, format := range []SessionFormat{SessionFormatEVR, SessionFormatProtobuf} {
		t.Run(fmt.Sprint(format), func(t *testing.T) {
			ts := consumeTestSession(t, format, 10*time.Second)

			// Queue far more than the loopback socket buffers hold; the client reads nothing.
			big := append(append([]byte{}, evr.MessageMarker...), make([]byte, 1<<20)...)
			for range 32 {
				if err := ts.s.SendBytes(big, true); err != nil {
					t.Fatalf("SendBytes: %v", err)
				}
			}
			refusal, err := evr.Marshal(evr.NewBroadcasterRegistrationFailure(evr.BroadcasterRegistration_Failure))
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if err := ts.s.SendBytes(refusal, true); err != nil {
				t.Fatalf("SendBytes: %v", err)
			}

			go ts.s.Close("banned", runtime.PresenceReasonDisconnect)

			deadline := time.Now().Add(500 * time.Millisecond)
			for ts.s.Context().Err() == nil && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if ts.s.Context().Err() == nil {
				t.Fatal("the session context is still live 500ms after Close: the session keeps working while its queue drains")
			}

			got := failureCodes(ts.readUntilClose(t))
			if len(got) != 1 || got[0] != evr.BroadcasterRegistration_Failure {
				t.Fatalf("result codes received before the close = %v, want [%v]", got, evr.BroadcasterRegistration_Failure)
			}
			if r := <-ts.ended; r != "banned" {
				t.Fatalf("session end reason = %q, want %q", r, "banned")
			}
		})
	}
}

// slowClientWithQueue is a protobuf session (one frame per write) with 60 frames
// of 1 MiB queued, far more than the loopback socket buffers hold, and a client
// that reads one message every 100ms, so writing the whole queue takes seconds.
// Each frame on its own is written well within the write wait.
func slowClientWithQueue(t *testing.T, writeWait time.Duration) *testSocket {
	t.Helper()
	ts := consumeTestSession(t, SessionFormatProtobuf, writeWait)
	frame := make([]byte, 1<<20)
	for range 60 {
		if err := ts.s.SendBytes(frame, true); err != nil {
			t.Fatalf("SendBytes: %v", err)
		}
	}
	go readSlowly(ts.client, 100*time.Millisecond)
	return ts
}

// Close returns to its caller (a match loop, a ban or kick loop, a runtime
// disconnect) without waiting for a slow client to drain its queue. Like main, it
// waits at most for the frame being written when it is called.
func TestSessionClose_SlowClient_ReturnsWithoutWaitingForTheQueue(t *testing.T) {
	ts := slowClientWithQueue(t, 5*time.Second)

	start := time.Now()
	ts.s.Close("kicked", runtime.PresenceReasonDisconnect)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Close blocked its caller for %v while a slow client drained its queue", elapsed)
	}
}

// The frames still queued at a Close share one write wait, so a slow client holds a
// closed session's socket open about that long, not a write wait per frame.
func TestProcessOutgoing_CloseBoundedByOneWriteWait(t *testing.T) {
	const writeWait = 500 * time.Millisecond
	ts := slowClientWithQueue(t, writeWait)
	done := ts.outgoingDone()

	start := time.Now()
	ts.s.Close("kicked", runtime.PresenceReasonDisconnect)
	select {
	case <-done:
	case <-time.After(3 * writeWait):
		t.Fatalf("processOutgoing still writing to a slow client %v after Close; the flush is not bounded by one write wait (%v)", time.Since(start), writeWait)
	}
}

// A sender that keeps queueing after a Close cannot hold the socket open: only the
// frames queued when the session stopped are written.
func TestProcessOutgoing_SteadySenderCannotHoldTheSocketOpen(t *testing.T) {
	const writeWait = 3 * time.Second
	ts := consumeTestSession(t, SessionFormatProtobuf, writeWait)
	done := ts.outgoingDone()
	// A client that reads a frame a millisecond, so each write waits on it and the
	// senders always have time to refill the queue.
	go readSlowly(ts.client, time.Millisecond)

	stopSending := make(chan struct{})
	defer close(stopSending)
	// Keep the queue topped up. Pushing straight onto it skips SendBytes's
	// queue-full Close, which would only add goroutines and slow the senders down.
	frame := make([]byte, 64<<10)
	for range 4 {
		go func() {
			for {
				select {
				case <-stopSending:
					return
				case ts.s.outgoingCh <- frame:
				}
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)

	start := time.Now()
	ts.s.Close("kicked", runtime.PresenceReasonDisconnect)
	select {
	case <-done:
	case <-time.After(writeWait / 2):
		t.Fatalf("processOutgoing still writing %v after Close, behind a sender that keeps queueing", time.Since(start))
	}
}

// The envelopes a Close carries (the banned or single-socket notification) reach
// the client after the queued frames and before the close frame.
func TestSessionClose_EnvelopesReachClientBeforeClose(t *testing.T) {
	ts := consumeTestSession(t, SessionFormatProtobuf, 5*time.Second)
	if err := ts.s.SendBytes([]byte("queued"), true); err != nil {
		t.Fatalf("SendBytes: %v", err)
	}
	ts.s.Close(closeReasonBanned, runtime.PresenceReasonDisconnect, &rtapi.Envelope{Message: &rtapi.Envelope_Notifications{
		Notifications: &rtapi.Notifications{Notifications: []*api.Notification{{Subject: "banned"}}},
	}})

	msgs := ts.readUntilClose(t)
	if len(msgs) != 2 || string(msgs[0]) != "queued" {
		t.Fatalf("client received %q, want the queued frame and then the envelope", msgs)
	}
	var env rtapi.Envelope
	if err := proto.Unmarshal(msgs[1], &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if n := env.GetNotifications().GetNotifications(); len(n) != 1 || n[0].Subject != "banned" {
		t.Fatalf("client received envelope %v, want the banned notification", &env)
	}
}

// Without processOutgoing (Consume never ran), Close writes and closes the socket
// itself.
func TestSessionClose_WithoutConsumeClosesTheSocket(t *testing.T) {
	ts := newTestSession(t, SessionFormatEVR, time.Second)
	ts.s.Close("early", runtime.PresenceReasonDisconnect)
	if msgs := ts.readUntilClose(t); len(msgs) != 0 {
		t.Fatalf("client received %d messages, want none", len(msgs))
	}
}

// A Consume that reaches its writer after a Close has stopped the session (and is
// closing the socket itself) must not start processOutgoing: nothing would stop it.
func TestConsume_StoppedSessionStartsNoWriter(t *testing.T) {
	ts := newTestSession(t, SessionFormatEVR, time.Second)
	// A Close that has stopped the session and not yet closed its socket.
	ts.s.Lock()
	ts.s.stopped = true
	ts.s.Unlock()
	ts.startConsume(t)

	if err := ts.s.SendBytes(unrequireBytes, true); err != nil {
		t.Fatalf("SendBytes: %v", err)
	}
	_ = ts.client.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, _, err := ts.client.ReadMessage(); err == nil {
		t.Fatal("a frame reached the client: Consume started processOutgoing on a stopped session")
	}
	if ts.outgoingDone() != nil {
		t.Fatal("Consume started processOutgoing on a stopped session")
	}
	// The Close finishes.
	ts.s.closeConn(nil)
}

// When processOutgoing exits because a write failed, it closes the session with
// the write error as the reason, and closes the socket, so Consume returns too.
func TestProcessOutgoing_WriteErrorClosesPromptly(t *testing.T) {
	const writeWait = 5 * time.Second
	ts := consumeTestSession(t, SessionFormatEVR, writeWait)

	// Shut only the sending half, so the next write fails and the read loop stays up.
	tcp, ok := ts.s.conn.UnderlyingConn().(*net.TCPConn)
	if !ok {
		t.Fatalf("underlying conn is %T, want *net.TCPConn", ts.s.conn.UnderlyingConn())
	}
	if err := tcp.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}

	start := time.Now()
	if err := ts.s.SendBytes(unrequireBytes, true); err != nil {
		t.Fatalf("SendBytes: %v", err)
	}
	select {
	case reason := <-ts.ended:
		if reason == "" || reason == "test cleanup" {
			t.Fatalf("session end reason = %q, want the write error", reason)
		}
	case <-time.After(writeWait / 2):
		t.Fatalf("no Close within %v of the write error", writeWait/2)
	}
	select {
	case <-ts.consumed:
	case <-time.After(writeWait / 2):
		t.Fatalf("Consume still reading %v after the write error: the socket was never closed", time.Since(start))
	}
}

// The Close that cancels the session context owns the session-end reason. A write
// that fails once the context is cancelled must not close the session itself: that
// Close would race the cancelling one for the session lock and could record the
// write error instead (a ban logged as "broken pipe").
func TestProcessOutgoing_CancelledContextLeavesTheReasonToClose(t *testing.T) {
	ts := consumeTestSession(t, SessionFormatEVR, 5*time.Second)
	done := ts.outgoingDone()

	// A Close that has cancelled the context and not yet taken the session lock.
	ts.s.ctxCancelFn()
	tcp := ts.s.conn.UnderlyingConn().(*net.TCPConn)
	if err := tcp.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}
	if err := ts.s.SendBytes(unrequireBytes, true); err != nil {
		t.Fatalf("SendBytes: %v", err)
	}
	select {
	case r := <-ts.ended:
		t.Fatalf("processOutgoing closed the session itself, with reason %q", r)
	case <-time.After(300 * time.Millisecond):
	}

	ts.s.Close("banned", runtime.PresenceReasonDisconnect)
	if r := <-ts.ended; r != "banned" {
		t.Fatalf("session end reason = %q, want %q", r, "banned")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("processOutgoing did not close the socket and exit after the Close")
	}
}
