package server

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
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

// flushTestSession is a real EVR sessionWS with its processOutgoing routine
// running, as Consume starts it, over a real websocket. It returns the session
// and the client end of the socket.
func flushTestSession(t *testing.T) (*sessionWS, *websocket.Conn) {
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

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var endReason string
	s := &sessionWS{
		logger: zap.NewNop(), id: uuid.Must(uuid.NewV4()), userID: uuid.Must(uuid.NewV4()),
		username: atomic.NewString("u"), ctx: ctx, ctxCancelFn: cancel, conn: conn,
		format: SessionFormatEVR, wsMessageType: websocket.BinaryMessage,
		writeWaitDuration: time.Second, pingTimer: time.NewTimer(time.Hour),
		outgoingCh: make(chan []byte, 16), metrics: &testMetrics{},
		matchmaker: closeTestMatchmaker{}, tracker: &testTracker{}, statusRegistry: closeTestStatusRegistry{},
		sessionRegistry: NewLocalSessionRegistry(&testMetrics{}), runtime: closeTestRuntime(&endReason),
	}
	s.outgoingRunning.Store(true) // as Consume does, before starting the routine
	go s.processOutgoing()
	return s, client
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
				s, client := flushTestSession(t)

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
			s, client := flushTestSession(t)
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
