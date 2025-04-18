package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/go-redis/redis"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/heroiclabs/nakama/v3/social"
	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protojson"

	"net/http"
	_ "net/http/pprof"
	// Import for side effects to enable pprof endpoint
)

var dg *discordgo.Session

var unrequireMessage = evr.NewSTcpConnectionUnrequireEvent()

var globalMatchmaker = &atomic.Pointer[LocalMatchmaker]{}

type EvrPipeline struct {
	sync.RWMutex
	ctx context.Context

	node              string
	broadcasterUserID string // The userID used for broadcaster connections
	internalIP        net.IP
	externalIP        net.IP // Server's external IP for external connections

	logger  *zap.Logger
	db      *sql.DB
	config  Config
	version string

	sessionRegistry SessionRegistry
	runtime         *Runtime
	nk              *RuntimeGoNakamaModule
	runtimeLogger   runtime.Logger

	profileCache                 *ProfileCache
	discordCache                 *DiscordIntegrator
	appBot                       *DiscordAppBot
	statisticsQueue              *StatisticsQueue
	userRemoteLogJournalRegistry *UserLogJouralRegistry
	guildGroupRegistry           *GuildGroupRegistry
	ipInfoCache                  *IPInfoCache

	placeholderEmail string
	linkDeviceURL    string

	messageCache *MapOf[string, evr.Message]
}

type ctxDiscordBotTokenKey struct{}

func NewEvrPipeline(logger *zap.Logger, startupLogger *zap.Logger, db *sql.DB, protojsonMarshaler *protojson.MarshalOptions, protojsonUnmarshaler *protojson.UnmarshalOptions, config Config, version string, socialClient *social.Client, storageIndex StorageIndex, leaderboardScheduler LeaderboardScheduler, leaderboardCache LeaderboardCache, leaderboardRankCache LeaderboardRankCache, sessionRegistry SessionRegistry, sessionCache SessionCache, statusRegistry StatusRegistry, matchRegistry MatchRegistry, matchmaker Matchmaker, tracker Tracker, router MessageRouter, streamManager StreamManager, metrics Metrics, pipeline *Pipeline, _runtime *Runtime) *EvrPipeline {
	go func() {
		if err := http.ListenAndServe("0.0.0.0:6060", nil); err != nil {
			startupLogger.Fatal("Failed to start pprof server", zap.Error(err))
		}
	}()

	nk := _runtime.nk
	globalMatchmaker.Store(matchmaker.(*LocalMatchmaker))

	// Add the bot token to the context
	vars := config.GetRuntime().Environment
	ctx := context.WithValue(context.Background(), ctxDiscordBotTokenKey{}, vars["DISCORD_BOT_TOKEN"])
	ctx = context.WithValue(ctx, runtime.RUNTIME_CTX_ENV, vars)

	// Load the global settings
	if _, err := ServiceSettingsLoad(ctx, nk); err != nil {
		logger.Fatal("Failed to load global settings", zap.Error(err))
	}

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

	runtimeLogger := NewRuntimeGoLogger(logger)

	statisticsQueue := NewStatisticsQueue(runtimeLogger, nk)
	profileRegistry := NewProfileRegistry(nk, db, runtimeLogger, metrics, sessionRegistry)
	guildGroupRegistry := NewGuildGroupRegistry(ctx, runtimeLogger, nk, db)
	lobbyBuilder := NewLobbyBuilder(logger, nk, sessionRegistry, matchRegistry, tracker, metrics)
	matchmaker.OnMatchedEntries(lobbyBuilder.handleMatchedEntries)
	userRemoteLogJournalRegistry := NewUserRemoteLogJournalRegistry(ctx, logger, nk, sessionRegistry)

	var redisClient *redis.Client

	// Connect to the redis server
	redisUri := vars["REDIS_URI"]
	if redisUri != "" {
		redisOptions, err := redis.ParseURL(redisUri)
		if err != nil {
			logger.Fatal("Failed to parse Redis URI", zap.Error(err))
		}

		redisClient = redis.NewClient(redisOptions)
		_, err = redisClient.Ping().Result()
		if err != nil {
			logger.Fatal("Failed to connect to Redis", zap.Error(err))
		}

		logger.Info("Connected to Redis", zap.String("addr", redisOptions.Addr))
	}

	ipqsClient, err := NewIPQSClient(logger, metrics, redisClient, vars["IPQS_API_KEY"])
	if err != nil {
		logger.Fatal("Failed to create IPQS client", zap.Error(err))
	}

	ipapiClient, err := NewIPAPIClient(logger, metrics, redisClient)
	if err != nil {
		logger.Fatal("Failed to create IPAPI client", zap.Error(err))
	}

	ipInfoCache, err := NewIPInfoCache(logger, metrics, ipapiClient, ipqsClient)
	if err != nil {
		logger.Fatal("Failed to create IP info cache", zap.Error(err))
	}

	var appBot *DiscordAppBot
	var discordCache *DiscordIntegrator
	if disable, ok := vars["DISABLE_DISCORD_BOT"]; ok && disable == "true" {
		logger.Info("Discord bot is disabled")
	} else {
		discordCache = NewDiscordIntegrator(ctx, logger, config, metrics, nk, db, dg, guildGroupRegistry)
		discordCache.Start()

		appBot, err = NewDiscordAppBot(ctx, runtimeLogger, nk, db, metrics, pipeline, config, discordCache, profileRegistry, statusRegistry, dg, ipInfoCache, guildGroupRegistry)
		if err != nil {
			logger.Error("Failed to create app bot", zap.Error(err))

		}

		// Add a once handler to wait for the bot to connect
		readyCh := make(chan struct{})
		dg.AddHandlerOnce(func(s *discordgo.Session, r *discordgo.Ready) {
			close(readyCh)
		})

		if err = dg.Open(); err != nil {
			logger.Warn("Failed to open discord bot connection: %w", zap.Error(err))
		}

		select {
		case <-readyCh:
			logger.Info("Discord bot is ready")
		case <-time.After(10 * time.Second):
			logger.Warn("Discord bot is not ready after 10 seconds")
		}
	}

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

		sessionRegistry: sessionRegistry,
		runtime:         _runtime,
		nk:              nk.(*RuntimeGoNakamaModule),
		runtimeLogger:   runtimeLogger,

		discordCache: discordCache,
		appBot:       appBot,
		internalIP:   internalIP,
		externalIP:   externalIP,

		profileCache:                 profileRegistry,
		statisticsQueue:              statisticsQueue,
		userRemoteLogJournalRegistry: userRemoteLogJournalRegistry,
		guildGroupRegistry:           guildGroupRegistry,
		ipInfoCache:                  ipInfoCache,

		placeholderEmail: config.GetRuntime().Environment["PLACEHOLDER_EMAIL_DOMAIN"],
		linkDeviceURL:    config.GetRuntime().Environment["LINK_DEVICE_URL"],

		messageCache: &MapOf[string, evr.Message]{},
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

func (p *EvrPipeline) ProcessRequestEVR(logger *zap.Logger, session Session, in evr.Message) bool {

	// Handle legacy messages

	switch msg := in.(type) {

	case *evr.BroadcasterRegistrationRequest:

		in = &evr.EchoToolsGameServerRegistrationRequestV1{
			LoginSessionID: uuid.Nil,
			ServerID:       msg.ServerId,
			InternalIP:     msg.InternalIP,
			Port:           msg.Port,
			RegionHash:     msg.Region,
			VersionLock:    msg.VersionLock,
			TimeStepUsecs:  0,
		}

	case *evr.BroadcasterPlayerSessionsLocked:
		matchID, _, err := GameServerBySessionID(p.nk, session.ID())
		if err != nil {
			logger.Error("Failed to get broadcaster's match by session ID", zap.Error(err))
			return true
		}
		in = &evr.EchoToolsLobbySessionLockV1{LobbySessionID: matchID.UUID}

	case *evr.BroadcasterPlayerSessionsUnlocked:
		matchID, _, err := GameServerBySessionID(p.nk, session.ID())
		if err != nil {
			logger.Error("Failed to get broadcaster's match by session ID", zap.Error(err))
			return true
		}
		in = &evr.EchoToolsLobbySessionUnlockV1{LobbySessionID: matchID.UUID}

	case *evr.GameServerJoinAttempt:
		matchID, _, err := GameServerBySessionID(p.nk, session.ID())
		if err != nil {
			logger.Error("Failed to get broadcaster's match by session ID", zap.Error(err))
			return true
		}
		in = &evr.EchoToolsLobbyEntrantNewV1{LobbySessionID: matchID.UUID, EntrantIDs: msg.EntrantIDs}

	case *evr.GameServerPlayerRemoved:
		matchID, _, err := GameServerBySessionID(p.nk, session.ID())
		if err != nil {
			logger.Error("Failed to get broadcaster's match by session ID", zap.Error(err))
			return true
		}

		in = &evr.EchoToolsLobbyEntrantRemovedV1{EntrantID: msg.EntrantID, LobbySessionID: matchID.UUID}

	case *evr.BroadcasterSessionStarted:
		matchID, _, err := GameServerBySessionID(p.nk, session.ID())
		if err != nil {
			logger.Error("Failed to get broadcaster's match by session ID", zap.Error(err))
			return true
		}
		in = &evr.EchoToolsLobbySessionStartedV1{LobbySessionID: matchID.UUID}

	case *evr.BroadcasterSessionEnded:
		matchID, _, err := GameServerBySessionID(p.nk, session.ID())
		if err != nil {
			logger.Error("Failed to get broadcaster's match by session ID", zap.Error(err))
			return true
		}
		in = &evr.EchoToolsLobbySessionEndedV1{LobbySessionID: matchID.UUID}

	}

	var pipelineFn func(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error

	isAuthenticationRequired := true

	switch in.(type) {

	// Config service
	case *evr.ConfigRequest:
		isAuthenticationRequired = false
		pipelineFn = p.configRequest

	// Transaction (IAP) service
	case *evr.ReconcileIAP:
		isAuthenticationRequired = false
		pipelineFn = p.reconcileIAP

	// Login Service
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

	// Match service
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

	// ServerDB service
	case *evr.EchoToolsGameServerRegistrationRequestV1:
		isAuthenticationRequired = false
		pipelineFn = p.gameserverRegistrationRequest
	case *evr.EchoToolsLobbySessionStartedV1:
		pipelineFn = p.gameserverLobbySessionStarted
	case *evr.EchoToolsLobbyStatusV1:
		pipelineFn = p.gameserverLobbySessionStatus
	case *evr.EchoToolsLobbyEntrantNewV1:
		pipelineFn = p.gameserverLobbyEntrantNew
	case *evr.EchoToolsLobbySessionEndedV1:
		pipelineFn = p.gameserverLobbySessionEnded
	case *evr.EchoToolsLobbySessionLockV1:
		pipelineFn = p.gameserverLobbySessionLock
	case *evr.EchoToolsLobbySessionUnlockV1:
		pipelineFn = p.gameserverLobbySessionUnlock
	case *evr.EchoToolsLobbyEntrantRemovedV1:
		pipelineFn = p.gameserverLobbyEntrantRemoved

	default:
		pipelineFn = func(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
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
					if err := LobbySession(session.(*sessionWS), p.sessionRegistry, idmessage.GetLoginSessionID()); err != nil {
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
		if !params.accountMetadata.Debug {
			//logger = logger.WithOptions(zap.IncreaseLevel(zap.InfoLevel))
		}

		logger = logger.With(zap.String("uid", session.UserID().String()), zap.String("sid", session.ID().String()), zap.String("username", session.Username()), zap.String("evrid", params.xpID.String()))
	}

	if err := pipelineFn(session.Context(), logger, session.(*sessionWS), in); err != nil {
		// Unwrap the error
		logger.Error("Pipeline error", zap.Error(err))
		// TODO: Handle errors and close the connection
	}
	// Keep the connection open, otherwise the client will display "service unavailable"
	return true
}

// Process outgoing protobuf envelopes and translate them to Evr messages
func ProcessOutgoing(logger *zap.Logger, session *sessionWS, in *rtapi.Envelope) ([]evr.Message, error) {
	p := session.evrPipeline

	switch in.Message.(type) {

	case *rtapi.Envelope_StreamData:
		// EVR binary protocol data
		payload := []byte(in.GetStreamData().GetData())
		if bytes.HasPrefix(payload, evr.MessageMarker) {
			return nil, session.SendBytes(payload, true)
		}

	case *rtapi.Envelope_MatchData:
		// EVR binary protocol data
		if in.GetMatchData().GetOpCode() == OpCodeEVRPacketData {
			return nil, session.SendBytes(in.GetMatchData().GetData(), true)
		}
	}

	params, ok := LoadParams(session.Context())
	if !ok {
		logger.Error("Failed to get lobby parameters")
		return nil, nil
	}

	// DM the user on discord
	if !strings.HasPrefix(session.Username(), "broadcaster:") && params.relayOutgoing {
		content := ""
		switch msg := in.Message.(type) {
		case *rtapi.Envelope_StreamData:

			discordMessage := struct {
				Stream  *rtapi.Stream       `json:"stream"`
				Sender  *rtapi.UserPresence `json:"sender"`
				Content json.RawMessage     `json:"content"`
			}{
				msg.StreamData.Stream,
				msg.StreamData.Sender,
				json.RawMessage(msg.StreamData.Data),
			}

			if msg.StreamData.Stream.Mode == int32(SessionFormatJson) {
				data, err := json.MarshalIndent(discordMessage, "", "  ")
				if err != nil {
					logger.Error("Failed to marshal stream data", zap.Error(err))
				} else {
					content = fmt.Sprintf("```json\n%s\n```", data)
				}
			}

		case *rtapi.Envelope_Error:

			// Json the message
			data, err := json.MarshalIndent(in.GetError(), "", "  ")
			if err != nil {
				logger.Error("Failed to marshal error", zap.Error(err))
			} else {
				content = string("```json\n" + string(data) + "\n```")
			}

		case *rtapi.Envelope_StatusPresenceEvent, *rtapi.Envelope_MatchPresenceEvent, *rtapi.Envelope_StreamPresenceEvent:

			// Json the message
			data, _ := json.MarshalIndent(in.GetMessage(), "", "  ")
			content = string("```json\n" + string(data) + "\n```")

		case *rtapi.Envelope_Party:
			discordIDs := make([]string, 0)
			leader := in.GetParty().GetLeader()
			userIDs := make([]string, 0)

			// Put leader first
			if leader != nil {
				userIDs = append(userIDs, leader.GetUserId())
			}
			for _, m := range in.GetParty().GetPresences() {
				if m.GetUserId() == leader.GetUserId() {
					continue
				}
				userIDs = append(userIDs, m.GetUserId())
			}
			partyGroupName := ""
			var err error
			for _, userID := range userIDs {
				if partyGroupName == "" {
					partyGroupName, _, err = GetLobbyGroupID(session.Context(), session.pipeline.db, userID)
					if err != nil {
						logger.Warn("Failed to get party group ID", zap.Error(err))
					}
				}
				if discordID, err := GetDiscordIDByUserID(session.Context(), session.pipeline.db, userID); err != nil {
					logger.Warn("Failed to get discord ID", zap.Error(err))
					discordIDs = append(discordIDs, userID)
				} else {
					discordIDs = append(discordIDs, fmt.Sprintf("<@%s>", discordID))
				}
			}

			content = fmt.Sprintf("Active party `%s`: %s", partyGroupName, strings.Join(discordIDs, ", "))

		case *rtapi.Envelope_PartyLeader:
			if discordID, err := GetDiscordIDByUserID(session.Context(), session.pipeline.db, in.GetPartyLeader().GetPresence().GetUserId()); err != nil {
				logger.Warn("Failed to get discord ID", zap.Error(err))
				content = fmt.Sprintf("Party leader: %s", in.GetPartyLeader().GetPresence().GetUsername())
			} else {
				content = fmt.Sprintf("New party leader: <@%s>", discordID)
			}

		case *rtapi.Envelope_PartyJoinRequest:

		case *rtapi.Envelope_PartyPresenceEvent:
			event := in.GetPartyPresenceEvent()
			joins := make([]string, 0)

			for _, join := range event.GetJoins() {
				if join.GetUserId() != session.UserID().String() {
					if discordID, err := GetDiscordIDByUserID(session.Context(), session.pipeline.db, join.GetUserId()); err != nil {
						logger.Warn("Failed to get discord ID", zap.Error(err))
						joins = append(joins, join.GetUsername())
					} else {
						joins = append(joins, fmt.Sprintf("<@%s>", discordID))
					}
				}
			}
			leaves := make([]string, 0)
			for _, leave := range event.GetLeaves() {
				if discordID, err := GetDiscordIDByUserID(session.Context(), session.pipeline.db, leave.GetUserId()); err != nil {
					logger.Warn("Failed to get discord ID", zap.Error(err))
					leaves = append(leaves, leave.GetUsername())
				} else {
					leaves = append(leaves, fmt.Sprintf("<@%s>", discordID))
				}
			}

			if len(joins) > 0 {
				content += fmt.Sprintf("Party join: %s\n", strings.Join(joins, ", "))
			}
			if len(leaves) > 0 {
				content += fmt.Sprintf("Party leave: %s\n", strings.Join(leaves, ", "))
			}

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
			} else if discordID, err := GetDiscordIDByUserID(session.Context(), session.pipeline.db, session.UserID().String()); err != nil {
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
