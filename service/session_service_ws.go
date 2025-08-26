package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/heroiclabs/nakama/v3/server"

	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/atomic"
	"go.uber.org/zap"
)

type ServiceType string

const (
	ServiceTypeIAP          ServiceType = "api"
	ServiceTypeConfig       ServiceType = "config"
	ServiceTypeLogin        ServiceType = "login"
	ServiceTypeLobby        ServiceType = "lobby"
	ServiceTypeServer       ServiceType = "server"
	ServiceTypeNativeServer ServiceType = "native_server"
	ServiceTypeUnknown      ServiceType = "unknown"
)
const (
	StreamLabelMatchService      = "matchservice"
	StreamLabelLoginService      = "loginservice"
	StreamLabelGameServerService = "serverservice"
)

func MessageServiceType(m evr.Message) ServiceType {
	switch m.(type) {
	case *evr.ConfigRequest:
		return ServiceTypeConfig
	case *evr.ReconcileIAP:
		return ServiceTypeIAP
	case *evr.RemoteLogSet, *evr.LoginRequest, *evr.DocumentRequest, *evr.LoggedInUserProfileRequest,
		*evr.ChannelInfoRequest, *evr.UpdateClientProfile, *evr.OtherUserProfileRequest,
		*evr.UserServerProfileUpdateRequest, *evr.GenericMessage:
		return ServiceTypeLogin
	case *evr.LobbyFindSessionRequest, *evr.LobbyCreateSessionRequest, *evr.LobbyJoinSessionRequest,
		*evr.LobbyMatchmakerStatusRequest, *evr.LobbyPingResponse, *evr.LobbyPlayerSessionsRequest,
		*evr.LobbyPendingSessionCancel:
		return ServiceTypeLobby
	case *evr.BroadcasterRegistrationRequest, *evr.GameServerJoinAttempt, *evr.GameServerPlayerRemoved,
		*evr.BroadcasterPlayerSessionsLocked, *evr.BroadcasterPlayerSessionsUnlocked, *evr.BroadcasterSessionEnded:
		return ServiceTypeServer
	default:
		return ServiceTypeUnknown
	}
}

type serviceWS struct {
	sync.Mutex
	logger     *zap.Logger
	config     server.Config
	clientIP   string
	clientPort string

	ctx         context.Context
	ctxCancelFn context.CancelFunc

	wsMessageType      int
	pingPeriodDuration time.Duration
	pongWaitDuration   time.Duration
	writeWaitDuration  time.Duration

	metrics server.Metrics

	stopped                *atomic.Bool
	conn                   *websocket.Conn
	receivedMessageCounter int
	pingTimer              *time.Timer
	pingTimerCAS           *atomic.Uint32
	outgoingCh             chan []byte
	serviceCh              chan []byte
}

func NewServiceWS(logger *zap.Logger, clientIP, clientPort string, conn *websocket.Conn, config server.Config, metrics server.Metrics) *serviceWS {

	ctx, ctxCancelFn := context.WithCancel(context.Background())

	return &serviceWS{
		logger:             logger,
		config:             config,
		clientIP:           clientIP,
		clientPort:         clientPort,
		ctx:                ctx,
		ctxCancelFn:        ctxCancelFn,
		wsMessageType:      websocket.BinaryMessage,
		pingPeriodDuration: time.Duration(config.GetSocket().PingPeriodMs) * time.Millisecond,
		pongWaitDuration:   time.Duration(config.GetSocket().PongWaitMs) * time.Millisecond,
		writeWaitDuration:  time.Duration(config.GetSocket().WriteWaitMs) * time.Millisecond,
		metrics:            metrics,

		stopped:                atomic.NewBool(false),
		conn:                   conn,
		receivedMessageCounter: config.GetSocket().PingBackoffThreshold,
		pingTimer:              time.NewTimer(time.Duration(config.GetSocket().PingPeriodMs) * time.Millisecond),
		pingTimerCAS:           atomic.NewUint32(1),
		outgoingCh:             make(chan []byte, config.GetSocket().OutgoingQueueSize),
		serviceCh:              make(chan []byte, config.GetSocket().OutgoingQueueSize),
	}
}

func (s *serviceWS) IsStopped() bool {
	return s.stopped.Load()
}

func (s *serviceWS) Context() context.Context {
	return s.ctx
}

func (s *serviceWS) Start() chan []byte {
	// Start the outgoing processing loop.
	go s.processOutgoing()

	// Start the incoming processing loop.
	go s.Consume()

	// Return the channel to read incoming messages from.
	return s.serviceCh
}

func (s *serviceWS) Consume() {
	s.conn.SetReadLimit(s.config.GetSocket().MaxMessageSizeBytes)
	if err := s.conn.SetReadDeadline(time.Now().Add(s.pongWaitDuration)); err != nil {
		s.logger.Warn("Failed to set initial read deadline", zap.Error(err))
		return
	}
	s.conn.SetPongHandler(func(string) error {
		s.maybeResetPingTimer()
		return nil
	})
	// Set the read handler to read messages from the connection and send them to the channel.
	// The channel is then used to read messages from the connection in the main loop.
	var messageType int
	var data []byte
	var err error
	var reason string
	for {
		select {
		case <-s.ctx.Done():
			return // Exit the goroutine if the context is done.
		default:
		}
		messageType, data, err = s.conn.ReadMessage()
		if err != nil {
			// Ignore "normal" WebSocket errors.
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived) {
				// Ignore underlying connection being shut down while read is waiting for data.
				var opErr *net.OpError
				if !errors.As(err, &opErr) || opErr.Error() != net.ErrClosed.Error() {
					// EchoVR does not cleanly close connections.
					s.logger.Debug("Error reading message from client", zap.Error(err))
					reason = err.Error()
				}
			}
			break
		}
		if messageType != s.wsMessageType {
			// Expected text but received binary, or expected binary but received text.
			// Disconnect client if it attempts to use this kind of mixed protocol mode.
			s.logger.Debug("Received unexpected WebSocket message type", zap.Int("expected", s.wsMessageType), zap.Int("actual", messageType))
			reason = "received unexpected WebSocket message type"
			break
		}
		s.receivedMessageCounter--
		if s.receivedMessageCounter <= 0 {
			s.receivedMessageCounter = s.config.GetSocket().PingBackoffThreshold
			if !s.maybeResetPingTimer() {
				// Problems resetting the ping timer indicate an error so we need to close the loop.
				reason = "error updating ping timer"
				break
			}
		}
		s.Lock()
		s.serviceCh <- data
		s.Unlock()
		// Update incoming message metrics.
		s.metrics.Message(int64(len(data)), false)
	}
	if reason != "" {
		// Update incoming message metrics.
		s.metrics.Message(int64(len(data)), true)
	}
	s.Close()
}

func (s *serviceWS) maybeResetPingTimer() bool {
	// If there's already a reset in progress there's no need to wait.
	if !s.pingTimerCAS.CompareAndSwap(1, 0) {
		return true
	}
	defer s.pingTimerCAS.CompareAndSwap(0, 1)

	if s.IsStopped() {
		return false
	}
	s.Lock()
	// CAS ensures concurrency is not a problem here.
	if !s.pingTimer.Stop() {
		select {
		case <-s.pingTimer.C:
		default:
		}
	}
	s.pingTimer.Reset(s.pingPeriodDuration)
	err := s.conn.SetReadDeadline(time.Now().Add(s.pongWaitDuration))
	s.Unlock()
	if err != nil {
		s.logger.Warn("Failed to set read deadline", zap.Error(err))
		s.Close()
		return false
	}
	return true
}

func (s *serviceWS) processOutgoing() {
OutgoingLoop:
	for {
		select {
		case <-s.ctx.Done():
			//server.Session is closing, close the outgoing process routine.
			break OutgoingLoop
		case <-s.pingTimer.C:
			// Periodically send pings.
			if msg, ok := s.pingNow(); !ok {
				// If ping fails the session will be stopped, clean up the loop.
				s.logger.Warn("Ping failed", zap.String("reason", msg))
				break OutgoingLoop
			}
		case payload := <-s.outgoingCh:
			if s.IsStopped() {
				// The connection may have stopped between the payload being queued on the outgoing channel and reaching here.
				// If that's the case then abort outgoing processing at this point and exit.
				break OutgoingLoop
			}
			messages, err := evr.ParsePacket(payload)
			if err != nil {
				s.logger.Warn("Failed to parse packet", zap.Error(err))
			} else {
				for _, message := range messages {
					s.logger.Debug("Sending message", zap.String("message_type", fmt.Sprintf("%T", message)), zap.Any("message", message))
				}
			}

			s.Lock()
			// Process the outgoing message queue.
			if err := s.conn.SetWriteDeadline(time.Now().Add(s.writeWaitDuration)); err != nil {
				s.Unlock()
				s.logger.Warn("Failed to set write deadline", zap.Error(err))
				break OutgoingLoop
			}
			if err := s.conn.WriteMessage(s.wsMessageType, payload); err != nil {
				s.Unlock()
				s.logger.Warn("Could not write message", zap.Error(err))
				break OutgoingLoop
			}
			s.Unlock()

			// Update outgoing message metrics.
			s.metrics.MessageBytesSent(int64(len(payload)))
		}
	}

	s.Close()
}

func (s *serviceWS) pingNow() (string, bool) {
	if s.IsStopped() {
		return "", false
	}
	s.Lock()
	if err := s.conn.SetWriteDeadline(time.Now().Add(s.writeWaitDuration)); err != nil {
		s.Unlock()
		s.logger.Warn("Could not set write deadline to ping", zap.Error(err))
		return err.Error(), false
	}
	err := s.conn.WriteMessage(websocket.PingMessage, []byte{})
	s.Unlock()
	if err != nil {
		s.logger.Warn("Could not send ping", zap.Error(err))
		return err.Error(), false
	}
	return "", true
}

func (s *serviceWS) SendBytes(payload []byte, reliable bool) error {
	// Attempt to queue messages and observe failures.
	select {
	case s.outgoingCh <- payload:
		return nil
	default:
		// The outgoing queue is full, likely because the remote client can't keep up.
		// Terminate the connection immediately because the only alternative that doesn't block the server is
		// to start dropping messages, which might cause unexpected behaviour.
		s.logger.Warn("Could not write message, session outgoing queue full")
		// Close in a goroutine as the method can block
		go s.Close()
		return server.ErrSessionQueueFull
	}
}

func (s *serviceWS) SendEVR(envelope Envelope) error {
	if s.IsStopped() {
		return errors.New("service is stopped, not sending message")
	}

	if envelope.State == RequireStateUnrequired {
		envelope.Messages = append(envelope.Messages, &evr.STcpConnectionUnrequireEvent{})
	}

	payload := make([]byte, 0, 1024)
	for _, message := range envelope.Messages {
		s.logger.Debug("Sending message", zap.String("message_type", fmt.Sprintf("%T", message)), zap.Any("message", message))
		data, err := evr.Marshal(message)
		if err != nil {
			return fmt.Errorf("failed to marshal message: %w", err)
		}
		if len(payload)+len(data) > int(s.config.GetSocket().MaxMessageSizeBytes) {
			// Send the message in chunks if it exceeds the maximum size.
			if err := s.SendBytes(payload, true); err != nil {
				return fmt.Errorf("failed to send message: %w", err)
			}
			payload = make([]byte, 0, 1024)
		}

		payload = append(payload, data...)
	}
	if err := s.SendBytes(payload, true); err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}
	return nil
}

func (s *serviceWS) Close() {

	// Cancel any ongoing operations tied to this session.
	s.ctxCancelFn()

	if s.IsStopped() {
		return // Already stopped.
	}
	s.stopped.Store(true)

	if s.logger.Core().Enabled(zap.DebugLevel) {
		s.logger.Info("Cleaning up closed client connection")
	}

	// Clean up internals.
	s.pingTimer.Stop()

	// Send close message.
	if err := s.conn.WriteControl(websocket.CloseMessage, []byte{}, time.Now().Add(s.writeWaitDuration)); err != nil {
		// This may not be possible if the socket was already fully closed by an error.
		s.logger.Debug("Could not send close message", zap.Error(err))
	}
	// Close WebSocket.
	if err := s.conn.Close(); err != nil {
		s.logger.Debug("Could not close", zap.Error(err))
	}

	s.logger.Info("Closed client connection")
}
