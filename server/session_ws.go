// Copyright 2018 The Nakama Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/gorilla/websocket"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	StreamModeService = 0x10 + iota
	StreamModeEntrant
	StreamModeGameServer
	StreamModeMatchmaking
	StreamModeLobbyGroup
)

const (
	StreamLabelMatchService      = "matchservice"
	StreamLabelLoginService      = "loginservice"
	StreamLabelGameServerService = "serverservice"
)

var (
	ErrSessionQueueFull = errors.New("session outgoing queue full")

	StreamContextLogin      = uuid.NewV5(uuid.Nil, "login")
	StreamContextMatch      = uuid.NewV5(uuid.Nil, "match")
	StreamContextGameServer = uuid.NewV5(uuid.Nil, "gameserver")

	hmdOverridePattern = regexp.MustCompile(`^[-a-zA-Z0-9_]+$`)
	featurePattern     = regexp.MustCompile(`^[a-z0-9_]+$`)
	discordIDPattern   = regexp.MustCompile(`^[0-9]+$`)
)

type (
	sessionWS struct {
		sync.Mutex
		logger     *zap.Logger
		baseLogger *zap.Logger
		config     Config
		id         uuid.UUID
		format     SessionFormat
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
		pingPeriodDuration   time.Duration
		pongWaitDuration     time.Duration
		writeWaitDuration    time.Duration

		sessionRegistry SessionRegistry
		statusRegistry  StatusRegistry
		matchmaker      Matchmaker
		tracker         Tracker
		metrics         Metrics
		pipeline        *Pipeline
		runtime         *Runtime

		stopped                bool
		conn                   *websocket.Conn
		receivedMessageCounter int
		pingTimer              *time.Timer
		pingTimerCAS           *atomic.Uint32
		outgoingCh             chan []byte
		closeMu                sync.Mutex

		storageIndex StorageIndex
		evrPipeline  *EvrPipeline
	}

	// Keys used for storing/retrieving user information in the context of a request after authentication.
	ctxSessionParametersKey struct{} // The Session Parameters
)

type SessionParameters struct {
	node         string                    // The node name
	evrID        atomic.Value              // The EchoVR ID
	discordID    atomic.String             // The Discord ID
	loginSession atomic.Pointer[sessionWS] // The login session ID
	lobbySession atomic.Pointer[sessionWS] // The match session ID

	hmdSerialOverride       string // The HMD Serial Override
	authDiscordID           string // The Discord ID use for authentication
	authPassword            string // The Password use for authentication
	userDisplayNameOverride string // The display name override (user-defined)

	ExternalServerAddr string      // The external server address (IP:port)
	isVPN              bool        // The user is using a VPN
	SupportedFeatures  []string    // The features from the urlparam
	RequiredFeatures   []string    // The required_features from the urlparam
	isVR               atomic.Bool // The user is using a VR headset
	isPCVR             atomic.Bool // The user is using a PCVR headset
	relayOutgoing      atomic.Bool // The user is relaying outgoing messages

	accountMetadata     atomic.Pointer[AccountMetadata] // The account metadata
	guildGroupMetadatas atomic.Value                    // The guild group metadata (map[uuid.UUID]*GroupMetadata)
	memberships         atomic.Value                    // The guild group memberships (map[string]*GuildGroupMembership)

	urlParameters map[string][]string // The URL parameters
}

func (p *SessionParameters) IsVR() bool {
	return p.isVR.Load()
}

func (p *SessionParameters) SetIsVR(v bool) {
	p.isVR.Store(v)
}

func (p *SessionParameters) SetIsPCVR(v bool) {
	p.isPCVR.Store(v)
}

func (p *SessionParameters) IsPCVR() bool {
	return p.isPCVR.Load()
}

func (p *SessionParameters) EvrID() evr.EvrId {
	return p.evrID.Load().(evr.EvrId)
}

func (p *SessionParameters) SetEvrID(v evr.EvrId) {
	p.evrID.Store(v)
}

func (p *SessionParameters) DiscordID() string {
	return p.discordID.Load()
}

func (p *SessionParameters) SetDiscordID(v string) {
	p.discordID.Store(v)
}
func (p *SessionParameters) AccountMetadata() *AccountMetadata {
	return p.accountMetadata.Load()
}

func (p *SessionParameters) SetAccountMetadata(v *AccountMetadata) {
	p.accountMetadata.Store(v)
}

func (p *SessionParameters) GuildGroupMetadatas() map[uuid.UUID]*GroupMetadata {
	return p.guildGroupMetadatas.Load().(map[uuid.UUID]*GroupMetadata)
}

func (p *SessionParameters) LobbySession() *sessionWS {
	return p.lobbySession.Load()
}

func (p *SessionParameters) SetLobbySession(v *sessionWS) {
	p.lobbySession.Store(v)
}

func (p *SessionParameters) LoginSession() *sessionWS {
	return p.loginSession.Load()
}

func (p *SessionParameters) SetLoginSession(v *sessionWS) {
	p.loginSession.Store(v)
}

func (p *SessionParameters) RelayOutgoing() bool {
	return p.relayOutgoing.Load()
}

func (p *SessionParameters) SetRelayOutgoing(v bool) {
	p.relayOutgoing.Store(v)
}

func (p *SessionParameters) Memberships() map[string]GuildGroupMembership {
	return p.memberships.Load().(map[string]GuildGroupMembership)
}

func (p *SessionParameters) SetMemberships(v map[string]GuildGroupMembership) {
	p.memberships.Store(v)
}

func NewSessionWS(logger *zap.Logger, config Config, format SessionFormat, sessionID, userID uuid.UUID, username string, vars map[string]string, expiry int64, clientIP, clientPort, lang string, protojsonMarshaler *protojson.MarshalOptions, protojsonUnmarshaler *protojson.UnmarshalOptions, conn *websocket.Conn, sessionRegistry SessionRegistry, statusRegistry StatusRegistry, matchmaker Matchmaker, tracker Tracker, metrics Metrics, pipeline *Pipeline, evrPipeline *EvrPipeline, runtime *Runtime, request http.Request, storageIndex StorageIndex) Session {
	sessionLogger := logger.With(zap.String("sid", sessionID.String()))

	if userID != uuid.Nil {
		sessionLogger = sessionLogger.With(zap.String("uid", userID.String()))
	}
	sessionLogger.Info("New WebSocket session connected", zap.String("requestUri", request.URL.Path), zap.String("query", request.URL.RawQuery), zap.Uint8("format", uint8(format)))

	ctx, ctxCancelFn := context.WithCancel(context.Background())

	ctx = context.WithValue(ctx, ctxVarsKey{}, vars)     // apiServer compatibility
	ctx = context.WithValue(ctx, ctxExpiryKey{}, expiry) // apiServer compatibility

	// Add the URL parameters to the context
	urlParams := make(map[string][]string, 0)
	for k, v := range request.URL.Query() {
		urlParams[k] = v
	}

	params := SessionParameters{
		node:                    pipeline.node,
		hmdSerialOverride:       parseUserQueryFunc(&request, "hmdserial", 32, hmdOverridePattern),
		authDiscordID:           parseUserQueryFunc(&request, "discordid", 20, discordIDPattern),
		authPassword:            parseUserQueryFunc(&request, "password", 32, nil),
		userDisplayNameOverride: parseUserQueryFunc(&request, "ign", 20, nil),
		ExternalServerAddr:      parseUserQueryFunc(&request, "serveraddr", 64, nil),
		SupportedFeatures:       parseUserQueryCommaDelimited(&request, "features", 32, featurePattern),
		RequiredFeatures:        parseUserQueryCommaDelimited(&request, "requires", 32, featurePattern),
		isVPN:                   evrPipeline.ipqsClient.IsVPN(clientIP),
		urlParameters:           urlParams,
	}

	ctx = context.WithValue(ctx, ctxSessionParametersKey{}, &params)

	for _, f := range params.RequiredFeatures {
		if !slices.Contains(params.SupportedFeatures, f) {
			params.SupportedFeatures = append(params.SupportedFeatures, f)
		}
	}
	slices.Sort(params.SupportedFeatures)

	wsMessageType := websocket.TextMessage
	if format == SessionFormatProtobuf || format == SessionFormatEVR {
		wsMessageType = websocket.BinaryMessage
	}

	return &sessionWS{
		logger:      sessionLogger,
		baseLogger:  logger,
		config:      config,
		id:          sessionID,
		format:      format,
		userID:      userID,
		username:    atomic.NewString(username),
		vars:        vars,
		expiry:      expiry,
		clientIP:    clientIP,
		clientPort:  clientPort,
		lang:        lang,
		ctx:         ctx,
		ctxCancelFn: ctxCancelFn,

		protojsonMarshaler:   protojsonMarshaler,
		protojsonUnmarshaler: protojsonUnmarshaler,

		wsMessageType:      wsMessageType,
		pingPeriodDuration: time.Duration(config.GetSocket().PingPeriodMs) * time.Millisecond,
		pongWaitDuration:   time.Duration(config.GetSocket().PongWaitMs) * time.Millisecond,
		writeWaitDuration:  time.Duration(config.GetSocket().WriteWaitMs) * time.Millisecond,

		sessionRegistry: sessionRegistry,
		statusRegistry:  statusRegistry,
		matchmaker:      matchmaker,
		tracker:         tracker,
		metrics:         metrics,
		pipeline:        pipeline,
		runtime:         runtime,
		evrPipeline:     evrPipeline,
		storageIndex:    storageIndex,

		stopped:                false,
		conn:                   conn,
		receivedMessageCounter: config.GetSocket().PingBackoffThreshold,
		pingTimer:              time.NewTimer(time.Duration(config.GetSocket().PingPeriodMs) * time.Millisecond),
		pingTimerCAS:           atomic.NewUint32(1),
		outgoingCh:             make(chan []byte, config.GetSocket().OutgoingQueueSize),
	}
}

func parseUserQueryFunc(request *http.Request, key string, maxLength int, pattern *regexp.Regexp) string {
	if v := request.URL.Query().Get(key); v != "" {
		if len(v) > maxLength {
			v = v[:maxLength]
		}
		if pattern != nil && !pattern.MatchString(v) {
			return ""
		}

	}

	return ""
}

func parseUserQueryCommaDelimited(request *http.Request, key string, maxLength int, pattern *regexp.Regexp) []string {
	// Add the items list to the urlparam, sanitizing it
	items := make([]string, 0)
	if v := request.URL.Query().Get(key); v != "" {
		s := strings.Split(v, ",")
		for _, f := range s {
			if f == "" {
				continue
			}
			if len(f) > maxLength {
				f = f[:maxLength]
			}
			if pattern != nil && !pattern.MatchString(f) {
				continue
			}
			items = append(items, f)
		}
		if len(items) == 0 {
			return nil
		}
	}
	slices.Sort(items)
	return items
}

func (s *sessionWS) SetIdentity(userID uuid.UUID, evrID evr.EvrId, username string) error {
	// Each player has a single login connection, which will act as the core session.
	// When this connection is terminated, all other connections should be terminated.

	// The EVR context is used by the secondary connections, once they have validated.
	// It ensures that all secondary connections are disconnected when the login
	// connection  is disconnected. It is passed at the pipeline level to ensure
	// that evr functions have access to it.

	// Replace the session context with a derived one that includes the login session ID and the EVR ID
	ctx := s.Context()
	s.Lock()
	ctx = context.WithValue(ctx, ctxUserIDKey{}, userID)     // apiServer compatibility
	ctx = context.WithValue(ctx, ctxUsernameKey{}, username) // apiServer compatibility
	s.ctx = ctx
	s.userID = userID
	s.SetUsername(username)
	s.logger = s.logger.With(zap.String("loginsid", s.id.String()), zap.String("uid", s.userID.String()), zap.String("evrid", evrID.Token()), zap.String("username", username))
	s.Unlock()

	// Register initial status tracking and presence(s) for this session.
	s.statusRegistry.Follow(s.id, map[uuid.UUID]struct{}{s.userID: {}})

	// Both notification and status presence.
	s.tracker.TrackMulti(ctx, s.id, []*TrackerOp{
		// EVR packet data stream for the login session by user ID, and service ID, with EVR ID
		{
			Stream: PresenceStream{Mode: StreamModeService, Subject: s.userID, Subcontext: StreamContextLogin},
			Meta:   PresenceMeta{Format: s.format, Username: evrID.Token(), Hidden: true},
		},
		// EVR packet data stream for the login session by session ID and service ID, with EVR ID
		{
			Stream: PresenceStream{Mode: StreamModeService, Subject: s.id, Subcontext: StreamContextLogin},
			Meta:   PresenceMeta{Format: s.format, Username: evrID.Token(), Hidden: true},
		},
		// Notification presence.
		{
			Stream: PresenceStream{Mode: StreamModeNotifications, Subject: s.userID},
			Meta:   PresenceMeta{Format: s.format, Username: username, Hidden: true},
		},

		// Status presence.
		{
			Stream: PresenceStream{Mode: StreamModeStatus, Subject: s.userID},
			Meta:   PresenceMeta{Format: s.format, Username: username, Status: ""},
		},
	}, s.userID)

	return nil
}

func (s *sessionWS) BroadcasterSession(userID uuid.UUID, username string, serverID uint64) error {
	// Broadcaster's are "partial" sessions, and aren't directly associated with the user.
	// There's no information that directly links this connection to the login connection.

	// This is the first time the session has been validated.

	ctx := context.WithValue(s.Context(), ctxUserIDKey{}, userID) // apiServer compatibility
	ctx = context.WithValue(ctx, ctxUsernameKey{}, username)      // apiServer compatibility
	ctx = context.WithValue(ctx, ctxVarsKey{}, s.vars)            // apiServer compatibility
	ctx = context.WithValue(ctx, ctxExpiryKey{}, s.expiry)        // apiServer compatibility

	s.SetUsername(username)
	s.Lock()
	s.ctx = ctx
	s.userID = userID
	s.logger = s.logger.With(zap.String("operator_id", userID.String()), zap.String("server_id", fmt.Sprintf("%d", serverID)))
	s.Unlock()

	s.tracker.TrackMulti(ctx, s.id, []*TrackerOp{
		// EVR packet data stream for the login session by Session ID and broadcaster ID
		{
			Stream: PresenceStream{Mode: StreamModeService, Subject: s.userID, Subcontext: StreamContextGameServer},
			Meta:   PresenceMeta{Format: s.format, Username: s.username.String(), Hidden: true},
		},
		// EVR packet data stream by session ID and broadcaster ID
		{
			Stream: PresenceStream{Mode: StreamModeService, Subject: s.id, Subcontext: StreamContextGameServer},
			Meta:   PresenceMeta{Format: s.format, Username: s.username.String(), Hidden: true},
		},
	}, s.userID)

	return nil
}

// ValidateSession validates the session information provided by the client.
func (s *sessionWS) ValidateSession(loginSessionID uuid.UUID) error {
	if loginSessionID == uuid.Nil {
		return fmt.Errorf("login session ID is nil")
	}

	// EVR uses multiple connections, that reference the login session, without using session IDs for any of the secondary connections.
	// The login session is the primary session, and the secondary sessions are validated against it.

	// If the session is not authenticated, use the login session to set the session information.
	if s.UserID() == uuid.Nil {

		// Obtain the login session.
		loginSession, ok := s.sessionRegistry.Get(loginSessionID).(*sessionWS)
		if !ok || loginSession == nil {
			return fmt.Errorf("login session not found: %v", loginSessionID)
		}

		// Use the context to avoid any locking issues.
		loginCtx := loginSession.Context()

		params, ok := loginCtx.Value(ctxSessionParametersKey{}).(*SessionParameters)
		if !ok {
			return fmt.Errorf("login session does not have session parameters")
		}

		ctx := context.WithValue(s.Context(), ctxSessionParametersKey{}, params)

		// Store this session as the lobby session.
		params.lobbySession.Store(s)

		// cancel/disconnect this session if the login session is cancelled.
		go func() {
			<-loginCtx.Done()
			s.Close("echovr login session closed", runtime.PresenceReasonDisconnect)
		}()

		// Create a derived context for this session.

		ctx = context.WithValue(s.Context(), ctxUserIDKey{}, loginSession.UserID()) // apiServer compatibility
		ctx = context.WithValue(ctx, ctxUsernameKey{}, loginSession.Username())     // apiServer compatibility

		// Set the session information
		s.Lock()
		s.ctx = ctx
		s.userID = loginSession.UserID()
		s.SetUsername(loginSession.Username())
		s.logger = s.logger.With(zap.String("loginsid", loginSessionID.String()), zap.String("uid", s.userID.String()), zap.String("evrid", params.EvrID().Token()), zap.String("username", s.Username()))
		s.Unlock()

	}
	return nil
}

func (s *sessionWS) Logger() *zap.Logger {
	return s.logger
}

func (s *sessionWS) ID() uuid.UUID {
	return s.id
}

func (s *sessionWS) UserID() uuid.UUID {
	return s.userID
}

func (s *sessionWS) ClientIP() string {
	return s.clientIP
}

func (s *sessionWS) ClientPort() string {
	return s.clientPort
}

func (s *sessionWS) Lang() string {
	return s.lang
}

func (s *sessionWS) Context() context.Context {
	return s.ctx
}

func (s *sessionWS) Username() string {
	return s.username.Load()
}

func (s *sessionWS) SetUsername(username string) {
	s.username.Store(username)
}

func (s *sessionWS) Vars() map[string]string {
	return s.vars
}

func (s *sessionWS) Expiry() int64 {
	return s.expiry
}

func (s *sessionWS) Consume() {
	// Fire an event for session start.
	if fn := s.runtime.EventSessionStart(); fn != nil {
		fn(s.userID.String(), s.username.Load(), s.vars, s.expiry, s.id.String(), s.clientIP, s.clientPort, s.lang, time.Now().UTC().Unix())
	}

	s.conn.SetReadLimit(s.config.GetSocket().MaxMessageSizeBytes)
	if err := s.conn.SetReadDeadline(time.Now().Add(s.pongWaitDuration)); err != nil {
		s.logger.Warn("Failed to set initial read deadline", zap.Error(err))
		go s.Close("failed to set initial read deadline", runtime.PresenceReasonDisconnect)
		return
	}
	s.conn.SetPongHandler(func(string) error {
		s.maybeResetPingTimer()
		return nil
	})

	// Start a routine to process outbound messages.
	go s.processOutgoing()

	var reason string
	var data []byte

	isDebug := s.logger.Core().Enabled(zap.DebugLevel)

IncomingLoop:
	for {

		messageType, data, err := s.conn.ReadMessage()
		if err != nil {
			// Ignore "normal" WebSocket errors.
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived) {
				// Ignore underlying connection being shut down while read is waiting for data.
				if e, ok := err.(*net.OpError); !ok || e.Err.Error() != "use of closed network connection" {
					if s.format != SessionFormatEVR {
						// EchoVR does not cleanly close connections.
						s.logger.Debug("Error reading message from client", zap.Error(err))
					}
					reason = err.Error()
				}
			}
			break
		}
		start := time.Now()
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

		if s.format == SessionFormatEVR {
			// EchoVR messages do not map directly onto nakama messages.

			requests, err := evr.ParsePacket(data)
			if err != nil {
				if errors.Is(err, evr.ErrSymbolNotFound) {
					s.logger.Debug("Received unknown message", zap.Error(err))
					continue
				}
				// If the payload is malformed the client is incompatible or misbehaving, either way disconnect it now.
				s.logger.Warn("Received malformed payload", zap.Binary("data", data), zap.Error(err))
				reason = "received malformed payload"
				break
			}
			// Send to the Evr pipeline for routing/processing.

			for _, request := range requests {
				logger := s.logger.With(zap.String("request_type", fmt.Sprintf("%T", request)))
				if isDebug { // remove extra heavy reflection processing
					logger = logger.With(zap.String("request", fmt.Sprintf("%s", request)))
					logger.Debug("Received message")
				}
				if request == nil {
					continue
				}

				if !s.evrPipeline.ProcessRequestEVR(logger, s, request) {
					reason = "error processing message"
					break IncomingLoop
				}
			}
		} else {
			request := &rtapi.Envelope{}
			switch s.format {
			case SessionFormatProtobuf:
				err = proto.Unmarshal(data, request)
			case SessionFormatJson:
				fallthrough
			default:
				err = s.protojsonUnmarshaler.Unmarshal(data, request)
			}
			if err != nil {
				// If the payload is malformed the client is incompatible or misbehaving, either way disconnect it now.
				s.logger.Warn("Received malformed payload", zap.Binary("data", data))
				reason = "received malformed payload"
				break
			}

			switch request.Cid {
			case "":
				if !s.pipeline.ProcessRequest(s.logger, s, request) {
					reason = "error processing message"
					break IncomingLoop
				}
			default:
				requestLogger := s.logger.With(zap.String("cid", request.Cid))
				if !s.pipeline.ProcessRequest(requestLogger, s, request) {
					reason = "error processing message"
					break IncomingLoop
				}
			}
		}
		// Update incoming message metrics.
		s.metrics.Message(int64(len(data)), false)
		s.metrics.CustomTimer("socket_incoming_message_processing_time", nil, time.Millisecond*time.Since(start))
	}

	if reason != "" {
		// Update incoming message metrics.
		s.metrics.Message(int64(len(data)), true)
	}

	s.Close(reason, runtime.PresenceReasonDisconnect)
}

func (s *sessionWS) maybeResetPingTimer() bool {
	// If there's already a reset in progress there's no need to wait.
	if !s.pingTimerCAS.CompareAndSwap(1, 0) {
		return true
	}
	defer s.pingTimerCAS.CompareAndSwap(0, 1)

	s.Lock()
	if s.stopped {
		s.Unlock()
		return false
	}
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
		s.Close("failed to set read deadline", runtime.PresenceReasonDisconnect)
		return false
	}
	return true
}

func (s *sessionWS) processOutgoing() {
	var reason string
	s.Lock()
	ctx := s.ctx
	s.Unlock()
OutgoingLoop:
	for {
		select {
		case <-ctx.Done():
			// Session is closing, close the outgoing process routine.
			break OutgoingLoop
		case <-s.pingTimer.C:
			// Periodically send pings.
			if msg, ok := s.pingNow(); !ok {
				// If ping fails the session will be stopped, clean up the loop.
				reason = msg
				break OutgoingLoop
			}
		case payload := <-s.outgoingCh:
			s.Lock()
			if s.stopped {
				// The connection may have stopped between the payload being queued on the outgoing channel and reaching here.
				// If that's the case then abort outgoing processing at this point and exit.
				s.Unlock()
				break OutgoingLoop
			}
			// Process the outgoing message queue.
			if err := s.conn.SetWriteDeadline(time.Now().Add(s.writeWaitDuration)); err != nil {
				s.Unlock()
				s.logger.Warn("Failed to set write deadline", zap.Error(err))
				reason = err.Error()
				break OutgoingLoop
			}
			if err := s.conn.WriteMessage(s.wsMessageType, payload); err != nil {
				s.Unlock()
				s.logger.Warn("Could not write message", zap.Error(err))
				reason = err.Error()
				break OutgoingLoop
			}
			s.Unlock()

			// Update outgoing message metrics.
			s.metrics.MessageBytesSent(int64(len(payload)))
		}
	}
	s.Close(reason, runtime.PresenceReasonDisconnect)
}

func (s *sessionWS) pingNow() (string, bool) {
	s.Lock()
	if s.stopped {
		s.Unlock()
		return "", false
	}
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

func (s *sessionWS) Format() SessionFormat {
	return s.format
}

var unrequireBytes, _ = evr.Marshal(evr.NewSTcpConnectionUnrequireEvent())

func (s *sessionWS) SendEvrUnrequire(messages ...evr.Message) error {
	err := s.SendEvr(messages...)
	if err != nil {
		return err
	}
	return s.SendBytes(unrequireBytes, true)
}

// SendEvr sends a message to the client in the EchoVR format.
// TODO Transition to using streamsend for all messages.
func (s *sessionWS) SendEvr(messages ...evr.Message) error {

	isDebug := s.logger.Core().Enabled(zap.DebugLevel)
	// Send the EVR messages one at a time.
	var message evr.Message
	for _, message = range messages {
		if message == nil {
			continue
		}
		if isDebug {
			s.logger.Debug(fmt.Sprintf("Sending %T message", message), zap.String("message", fmt.Sprintf("%s", message)))
		}
		// Marshal the message.
		payload, err := evr.Marshal(message)
		if err != nil {
			s.logger.Error("Could not marshal message", zap.Error(err))
			s.Close("could not marshal message", runtime.PresenceReasonDisconnect)
		}
		// Send the message.
		if err := s.SendBytes(payload, true); err != nil {
			s.logger.Error("Could not send message", zap.Error(err))
			s.Close("could not send message", runtime.PresenceReasonDisconnect)
		}
	}

	// If the last message is a STcpConnectionUnrequireEvent, return early.
	if _, ok := message.(*evr.STcpConnectionUnrequireEvent); !ok {
		return nil
	}

	return nil
}

func (s *sessionWS) Send(envelope *rtapi.Envelope, reliable bool) error {
	var payload []byte
	var err error
	switch s.format {
	case SessionFormatEVR:
		messages, err := ProcessOutgoing(s.logger, s, envelope)
		if err != nil {
			return fmt.Errorf("could not process outgoing message: %w", err)
		}
		return s.SendEvr(messages...)
	case SessionFormatProtobuf:
		payload, err = proto.Marshal(envelope)
	case SessionFormatJson:
		fallthrough
	default:
		if buf, err := s.protojsonMarshaler.Marshal(envelope); err == nil {
			payload = buf
		}
	}
	if err != nil {
		s.logger.Warn("Could not marshal envelope", zap.Error(err))
		return err
	}

	if s.logger.Core().Enabled(zap.DebugLevel) {
		switch envelope.Message.(type) {
		case *rtapi.Envelope_Error:
			s.logger.Debug("Sending error message", zap.Binary("payload", payload))
		default:
			s.logger.Debug(fmt.Sprintf("Sending %T message", envelope.Message), zap.Any("envelope", envelope))
		}
	}

	return s.SendBytes(payload, reliable)
}

func (s *sessionWS) SendBytes(payload []byte, reliable bool) error {
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
		go s.Close(ErrSessionQueueFull.Error(), runtime.PresenceReasonDisconnect)
		return ErrSessionQueueFull
	}
}

func (s *sessionWS) CloseLock() {
	s.closeMu.Lock()
}

func (s *sessionWS) CloseUnlock() {
	s.closeMu.Unlock()
}

func (s *sessionWS) Close(msg string, reason runtime.PresenceReason, envelopes ...*rtapi.Envelope) {
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

	isDebug := s.logger.Core().Enabled(zap.DebugLevel)
	if isDebug {
		//s.logger.Debug("Cleaning up closed client connection")
	}

	// When connection close originates internally in the session, ensure cleanup of external resources and references.
	if err := s.matchmaker.RemoveSessionAll(s.id.String()); err != nil {
		s.logger.Warn("Failed to remove all matchmaking tickets", zap.Error(err))
	}
	if isDebug {
		//s.logger.Info("Cleaned up closed connection matchmaker")
	}
	s.tracker.UntrackAll(s.id, reason)
	if isDebug {
		//s.logger.Info("Cleaned up closed connection tracker")
	}
	s.statusRegistry.UnfollowAll(s.id)
	if isDebug {
		//s.logger.Info("Cleaned up closed connection status registry")
	}
	s.sessionRegistry.Remove(s.id)
	if isDebug {
		// s.logger.Info("Cleaned up closed connection session registry")
	}

	// Clean up internals.
	s.pingTimer.Stop()

	for _, envelope := range envelopes {
		var payload []byte
		var err error
		switch s.format {
		case SessionFormatEVR:
			if s.logger.Core().Enabled(zap.DebugLevel) {
				s.logger.Warn("Blocked attempt to send protobuf message to EVR client.", zap.Any("envelope", envelope))
			}
			continue
		case SessionFormatProtobuf:
			payload, err = proto.Marshal(envelope)
		case SessionFormatJson:
			fallthrough
		default:
			if buf, err := s.protojsonMarshaler.Marshal(envelope); err == nil {
				payload = buf
			}
		}
		if err != nil {
			s.logger.Warn("Could not marshal envelope", zap.Error(err))
			continue
		}

		if isDebug {
			switch envelope.Message.(type) {
			case *rtapi.Envelope_Error:
				s.logger.Debug("Sending error message", zap.Binary("payload", payload))
			default:
				s.logger.Debug(fmt.Sprintf("Sending %T message", envelope.Message), zap.Any("envelope", envelope))
			}
		}

		s.Lock()
		if err := s.conn.SetWriteDeadline(time.Now().Add(s.writeWaitDuration)); err != nil {
			s.Unlock()
			s.logger.Warn("Failed to set write deadline", zap.Error(err))
			continue
		}
		if err := s.conn.WriteMessage(s.wsMessageType, payload); err != nil {
			s.Unlock()
			s.logger.Warn("Could not write message", zap.Error(err))
			continue
		}
		s.Unlock()
	}

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

	// Fire an event for session end.
	if fn := s.runtime.EventSessionEnd(); fn != nil {
		fn(s.userID.String(), s.username.Load(), s.vars, s.expiry, s.id.String(), s.clientIP, s.clientPort, s.lang, time.Now().UTC().Unix(), msg)
	}
}
