package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"go.uber.org/atomic"
	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/bwmarrin/discordgo"
	"github.com/echotools/nevr-common/v3/rtapi"
	"github.com/go-redis/redis"
	"github.com/gofrs/uuid/v5"

	evr "github.com/echotools/nakama/v3/protocol"
	nkrtapi "github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server"
	"github.com/heroiclabs/nakama/v3/social"
)

var dg *discordgo.Session

var unrequireMessage = evr.NewSTcpConnectionUnrequireEvent()

var globalMatchmaker = atomic.NewPointer[server.LocalMatchmaker](nil)
var globalAppBot = atomic.NewPointer[DiscordAppBot](nil)

type EvrPipeline struct {
	sync.RWMutex
	ctx context.Context

	node              string
	broadcasterUserID string // The userID used for broadcaster connections
	internalIP        net.IP
	externalIP        net.IP // Server's external IP for external connections

	logger *zap.Logger
	db     *sql.DB

	config          server.Config
	matchRegistry   server.MatchRegistry
	sessionRegistry server.SessionRegistry
	sessionCache    server.SessionCache
	statusRegistry  server.StatusRegistry
	partyRegistry   server.PartyRegistry
	tracker         server.Tracker
	matchmaker      server.Matchmaker
	router          server.MessageRouter
	streamManager   server.StreamManager
	metrics         server.Metrics

	version string

	runtime       *server.Runtime
	nk            *server.RuntimeGoNakamaModule
	runtimeLogger runtime.Logger

	profileCache *ProfileCache
	discordCache *DiscordIntegrator
	appBot       *DiscordAppBot

	userRemoteLogJournalRegistry *UserLogJouralRegistry
	guildGroupRegistry           *GuildGroupRegistry
	ipInfoCache                  *IPInfoCache

	placeholderEmail string
	linkDeviceURL    string

	messageCache *server.MapOf[string, evr.Message]
}

type ctxDiscordBotTokenKey struct{}

func NewEvrPipeline(logger *zap.Logger, startupLogger *zap.Logger, db *sql.DB, protojsonMarshaler *protojson.MarshalOptions, protojsonUnmarshaler *protojson.UnmarshalOptions, config server.Config, version string, socialClient *social.Client, storageIndex server.StorageIndex, leaderboardScheduler server.LeaderboardScheduler, leaderboardCache server.LeaderboardCache, leaderboardRankCache server.LeaderboardRankCache, sessionRegistry server.SessionRegistry, sessionCache server.SessionCache, statusRegistry server.StatusRegistry, matchRegistry server.MatchRegistry, partyRegistry server.PartyRegistry, matchmaker server.Matchmaker, tracker server.Tracker, router server.MessageRouter, streamManager server.StreamManager, metrics server.Metrics, pipeline *server.Pipeline, _runtime *server.Runtime) *EvrPipeline {
	globalMatchmaker.Store(matchmaker.(*server.LocalMatchmaker))

	nk := server.NewRuntimeGoNakamaModule(logger, db, protojsonMarshaler, config, socialClient, leaderboardCache, leaderboardRankCache, leaderboardScheduler, sessionRegistry, sessionCache, statusRegistry, matchRegistry, partyRegistry, tracker, metrics, streamManager, router, storageIndex, nil)

	// Add the bot token to the context
	vars := config.GetRuntime().Environment
	ctx := context.WithValue(context.Background(), ctxDiscordBotTokenKey{}, vars["DISCORD_BOT_TOKEN"])
	ctx = context.WithValue(ctx, runtime.RUNTIME_CTX_ENV, vars)

	// TODO: move this to a Start() function
	// Load the global settings
	if _, err := ServiceSettingsLoad(ctx, nk); err != nil {
		logger.Fatal("Failed to load global settings", zap.Error(err))
	}

	// TODO: move this to a Start() function
	go func() {

		interval := 30 * time.Second
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				ticker.Stop()
				return
			case <-ticker.C:
				if _, err := ServiceSettingsLoad(ctx, nk); err != nil {
					logger.Error("Failed to load global settings", zap.Error(err))
				}
			}
		}
	}()

	botToken, ok := ctx.Value(ctxDiscordBotTokenKey{}).(string)
	if !ok {
		panic("Bot token is not set in context.")
	}

	var err error
	if botToken != "" {
		dg, err = discordgo.New("Bot " + botToken)
		if err != nil {
			logger.Error("Unable to create bot")
		}
		dg.StateEnabled = true
	}

	runtimeLogger := server.NewRuntimeGoLogger(logger)

	guildGroupRegistry := NewGuildGroupRegistry(ctx, runtimeLogger, nk, db)

	// TODO: Move component creation to main.go
	profileRegistry := NewProfileRegistry(nk, db, runtimeLogger, metrics, sessionRegistry)
	lobbyBuilder := NewLobbyBuilder(logger, nk, sessionRegistry, matchRegistry, tracker, metrics)
	matchmaker.OnMatchedEntries(lobbyBuilder.handleMatchedEntries)
	userRemoteLogJournalRegistry := NewUserRemoteLogJournalRegistry(ctx, logger, nk, sessionRegistry)

	// TODO: Move component creation to main.go
	// Connect to the redis server
	var redisClient *redis.Client
	if vars["REDIS_URI"] != "" {
		redisClient, err = connectRedis(ctx, vars["REDIS_URI"])
		if err != nil {
			logger.Fatal("Failed to connect to Redis", zap.Error(err))
		}
	}

	// TODO: Move component creation to main.go
	var ipInfoCache *IPInfoCache
	if redisClient != nil {
		var providers []IPInfoProvider
		if vars["IPAPI_API_KEY"] != "" {
			ipqsClient, err := NewIPQSClient(logger, metrics, redisClient, vars["IPQS_API_KEY"])
			if err != nil {
				logger.Fatal("Failed to create IPQS client", zap.Error(err))
			}
			providers = append(providers, ipqsClient)
		}

		ipapiClient, err := NewIPAPIClient(logger, metrics, redisClient)
		if err != nil {
			logger.Fatal("Failed to create IPAPI client", zap.Error(err))
		}
		providers = append(providers, ipapiClient)

		ipInfoCache, err = NewIPInfoCache(logger, metrics, providers...)
		if err != nil {
			logger.Fatal("Failed to create IP info cache", zap.Error(err))
		}
	}

	var appBot *DiscordAppBot
	var discordIntegrator *DiscordIntegrator

	// TODO: Move component creation to main.go
	discordIntegrator = NewDiscordIntegrator(ctx, logger, config, metrics, nk, db, dg, guildGroupRegistry)
	discordIntegrator.Start()

	// TODO: Move component creation to main.go
	appBot, err = NewDiscordAppBot(ctx, runtimeLogger, nk, db, metrics, config, discordIntegrator, profileRegistry, statusRegistry, sessionRegistry, dg, ipInfoCache, guildGroupRegistry)
	if err != nil {
		logger.Error("Failed to create app bot", zap.Error(err))

	}
	// Store the app bot in a global variable for later use
	globalAppBot.Store(appBot) // TODO: This is a temporary solution, we should refactor this to avoid global state

	// Add a once handler to wait for the bot to connect
	readyCh := make(chan struct{})
	dg.AddHandlerOnce(func(s *discordgo.Session, r *discordgo.Ready) {
		close(readyCh)
	})

	if err = dg.Open(); err != nil {
		logger.Fatal("Failed to open discord bot connection: %w", zap.Error(err))
	}

	select {
	case <-readyCh:
		logger.Info("Discord bot is ready")
	case <-time.After(10 * time.Second):
		logger.Fatal("Discord bot is not ready after 10 seconds")
	}

	// TODO: Move this to a Start() function
	internalIP, externalIP, err := DetermineServiceIPs(ctx)
	if err != nil {
		logger.Fatal("Unable to determine service IPs", zap.Error(err))
	}

	evrPipeline := &EvrPipeline{
		ctx:     ctx,
		node:    config.GetName(),
		logger:  logger,
		db:      db,
		config:  config,
		version: version,

		partyRegistry:   partyRegistry,
		sessionRegistry: sessionRegistry,
		runtime:         _runtime,
		nk:              nk,

		runtimeLogger: runtimeLogger,

		discordCache: discordIntegrator,
		appBot:       appBot,
		internalIP:   internalIP,
		externalIP:   externalIP,

		profileCache:                 profileRegistry,
		userRemoteLogJournalRegistry: userRemoteLogJournalRegistry,
		guildGroupRegistry:           guildGroupRegistry,
		ipInfoCache:                  ipInfoCache,

		placeholderEmail: config.GetRuntime().Environment["PLACEHOLDER_EMAIL_DOMAIN"],
		linkDeviceURL:    config.GetRuntime().Environment["LINK_DEVICE_URL"],

		messageCache: &server.MapOf[string, evr.Message]{},
	}

	return evrPipeline
}

func DetermineServiceIPs(ctx context.Context) (intIP net.IP, extIP net.IP, err error) {

	if vars, ok := ctx.Value(runtime.RUNTIME_CTX_ENV).(map[string]string); ok {
		// Check if the internal IP is set in the environment
		if ip := vars["IP_ADDRESS_INTERNAL"]; ip != "" {
			intIP = net.ParseIP(ip)
			if intIP == nil {
				return nil, nil, fmt.Errorf("invalid internal IP from environment: %s", ip)
			}
		}

		if ip := vars["IP_ADDRESS_EXTERNAL"]; ip != "" {
			extIP = net.ParseIP(ip)
			if extIP == nil {
				return nil, nil, fmt.Errorf("invalid external IP from environment: %s", ip)
			}
		}
	}

	if intIP == nil {
		intIP, err = DetermineLocalIPAddress()
		if err != nil {
			return nil, nil, fmt.Errorf("unable to determine internal IP: %w", err)
		}
	}

	if extIP == nil {
		extIP, err = DetermineExternalIPAddress()
		if err != nil {
			return nil, nil, fmt.Errorf("unable to determine external IP: %w", err)
		}
	}

	return intIP, extIP, nil
}

func (p *EvrPipeline) Stop() {}

func (p *EvrPipeline) MessageCacheStore(key string, message evr.Message, ttl time.Duration) {
	p.messageCache.Store(key, message)
	time.AfterFunc(ttl, func() {
		p.messageCache.Delete(key)
	})
}

func (p *EvrPipeline) MessageCacheLoad(key string) evr.Message {
	if message, ok := p.messageCache.Load(key); ok {
		return message
	}
	return nil
}

func (p *EvrPipeline) ProcessRequestEVR(logger *zap.Logger, session server.Session, in evr.Message) bool {

	// Handle legacy messages

	var envelope *rtapi.Envelope

	switch msg := in.(type) {
	// EVR Encapsulated Protobuf message (NEVR protocol)

	case *evr.NEVRProtobufJSONMessageV1:
		envelope = &rtapi.Envelope{}
		err := protojson.Unmarshal(msg.Payload, envelope)
		if err != nil {
			logger.Error("Failed to unmarshal protobuf message", zap.String("format", "json"), zap.Error(err), zap.String("data", string(msg.Payload)))
			return false
		}

	case *evr.NEVRProtobufMessageV1:
		envelope = &rtapi.Envelope{}
		err := proto.Unmarshal(msg.Payload, envelope)
		if err != nil {
			logger.Error("Failed to unmarshal protobuf message", zap.String("format", "binary"), zap.Error(err), zap.Binary("data", msg.Payload))
			return false
		}

	}

	if envelope == nil {
		// TODO: Extract this to a separate file/function
		// Handle legacy messages that are not encapsulated in NEVR protobuf messages
		switch msg := in.(type) {
		case *evr.BroadcasterRegistrationRequest: // Legacy message
			envelope = &rtapi.Envelope{
				Message: &rtapi.Envelope_GameServerRegistration{
					GameServerRegistration: &rtapi.GameServerRegistrationMessage{
						ServerId:          msg.ServerID,
						InternalIpAddress: msg.InternalIP.String(),
						Port:              uint32(msg.Port),
						Region:            uint64(msg.Region),
						VersionLock:       uint64(msg.VersionLock),
					},
				},
			}
		case *evr.GameServerJoinAttempt: // Legacy message
			matchID, _, err := GameServerBySessionID(p.nk, session.ID())
			if err != nil {
				logger.Error("Failed to get broadcaster's match by session ID", zap.Error(err))
				return true
			}
			entrantIDStrs := make([]string, 0, len(msg.EntrantIDs))
			for _, id := range msg.EntrantIDs {
				entrantIDStrs = append(entrantIDStrs, id.String())
			}
			envelope = &rtapi.Envelope{
				Message: &rtapi.Envelope_LobbyEntrantConnected{
					LobbyEntrantConnected: &rtapi.LobbyEntrantsConnectedMessage{
						LobbySessionId: matchID.UUID.String(),
						EntrantIds:     entrantIDStrs,
					},
				},
			}
		case *evr.GameServerPlayerRemoved: // Legacy message
			matchID, _, err := GameServerBySessionID(p.nk, session.ID())
			if err != nil {
				logger.Error("Failed to get broadcaster's match by session ID", zap.Error(err))
				return true
			}
			envelope = &rtapi.Envelope{
				Message: &rtapi.Envelope_LobbyEntrantRemoved{
					LobbyEntrantRemoved: &rtapi.LobbyEntrantRemovedMessage{
						LobbySessionId: matchID.UUID.String(),
						EntrantId:      msg.EntrantID.String(),
					},
				},
			}
		case *evr.BroadcasterPlayerSessionsLocked: // Legacy message
			matchID, _, err := GameServerBySessionID(p.nk, session.ID())
			if err != nil {
				logger.Error("Failed to get broadcaster's match by session ID", zap.Error(err))
				return true
			}
			envelope = &rtapi.Envelope{
				Message: &rtapi.Envelope_LobbySessionEvent{
					LobbySessionEvent: &rtapi.LobbySessionEventMessage{
						LobbySessionId: matchID.UUID.String(),
						Code:           int32(rtapi.LobbySessionEventMessage_LOCKED),
					},
				},
			}
		case *evr.BroadcasterPlayerSessionsUnlocked: // Legacy message
			matchID, _, err := GameServerBySessionID(p.nk, session.ID())
			if err != nil {
				logger.Error("Failed to get broadcaster's match by session ID", zap.Error(err))
				return true
			}
			envelope = &rtapi.Envelope{
				Message: &rtapi.Envelope_LobbySessionEvent{
					LobbySessionEvent: &rtapi.LobbySessionEventMessage{
						LobbySessionId: matchID.UUID.String(),
						Code:           int32(rtapi.LobbySessionEventMessage_UNLOCKED),
					},
				},
			}
		case *evr.BroadcasterSessionEnded: // Legacy message
			matchID, _, err := GameServerBySessionID(p.nk, session.ID())
			if err != nil {
				logger.Error("Failed to get broadcaster's match by session ID", zap.Error(err))
				return true
			}
			envelope = &rtapi.Envelope{
				Message: &rtapi.Envelope_LobbySessionEvent{
					LobbySessionEvent: &rtapi.LobbySessionEventMessage{
						LobbySessionId: matchID.UUID.String(),
						Code:           int32(rtapi.LobbySessionEventMessage_ENDED),
					},
				},
			}
		}
	}
	if envelope != nil {
		// This is a legacy message that has been converted to a protobuf message
		if err := p.ProcessProtobufRequest(logger, session, envelope); err != nil {
			logger.Error("Failed to process protobuf message", zap.Any("envelope", envelope), zap.Error(err))
			return false
		}
		return true
	}

	var pipelineFn func(ctx context.Context, logger *zap.Logger, session *sessionEVR, in evr.Message) error

	isAuthenticationRequired := true

	switch in.(type) {

	// Config service
	case *evr.ConfigRequest:
		isAuthenticationRequired = false
		pipelineFn = p.configRequest

	// Transaction (IAP) service (unused)
	case *evr.ReconcileIAP:
		isAuthenticationRequired = false
		pipelineFn = p.reconcileIAP

	// Login/Profile Service
	case *evr.RemoteLogSet:
		isAuthenticationRequired = false
		pipelineFn = p.remoteLogSetv3
	case *evr.LoginRequest:
		isAuthenticationRequired = false
		pipelineFn = p.loginRequest
	case *evr.DocumentRequest:
		pipelineFn = p.documentRequest
	case *evr.LoggedInUserProfileRequest:
		pipelineFn = p.loggedInUserProfileRequest
	case *evr.ChannelInfoRequest:
		pipelineFn = p.channelInfoRequest
	case *evr.UpdateClientProfile:
		pipelineFn = p.updateClientProfileRequest
	case *evr.OtherUserProfileRequest:
		pipelineFn = p.otherUserProfileRequest
	case *evr.UserServerProfileUpdateRequest: // Broadcaster only via it's login connection
		pipelineFn = p.userServerProfileUpdateRequest
	case *evr.GenericMessage:
		pipelineFn = p.genericMessage

	// server.Matchmaker service
	// server.Matchmaker service
	case *evr.LobbyFindSessionRequest:
		pipelineFn = p.lobbySessionRequest
	case *evr.LobbyCreateSessionRequest:
		pipelineFn = p.lobbySessionRequest
	case *evr.LobbyJoinSessionRequest:
		pipelineFn = p.lobbySessionRequest
	case *evr.LobbyMatchmakerStatusRequest:
		pipelineFn = p.lobbyMatchmakerStatusRequest
	case *evr.LobbyPingResponse:
		pipelineFn = p.lobbyPingResponse
	case *evr.LobbyPlayerSessionsRequest:
		pipelineFn = p.lobbyPlayerSessionsRequest
	case *evr.LobbyPendingSessionCancel:
		pipelineFn = p.lobbyPendingSessionCancel

	default:
		pipelineFn = func(ctx context.Context, logger *zap.Logger, session *sessionEVR, in evr.Message) error {
			logger.Warn("Received unhandled message", zap.Any("message", in))
			return nil
		}
	}

	if isAuthenticationRequired && session.UserID().IsNil() {

		// set/validate the login session
		if idmessage, ok := in.(evr.LoginIdentifier); ok {

			if idmessage.GetLoginSessionID().IsNil() {
				logger.Error("Login session ID is nil")
				return false
			}

			params, ok := LoadParams(session.Context())
			if !ok {
				logger.Error("Failed to get lobby parameters")
				return false
			}

			loginSession := params.loginSession
			if loginSession == nil {
				switch idmessage.(type) {
				case evr.LobbySessionRequest:
					// associate lobby session with login session
					// If the message is an identifying message, validate the session and evr id.
					if err := LobbySession(session.(*sessionEVR), p.sessionRegistry, idmessage.GetLoginSessionID()); err != nil {
						logger.Error("Invalid session", zap.Error(err))
						// Disconnect the client if the session is invalid.
						return false
					}
				default:
					logger.Error("Login session not found", zap.String("login_session_id", idmessage.GetLoginSessionID().String()))
					return false
				}
			} else if !loginSession.id.IsNil() && loginSession.id != idmessage.GetLoginSessionID() {
				// If the ID message is not associated with the current session, log the error and return.
				logger.Error("mismatched login session id", zap.String("login_session_id", idmessage.GetLoginSessionID().String()), zap.String("login_session_id", loginSession.id.String()))
				return false
			}

		}

		// Set/validate the XPI
		if xpimessage, ok := in.(evr.XPIdentifier); ok {

			params, ok := LoadParams(session.Context())
			if !ok {
				logger.Error("Failed to get lobby parameters")
				return false
			}

			if !params.xpID.Equals(xpimessage.GetEvrID()) {
				logger.Error("mismatched evr id", zap.String("evrid", xpimessage.GetEvrID().String()), zap.String("evrid2", params.xpID.String()))
				return false
			}
		}

		// If the session is not authenticated, log the error and return.
		if session != nil && session.UserID() == uuid.Nil {

			logger.Warn("Received unauthenticated message", zap.Any("message", in))

			// Send an unrequire
			if err := SendEVRMessages(session, false, unrequireMessage); err != nil {
				logger.Error("Failed to send unrequire message", zap.Error(err))
				return false
			}

			return true
		}
	}

	if params, ok := LoadParams(session.Context()); ok && !params.xpID.IsNil() {
		logger = logger.With(zap.String("uid", session.UserID().String()), zap.String("sid", session.ID().String()), zap.String("username", session.Username()), zap.String("evrid", params.xpID.String()))
	}

	if err := pipelineFn(session.Context(), logger, session.(*sessionEVR), in); err != nil {
		// Unwrap the error
		logger.Error("server.Pipeline error", zap.Error(err))
		logger.Error("server.Pipeline error", zap.Error(err))
		// TODO: Handle errors and close the connection
	}
	// Keep the connection open, otherwise the client will display "service unavailable"
	return true
}

// Process outgoing protobuf envelopes and translate them to Evr messages
func ProcessOutgoing(logger *zap.Logger, session *sessionEVR, in *nkrtapi.Envelope) ([]evr.Message, error) {
	p := session.evrPipeline

	switch in.Message.(type) {

	case *nkrtapi.Envelope_StreamData:
		// EVR binary protocol data
		payload := []byte(in.GetStreamData().GetData())
		if bytes.HasPrefix(payload, evr.MessageMarker) {
			return nil, session.SendBytes(payload, true)
		}

	case *nkrtapi.Envelope_MatchData:
		// EVR binary protocol data
		if in.GetMatchData().GetOpCode() == OpCodeEVRPacketData {
			return nil, session.SendBytes(in.GetMatchData().GetData(), true)
		}
	}

	if err := p.updatePartyLeaderFromMessage(session, in); err != nil {
		logger.Error("Failed to update party leader from message", zap.Error(err))
		return nil, err
	}
	params, ok := LoadParams(session.Context())
	if !ok {
		logger.Error("Failed to get lobby parameters")
		return nil, nil
	}

	switch in.Message.(type) {
	case *nkrtapi.Envelope_Party, *nkrtapi.Envelope_PartyCreate, *nkrtapi.Envelope_PartyJoin,
		*nkrtapi.Envelope_PartyLeave, *nkrtapi.Envelope_PartyPromote, *nkrtapi.Envelope_PartyLeader,
		*nkrtapi.Envelope_PartyAccept, *nkrtapi.Envelope_PartyRemove, *nkrtapi.Envelope_PartyClose,
		*nkrtapi.Envelope_PartyJoinRequestList, *nkrtapi.Envelope_PartyJoinRequest,
		*nkrtapi.Envelope_PartyMatchmakerAdd, *nkrtapi.Envelope_PartyMatchmakerRemove,
		*nkrtapi.Envelope_PartyMatchmakerTicket, *nkrtapi.Envelope_PartyData, *nkrtapi.Envelope_PartyDataSend,
		*nkrtapi.Envelope_PartyPresenceEvent, *nkrtapi.Envelope_PartyUpdate:
		return nil, p.processPartyMessage(logger, session, in)
	}

	// DM the user on discord
	if !strings.HasPrefix(session.Username(), "broadcaster:") && params.relayOutgoing {
		content := ""
		switch msg := in.Message.(type) {
		case *nkrtapi.Envelope_StreamData:

			discordMessage := struct {
				Stream  *nkrtapi.Stream       `json:"stream"`
				Sender  *nkrtapi.UserPresence `json:"sender"`
				Content json.RawMessage       `json:"content"`
			}{
				msg.StreamData.Stream,
				msg.StreamData.Sender,
				json.RawMessage(msg.StreamData.Data),
			}

			if msg.StreamData.Stream.Mode == int32(server.SessionFormatJson) {
				data, err := json.MarshalIndent(discordMessage, "", "  ")
				if err != nil {
					logger.Error("Failed to marshal stream data", zap.Error(err))
				} else {
					content = fmt.Sprintf("```json\n%s\n```", data)
				}
			}

		case *nkrtapi.Envelope_Error:

			// Json the message
			data, err := json.MarshalIndent(in.GetError(), "", "  ")
			if err != nil {
				logger.Error("Failed to marshal error", zap.Error(err))
			} else {
				content = string("```json\n" + string(data) + "\n```")
			}

		case *nkrtapi.Envelope_StatusPresenceEvent, *nkrtapi.Envelope_MatchPresenceEvent, *nkrtapi.Envelope_StreamPresenceEvent:

			// Json the message
			data, _ := json.MarshalIndent(in.GetMessage(), "", "  ")
			content = string("```json\n" + string(data) + "\n```")

		default:
			if data, err := json.MarshalIndent(in.GetMessage(), "", "  "); err != nil {
				logger.Error("Failed to marshal message", zap.Error(err))
			} else if len(data) > 2000 {
				content = "Message too long to display"
			} else if len(data) > 0 {
				content = string("```json\n" + string(data) + "\n```")
			}
		}

		if content != "" {
			if dg := p.discordCache.dg; dg == nil {
				// No discord bot
			} else if discordID, err := GetDiscordIDByUserID(session.Context(), p.db, session.UserID().String()); err != nil {
				logger.Warn("Failed to get discord ID", zap.Error(err))
			} else if channel, err := dg.UserChannelCreate(discordID); err != nil {
				logger.Warn("Failed to create DM channel", zap.Error(err))
			} else {

				// Limit the entire size of the message to 4k bytes
				if len(content) > 4000 {
					content = content[:4000]
				}

				// If the message is over 1800 bytes, then send it in chunks. just split it into 1800 byte chunks
				if len(content) > 1800 {
					for i := 0; i < len(content); i += 1800 {
						max := min(i+1800, len(content))
						if _, err = dg.ChannelMessageSend(channel.ID, content[i:max]); err != nil {
							logger.Warn("Failed to send message to user", zap.Error(err))
						}
					}
				} else {
					if _, err = dg.ChannelMessageSend(channel.ID, content); err != nil {
						logger.Warn("Failed to send message to user", zap.Error(err))
					}
				}
			}
		}
	}
	return nil, nil
}

func (p *EvrPipeline) ProcessProtobufRequest(logger *zap.Logger, session server.Session, in *rtapi.Envelope) error {
	if in.Message == nil {
		return fmt.Errorf("received empty protobuf message")
	}

	var pipelineFn func(logger *zap.Logger, session *sessionEVR, in *rtapi.Envelope) error

	// Process the request based on its type
	switch in.Message.(type) {
	case *rtapi.Envelope_GameServerRegistration:
		pipelineFn = p.gameserverRegistrationRequest
	case *rtapi.Envelope_LobbyEntrantConnected:
		pipelineFn = p.lobbyEntrantConnected
	case *rtapi.Envelope_LobbySessionEvent:
		pipelineFn = p.lobbySessionEvent
	case *rtapi.Envelope_LobbyEntrantRemoved:
		pipelineFn = p.lobbyEntrantRemoved
	default:
		// For all other messages, use the generic protobuf handler
		return fmt.Errorf("unhandled protobuf message type: %T", in.Message)
	}

	if err := pipelineFn(logger, session.(*sessionEVR), in); err != nil {
		logger.Error("Failed to process protobuf message", zap.Any("envelope", in), zap.Error(err))
		return err
	}
	return nil
}
