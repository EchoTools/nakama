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
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/heroiclabs/nakama/v3/social"

	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protojson"

	_ "net/http/pprof"
	// Import for side effects to enable pprof endpoint
)

var GlobalConfig = &struct {
	sync.RWMutex
	rejectMatchmaking bool
}{
	rejectMatchmaking: true,
}

var unrequireMessage = evr.NewSTcpConnectionUnrequireEvent()

type EvrPipeline struct {
	sync.RWMutex
	ctx context.Context

	node              string
	broadcasterUserID string        // The userID used for broadcaster connections
	externalIP        *atomic.Value // Server's external IP for external connections
	localIP           net.IP        // Server's local IP for external connections

	logger               *zap.Logger
	db                   *sql.DB
	config               Config
	version              string
	socialClient         *social.Client
	storageIndex         StorageIndex
	leaderboardCache     LeaderboardCache
	leaderboardRankCache LeaderboardRankCache
	sessionCache         SessionCache
	apiServer            *ApiServer
	sessionRegistry      SessionRegistry
	statusRegistry       StatusRegistry
	matchRegistry        MatchRegistry
	tracker              Tracker
	router               MessageRouter
	streamManager        StreamManager
	metrics              Metrics
	runtime              *Runtime
	runtimeModule        *RuntimeGoNakamaModule
	runtimeLogger        runtime.Logger

	profileRegistry              *ProfileRegistry
	discordCache                 *DiscordCache
	guildGroupCache              *GuildGroupCache
	appBot                       *DiscordAppBot
	leaderboardRegistry          *LeaderboardRegistry
	userRemoteLogJournalRegistry *UserLogJouralRegistry
	ipqsClient                   *IPQSClient
	matchLogManager              *MatchLogManager

	createLobbyMu                    sync.Mutex
	broadcasterRegistrationBySession *MapOf[string, *MatchBroadcaster] // sessionID -> MatchBroadcaster

	placeholderEmail string
	linkDeviceURL    string

	cacheMu sync.Mutex // Writers only

	messageCache *atomic.Value // map[string]evr.Message

}

type ctxDiscordBotTokenKey struct{}

func NewEvrPipeline(logger *zap.Logger, startupLogger *zap.Logger, db *sql.DB, protojsonMarshaler *protojson.MarshalOptions, protojsonUnmarshaler *protojson.UnmarshalOptions, config Config, version string, socialClient *social.Client, storageIndex StorageIndex, leaderboardScheduler LeaderboardScheduler, leaderboardCache LeaderboardCache, leaderboardRankCache LeaderboardRankCache, sessionRegistry SessionRegistry, sessionCache SessionCache, statusRegistry StatusRegistry, matchRegistry MatchRegistry, matchmaker Matchmaker, tracker Tracker, router MessageRouter, streamManager StreamManager, metrics Metrics, pipeline *Pipeline, runtime *Runtime) *EvrPipeline {

	// Add the bot token to the context

	vars := config.GetRuntime().Environment

	ctx := context.WithValue(context.Background(), ctxDiscordBotTokenKey{}, vars["DISCORD_BOT_TOKEN"])

	nk := NewRuntimeGoNakamaModule(logger, db, protojsonMarshaler, config, socialClient, leaderboardCache, leaderboardRankCache, leaderboardScheduler, sessionRegistry, sessionCache, statusRegistry, matchRegistry, tracker, metrics, streamManager, router, storageIndex)

	runtimeLogger := NewRuntimeGoLogger(logger)

	// Load the global settings
	if _, err := LoadGlobalSettingsData(ctx, nk); err != nil {
		logger.Fatal("Failed to load global settings", zap.Error(err))
	}

	botToken, ok := ctx.Value(ctxDiscordBotTokenKey{}).(string)
	if !ok {
		panic("Bot token is not set in context.")
	}

	var dg *discordgo.Session
	var err error
	if botToken != "" {
		dg, err = discordgo.New("Bot " + botToken)
		if err != nil {
			logger.Error("Unable to create bot")
		}
		dg.StateEnabled = true
	}

	leaderboardRegistry := NewLeaderboardRegistry(runtimeLogger, nk, config.GetName())
	profileRegistry := NewProfileRegistry(nk, db, runtimeLogger, tracker, metrics)
	broadcasterRegistrationBySession := MapOf[string, *MatchBroadcaster]{}
	lobbyBuilder := NewLobbyBuilder(logger, nk, sessionRegistry, matchRegistry, tracker, metrics, profileRegistry)
	matchmaker.OnMatchedEntries(lobbyBuilder.handleMatchedEntries)
	userRemoteLogJournalRegistry := NewUserRemoteLogJournalRegistry(logger, nk, sessionRegistry)
	guildGroupCache := NewGuildGroupCache(ctx, runtimeLogger, nk)
	guildGroupCache.Start()

	ipqsClient, err := NewIPQS(logger, db, metrics, storageIndex, vars["IPQS_API_KEY"])
	if err != nil {
		logger.Fatal("Failed to create IPQS client", zap.Error(err))
	}

	messageCache := &atomic.Value{}
	messageCache.Store(make(map[string]evr.Message))

	var appBot *DiscordAppBot
	var discordCache *DiscordCache
	if disable, ok := vars["DISABLE_DISCORD_BOT"]; ok && disable == "true" {
		logger.Info("Discord bot is disabled")
	} else {
		discordCache = NewDiscordCache(ctx, logger, config, metrics, guildGroupCache, nk, db, dg)
		discordCache.Start()

		appBot, err = NewDiscordAppBot(runtimeLogger, nk, db, metrics, pipeline, config, discordCache, profileRegistry, statusRegistry, dg)
		if err != nil {
			logger.Error("Failed to create app bot", zap.Error(err))

		}
		if err := appBot.InitializeDiscordBot(); err != nil {
			logger.Error("Failed to initialize app bot", zap.Error(err))
		}
		if err = appBot.dg.Open(); err != nil {
			logger.Warn("Failed to open discord bot connection: %w", zap.Error(err))
		}
	}

	matchLogManager := NewMatchLogManager(ctx, logger, vars["MONGO_URI"])
	matchLogManager.Start()
	localIP, err := DetermineLocalIPAddress()
	if err != nil {
		logger.Fatal("Failed to determine local IP address", zap.Error(err))
	}

	evrPipeline := &EvrPipeline{
		ctx:                  ctx,
		node:                 config.GetName(),
		logger:               logger,
		db:                   db,
		config:               config,
		version:              version,
		socialClient:         socialClient,
		leaderboardCache:     leaderboardCache,
		leaderboardRankCache: leaderboardRankCache,
		storageIndex:         storageIndex,
		sessionCache:         sessionCache,
		sessionRegistry:      sessionRegistry,
		statusRegistry:       statusRegistry,
		matchRegistry:        matchRegistry,
		tracker:              tracker,
		router:               router,
		streamManager:        streamManager,
		metrics:              metrics,
		runtime:              runtime,
		runtimeModule:        nk,
		runtimeLogger:        runtimeLogger,

		discordCache:    discordCache,
		guildGroupCache: guildGroupCache,
		appBot:          appBot,
		localIP:         localIP,
		externalIP:      &atomic.Value{},

		profileRegistry:                  profileRegistry,
		leaderboardRegistry:              leaderboardRegistry,
		broadcasterRegistrationBySession: &broadcasterRegistrationBySession,
		userRemoteLogJournalRegistry:     userRemoteLogJournalRegistry,
		ipqsClient:                       ipqsClient,
		matchLogManager:                  matchLogManager,

		placeholderEmail: config.GetRuntime().Environment["PLACEHOLDER_EMAIL_DOMAIN"],
		linkDeviceURL:    config.GetRuntime().Environment["LINK_DEVICE_URL"],

		messageCache: messageCache,
	}

	evrPipeline.externalIP.Store(net.IPv4zero)

	externalIP, err := DetermineExternalIPAddress()
	if err != nil {
		logger.Warn("Failed to determine external IP address, using internal", zap.Error(err))
	} else {
		evrPipeline.externalIP.Store(externalIP)
	}

	// Create a timer to periodically clear the backfill queue
	go func() {
		interval := 3 * time.Minute

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		messageCacheTicker := time.NewTicker(3 * time.Minute)
		defer messageCacheTicker.Stop()

		for {
			select {
			case <-evrPipeline.ctx.Done():
				ticker.Stop()
				return

			case <-messageCacheTicker.C:

				evrPipeline.cacheMu.Lock()
				evrPipeline.messageCache.Store(make(map[string]evr.Message))
				evrPipeline.cacheMu.Unlock()

			case <-ticker.C:

				evrPipeline.broadcasterRegistrationBySession.Range(func(key string, value *MatchBroadcaster) bool {
					if sessionRegistry.Get(uuid.FromStringOrNil(value.SessionID)) == nil {
						logger.Debug("Housekeeping: Session not found for broadcaster", zap.String("sessionID", value.SessionID))
						evrPipeline.broadcasterRegistrationBySession.Delete(key)
					}
					return true
				})

			}
		}
	}()

	return evrPipeline
}

func (p *EvrPipeline) ServiceExternalIP() net.IP {
	extIP := p.externalIP.Load().(net.IP)
	var err error
	// Update it, if it's missing
	if extIP == nil {
		extIP, err = DetermineExternalIPAddress()
		if err != nil {
			p.logger.Warn("Failed to determine external IP address, using internal", zap.Error(err))
			extIP, err = DetermineLocalIPAddress()
			if err != nil {
				p.logger.Warn("Could not determine internal IP")
			}
		} else {
			p.externalIP.Store(extIP)
		}
	}
	return extIP
}

func (p *EvrPipeline) SetApiServer(apiServer *ApiServer) {
	p.apiServer = apiServer
}

func (p *EvrPipeline) Stop() {}

func (p *EvrPipeline) CacheMessage(key string, message evr.Message) {
	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()

	newCache := make(map[string]evr.Message)
	for k, v := range p.messageCache.Load().(map[string]evr.Message) {
		newCache[k] = v
	}
	newCache[key] = message
	p.messageCache.Store(newCache)
}

func (p *EvrPipeline) GetCachedMessage(key string) evr.Message {
	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()

	return p.messageCache.Load().(map[string]evr.Message)[key]
}

func (p *EvrPipeline) ProcessRequestEVR(logger *zap.Logger, session *sessionWS, in evr.Message) bool {

	var pipelineFn func(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error

	requireAuthed := true

	switch in.(type) {
	// Config service
	case *evr.ConfigRequest:
		requireAuthed = false
		pipelineFn = p.configRequest

	// Transaction (IAP) service
	case *evr.ReconcileIAP:
		requireAuthed = false
		pipelineFn = p.reconcileIAP

	// Login Service
	case *evr.RemoteLogSet:
		requireAuthed = false
		pipelineFn = p.remoteLogSetv3
	case *evr.LoginRequest:
		requireAuthed = false
		pipelineFn = p.loginRequest
	case *evr.DocumentRequest:
		pipelineFn = p.documentRequest
	case *evr.LoggedInUserProfileRequest:
		pipelineFn = p.loggedInUserProfileRequest
	case *evr.ChannelInfoRequest:
		pipelineFn = p.channelInfoRequest
	case *evr.UpdateClientProfile:
		pipelineFn = p.updateClientProfileRequest
	case *evr.OtherUserProfileRequest: // Broadcaster only via it's login connection
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
	case *evr.BroadcasterRegistrationRequest:
		requireAuthed = false
		pipelineFn = p.broadcasterRegistrationRequest
	case *evr.EchoToolsGameServerRegistrationRequestV1:
		requireAuthed = false
		pipelineFn = p.echoToolsGameServerRegistrationRequestV1

	case *evr.BroadcasterSessionStarted:
		pipelineFn = p.broadcasterSessionStarted

	case *evr.GameServerJoinAttempt:
		pipelineFn = p.legacyGameServerPlayerAccept
	case *evr.GameServerPlayerRemoved:
		pipelineFn = p.broadcasterPlayerRemoved
	case *evr.BroadcasterPlayerSessionsLocked:
		pipelineFn = p.broadcasterPlayerSessionsLocked
	case *evr.BroadcasterPlayerSessionsUnlocked:
		pipelineFn = p.broadcasterPlayerSessionsUnlocked
	case *evr.BroadcasterSessionEnded:
		pipelineFn = p.broadcasterSessionEnded

		// Advanced Game Servers
	case *evr.EchoToolsLobbySessionStartedV1:
		pipelineFn = p.echoToolsLobbySessionStartedV1
	case *evr.EchoToolsLobbyStatusV1:
		pipelineFn = p.echoToolsLobbyStatusV1
	case *evr.EchoToolsLobbyEntrantNewV1:
		pipelineFn = p.echoToolsLobbyEntrantNewV1
	case *evr.EchoToolsLobbySessionEndedV1:
		pipelineFn = p.echoToolsLobbySessionEndedV1

	default:
		pipelineFn = func(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
			logger.Warn("Received unhandled message", zap.Any("message", in))
			return nil
		}
	}

	// Identifying messages are used to associate the message with a user.
	// The session ID is used to identify the user.
	// The session ID is mostly ignored once the connection is established.
	if idmessage, ok := in.(evr.LoginIdentifier); ok {
		// If the message is an identifying message, validate the session and evr id.
		if err := session.LobbySession(idmessage.GetLoginSessionID()); err != nil {
			logger.Error("Invalid session", zap.Error(err))
			// Disconnect the client if the session is invalid.
			return false
		}
	}

	if xpimessage, ok := in.(evr.XPIdentifier); ok {
		// If the message is an identifying message, validate the session and evr id.
		params, ok := LoadParams(session.Context())
		if !ok {
			logger.Error("Failed to get lobby parameters")
			return false
		}

		if !params.EvrID.Equals(xpimessage.GetEvrID()) {
			logger.Error("mismatched evr id", zap.String("evrid", xpimessage.GetEvrID().String()), zap.String("evrid2", params.EvrID.String()))
			return false
		}
	}

	// If the message requires authentication, check if the session is authenticated.
	if requireAuthed {
		// If the session is not authenticated, log the error and return.
		if session != nil && session.UserID() == uuid.Nil {

			logger.Warn("Received unauthenticated message", zap.Any("message", in))

			// Send an unrequire
			if err := session.SendEvr(unrequireMessage); err != nil {
				logger.Error("Failed to send unrequire message", zap.Error(err))
				return false
			}

			return true
		}
	}

	if params, ok := LoadParams(session.Context()); ok {
		evrID := params.EvrID
		if !evrID.IsNil() {
			logger = logger.With(zap.String("uid", session.UserID().String()), zap.String("sid", session.ID().String()), zap.String("username", session.Username()), zap.String("evrid", evrID.String()))
		}
	}

	if err := pipelineFn(session.Context(), logger, session, in); err != nil {
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
	if !strings.HasPrefix(session.Username(), "broadcaster:") && params.RelayOutgoing {
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

// relayMatchData relays the data to the match by determining the match id from the session or user id.
func (p *EvrPipeline) relayMatchData(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	var matchID MatchID
	var err error
	if message, ok := in.(evr.LobbySessionMessage); ok {
		if matchID, err = NewMatchID(message.LobbyID(), p.node); err != nil {
			return fmt.Errorf("failed to create match ID: %w", err)
		}
	} else if matchID, _, err = GameServerBySessionID(p.runtimeModule, session.id); err != nil {
		return fmt.Errorf("failed to get match by session ID: %w", err)
	} else if matchID.IsNil() {
		return fmt.Errorf("no match found for session ID: %s", session.id)
	}

	requestJson, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}
	// Set the OpCode to the symbol of the message.
	opCode := int64(evr.SymbolOf(in))
	// Send the data to the match.
	p.matchRegistry.SendData(matchID.UUID, matchID.Node, session.UserID(), session.ID(), session.Username(), matchID.Node, opCode, requestJson, true, time.Now().UTC().UnixNano()/int64(time.Millisecond))

	return nil
}
