package service

import (
	"context"
	"fmt"
	"sync"

	evr "github.com/echotools/nakama/v3/protocol"

	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/gorilla/websocket"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	StreamModeService = 0x10 + iota
	StreamModeEntrant
	StreamModeGameServer
	StreamModeMatchmaking
	StreamModeGuildGroup
	StreamModeMatchmaker
)

type ByteSender interface {
	SendBytes(payload []byte, reliable bool) error
}

type RequireState int

const (
	RequireStateNone RequireState = iota
	RequireStateRequired
	RequireStateUnrequired
)

type Envelope struct {
	ServiceType ServiceType
	Messages    []evr.Message
	State       RequireState
}

var _ = server.Session(&sessionEVR{})

type sessionEVR struct {
	sync.Mutex
	logger     *zap.Logger
	config     server.Config
	id         uuid.UUID
	userID     uuid.UUID
	username   *atomic.String
	vars       map[string]string
	expiry     int64
	clientIP   string
	clientPort string
	lang       string

	ctx         context.Context
	ctxCancelFn context.CancelFunc

	protojsonMarshaler   *protojson.MarshalOptions
	protojsonUnmarshaler *protojson.UnmarshalOptions
	wsMessageType        int

	sessionRegistry server.SessionRegistry
	statusRegistry  server.StatusRegistry
	matchmaker      server.Matchmaker
	tracker         server.Tracker
	metrics         server.Metrics

	stopped    bool
	services   *server.MapOf[ServiceType, *serviceWS]
	incomingCh chan []byte
	outgoingCh chan Envelope
	closeMu    sync.Mutex

	pipeline *Pipeline

	// TODO relayOutgoing should be a slice of message types the user wants relayed to them.
	relayOutgoing bool // The user wants (some) outgoing messages relayed to them via discord
	debug         bool // The user wants debug information

	xpid evr.XPID
}

func NewSessionWSEVR(logger *zap.Logger, config server.Config, sessionID, userID uuid.UUID, params SessionParameters, vars map[string]string, clientIP, clientPort string, protojsonMarshaler *protojson.MarshalOptions, protojsonUnmarshaler *protojson.UnmarshalOptions, service *serviceWS, sessionRegistry server.SessionRegistry, statusRegistry server.StatusRegistry, matchmaker server.Matchmaker, tracker server.Tracker, metrics server.Metrics, pipeline *Pipeline) *sessionEVR {

	ctx, ctxCancelFn := context.WithCancel(context.Background())
	logger = logger.With(
		zap.String("sid", sessionID.String()),
		zap.String("uid", userID.String()),
		zap.String("username", params.UserID()),
		zap.String("xpid", params.xpID.String()))

	// Store the session parameters in the context for access by services.
	ctx = context.WithValue(ctx, ctxSessionParametersKey{}, atomic.NewPointer(&params))

	return &sessionEVR{
		logger:               logger,
		config:               config,
		id:                   sessionID,
		userID:               userID,
		username:             atomic.NewString(params.profile.Username()),
		vars:                 vars,
		clientIP:             clientIP,
		clientPort:           clientPort,
		lang:                 "en",
		ctx:                  ctx,
		ctxCancelFn:          ctxCancelFn,
		protojsonMarshaler:   protojsonMarshaler,
		protojsonUnmarshaler: protojsonUnmarshaler,
		wsMessageType:        websocket.BinaryMessage,

		sessionRegistry: sessionRegistry,
		statusRegistry:  statusRegistry,
		matchmaker:      matchmaker,
		tracker:         tracker,
		metrics:         metrics,

		stopped:    false,
		services:   &server.MapOf[ServiceType, *serviceWS]{},
		incomingCh: make(chan []byte, config.GetSocket().OutgoingQueueSize),
		outgoingCh: make(chan Envelope, config.GetSocket().OutgoingQueueSize),

		pipeline:      pipeline,
		xpid:          params.xpID,
		relayOutgoing: false,
		debug:         false,
	}
}

func (s *sessionEVR) Logger() *zap.Logger {
	return s.logger
}

func (s *sessionEVR) ID() uuid.UUID {
	return s.id
}

func (s *sessionEVR) UserID() uuid.UUID {
	return s.userID
}

func (s *sessionEVR) ClientIP() string {
	return s.clientIP
}

func (s *sessionEVR) ClientPort() string {
	return s.clientPort
}

func (s *sessionEVR) Lang() string {
	return s.lang
}

func (s *sessionEVR) Context() context.Context {
	return s.ctx
}

func (s *sessionEVR) Username() string {
	return s.username.Load()
}

func (s *sessionEVR) SetUsername(username string) {
	s.username.Store(username)
}

func (s *sessionEVR) Vars() map[string]string {
	return s.vars
}

func (s *sessionEVR) Expiry() int64 {
	return s.expiry
}

func (s *sessionEVR) Consume() {

	go s.processOutgoing()
	var reason string
	var data []byte
IncomingLoop:
	for {
		select {
		case <-s.ctx.Done():
			return
		case data = <-s.incomingCh:
			messages, err := evr.ParsePacket(data)
			if err != nil {
				// If the payload is malformed the client is incompatible or misbehaving, either way disconnect it now.
				s.logger.Warn("Received malformed payload", zap.Binary("data", data), zap.Error(err))
				reason = "received malformed payload"
				break IncomingLoop
			}
			logger := s.Logger()
			start := time.Now()
			for _, message := range messages {
				if logger.Core().Enabled(zap.DebugLevel) { // remove extra heavy reflection processing
					logger = logger.With(zap.String("request", fmt.Sprintf("%s", message)))
					logger.Debug("Received message")
				}
				if _, ok := message.(*evr.UnimplementedMessage); ok {
					logger.Warn("Received unimplemented message", zap.Any("message", message))
					continue // skip unimplemented messages
				}

				// Send to the Evr pipeline for routing/processing.
				if !s.pipeline.ProcessRequest(logger, s, message) {
					reason = "error processing message"
					break IncomingLoop
				}
			}
			// Update incoming message metrics.
			s.metrics.CustomTimer("socket_incoming_message_processing_time", nil, time.Millisecond*time.Since(start))
		}
	}
	if reason != "" {
		// Update incoming message metrics.
		s.metrics.Message(int64(len(data)), true)
	}
	s.Close(reason, runtime.PresenceReasonDisconnect)
}

func (s *sessionEVR) processOutgoing() {
	var reason string

OutgoingLoop:
	for {
		select {
		case <-s.ctx.Done():
			//server.Session is closing, close the outgoing process routine.
			break OutgoingLoop
		case envelope := <-s.outgoingCh:

			// Determine the correct service type for the message.
			svc, ok := s.services.Load(envelope.ServiceType)
			if !ok {
				s.logger.Warn("Service not found", zap.String("service_type", string(envelope.ServiceType)))
				break OutgoingLoop
			}
			if err := svc.SendEVR(envelope); err != nil {
				s.logger.Warn("Failed to send message to service", zap.Error(err))
				break OutgoingLoop
			}
		}
	}

	s.Close(reason, runtime.PresenceReasonDisconnect)
}

func (s *sessionEVR) Format() server.SessionFormat {
	return 42
}

func (s *sessionEVR) Send(envelope *rtapi.Envelope, reliable bool) error {
	messages, err := s.pipeline.ProcessOutgoing(s.logger, s, envelope)
	if err != nil {
		return err
	}
	if len(messages) == 0 {
		// Nothing to send.
		return nil
	}

	// Route them to the correct
	for i, msg := range messages {
		if msg == nil {
			s.logger.Warn("Nil message in outgoing messages")
			continue
		}
		serviceType := MessageServiceType(messages[0])
		if serviceType == ServiceTypeUnknown {
			s.logger.Warn("Unknown service type in outgoing message", zap.Any("message", msg))
			continue
		}
		if _, ok := s.services.Load(serviceType); !ok {
			s.logger.Warn("Service not found for outgoing message", zap.String("service_type", string(serviceType)))
			continue
		}
		// Send all messages as a single envelope to preserve ordering.
		select {
		case s.outgoingCh <- Envelope{ServiceType: ServiceTypeLogin, Messages: messages[i : i+1], State: RequireStateNone}:
			return nil
		default:
			// The outgoing queue is full, likely because the remote client can't keep up.
			return server.ErrSessionQueueFull
		}
	}
	return nil
}

func (s *sessionEVR) SendBytes(payload []byte, reliable bool) error {
	if len(payload) == 0 {
		return nil
	}
	request := &rtapi.Envelope{}
	if err := s.protojsonUnmarshaler.Unmarshal(payload, request); err != nil {
		s.logger.Error("Failed to unmarshal message", zap.Error(err))
		return fmt.Errorf("failed to unmarshal message: %w", err)
	}
	s.pipeline.ProcessOutgoing(s.logger, s, request)
	return nil
}

func (s *sessionEVR) CloseLock() {
	s.closeMu.Lock()
}

func (s *sessionEVR) CloseUnlock() {
	s.closeMu.Unlock()
}

func (s *sessionEVR) Close(msg string, reason runtime.PresenceReason, envelopes ...*rtapi.Envelope) {
	s.CloseLock()
	// Cancel any ongoing operations tied to this session.
	s.ctxCancelFn()
	s.CloseUnlock()

	s.Lock()
	if s.stopped {
		s.Unlock()
		return
	}
	s.stopped = true
	s.Unlock()

	if s.logger.Core().Enabled(zap.DebugLevel) {
		s.logger.Info("Cleaning up closed client connection")
	}

	// When connection close originates internally in the session, ensure cleanup of external resources and references.
	if err := s.matchmaker.RemoveSessionAll(s.id.String()); err != nil {
		s.logger.Warn("Failed to remove all matchmaking tickets", zap.Error(err))
	}
	if s.logger.Core().Enabled(zap.DebugLevel) {
		s.logger.Info("Cleaned up closed connection matchmaker")
	}
	s.tracker.UntrackAll(s.id, reason)
	if s.logger.Core().Enabled(zap.DebugLevel) {
		s.logger.Info("Cleaned up closed connection tracker")
	}
	s.statusRegistry.UnfollowAll(s.id)
	if s.logger.Core().Enabled(zap.DebugLevel) {
		s.logger.Info("Cleaned up closed connection status registry")
	}
	s.sessionRegistry.Remove(s.id)
	if s.logger.Core().Enabled(zap.DebugLevel) {
		s.logger.Info("Cleaned up closed connection session registry")
	}

	// Send final messages, if any are specified.
	for _, envelope := range envelopes {
		s.pipeline.ProcessOutgoing(s.logger, s, envelope)
	}

	s.services.Range(func(key ServiceType, service *serviceWS) bool {
		service.Close()
		return true
	})

	s.logger.Info("Closed client connection")

}

func (s *sessionEVR) AddService(serviceType ServiceType, service *serviceWS) error {
	s.Lock()
	if svc, ok := s.services.LoadAndDelete(serviceType); ok {
		s.logger.Debug("Replacing existing service", zap.String("service_type", string(serviceType)))
		s.Unlock()
		svc.Close()
		return nil
	}
	s.Unlock()
	service.Lock()
	// Receive messages from the service.
	service.serviceCh = s.incomingCh
	service.Unlock()
	// The connection is stopped, so just replace it.
	s.services.Store(serviceType, service)
	return nil
}

func (s *sessionEVR) SendEVR(envelope Envelope) error {
	select {
	case s.outgoingCh <- envelope:
		return nil
	default:
		// The outgoing queue is full, likely because the remote client can't keep up.
		return server.ErrSessionQueueFull
	}
}
