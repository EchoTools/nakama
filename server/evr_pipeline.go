package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/heroiclabs/nakama/v3/social"

	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protojson"
)

var GlobalConfig = &struct {
	sync.RWMutex
	rejectMatchmaking bool
}{
	rejectMatchmaking: true,
}

type EvrPipeline struct {
	sync.RWMutex
	ctx context.Context

	node              string
	broadcasterUserID string // The userID used for broadcaster connections
	externalIP        net.IP // Server's external IP for external connections
	localIP           net.IP // Server's local IP for external connections

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

	matchmakingRegistry *MatchmakingRegistry
	profileRegistry     *ProfileRegistry
	discordRegistry     DiscordRegistry
	appBot              *DiscordAppBot
	leaderboardRegistry *LeaderboardRegistry
	sbmm                *SkillBasedMatchmaker

	createLobbyMu                    sync.Mutex
	broadcasterRegistrationBySession *MapOf[string, *MatchBroadcaster] // sessionID -> MatchBroadcaster

	placeholderEmail string
	linkDeviceURL    string
}

type ctxDiscordBotTokenKey struct{}

func NewEvrPipeline(logger *zap.Logger, startupLogger *zap.Logger, db *sql.DB, protojsonMarshaler *protojson.MarshalOptions, protojsonUnmarshaler *protojson.UnmarshalOptions, config Config, version string, socialClient *social.Client, storageIndex StorageIndex, leaderboardScheduler LeaderboardScheduler, leaderboardCache LeaderboardCache, leaderboardRankCache LeaderboardRankCache, sessionRegistry SessionRegistry, sessionCache SessionCache, statusRegistry StatusRegistry, matchRegistry MatchRegistry, matchmaker Matchmaker, tracker Tracker, router MessageRouter, streamManager StreamManager, metrics Metrics, pipeline *Pipeline, runtime *Runtime) *EvrPipeline {

	// Add the bot token to the context

	vars := config.GetRuntime().Environment

	ctx := context.WithValue(context.Background(), ctxDiscordBotTokenKey{}, vars["DISCORD_BOT_TOKEN"])
	ctx = context.WithValue(ctx, ctxNodeKey{}, config.GetName())
	nk := NewRuntimeGoNakamaModule(logger, db, protojsonMarshaler, config, socialClient, leaderboardCache, leaderboardRankCache, leaderboardScheduler, sessionRegistry, sessionCache, statusRegistry, matchRegistry, tracker, metrics, streamManager, router, storageIndex)

	// TODO Add a symbol cache that gets populated and stored back occasionally

	runtimeLogger := NewRuntimeGoLogger(logger)

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

	discordRegistry := NewLocalDiscordRegistry(ctx, nk, runtimeLogger, metrics, config, pipeline, dg)
	leaderboardRegistry := NewLeaderboardRegistry(NewRuntimeGoLogger(logger), nk, config.GetName())
	profileRegistry := NewProfileRegistry(nk, db, runtimeLogger, tracker, discordRegistry)
	broadcasterRegistrationBySession := MapOf[string, *MatchBroadcaster]{}
	skillBasedMatchmaker := NewSkillBasedMatchmaker()
	var appBot *DiscordAppBot

	if disable, ok := vars["DISABLE_DISCORD_BOT"]; ok && disable == "true" {
		logger.Info("Discord bot is disabled")
	} else {
		appBot, err = NewDiscordAppBot(runtimeLogger, nk, db, metrics, pipeline, config, discordRegistry, profileRegistry, dg)
		if err != nil {
			logger.Error("Failed to create app bot", zap.Error(err))

		}
		if err := appBot.InitializeDiscordBot(); err != nil {
			logger.Error("Failed to initialize app bot", zap.Error(err))
		}
		if err = appBot.dg.Open(); err != nil {
			logger.Error("Failed to open discord bot connection: %w", zap.Error(err))
		}
	}

	// Every 5 minutes, store the cache.

	localIP, err := DetermineLocalIPAddress()
	if err != nil {
		logger.Fatal("Failed to determine local IP address", zap.Error(err))
	}

	// loop until teh external IP is set
	externalIP, err := DetermineExternalIPAddress()
	if err != nil {
		logger.Fatal("Failed to determine external IP address", zap.Error(err))
	}

	broadcasterUserID, _, _, err := nk.AuthenticateCustom(ctx, "000000000000000000", "broadcasthost", true)
	if err != nil {
		logger.Fatal("Failed to authenticate broadcaster", zap.Error(err))
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

		discordRegistry:   discordRegistry,
		appBot:            appBot,
		localIP:           localIP,
		externalIP:        externalIP,
		broadcasterUserID: broadcasterUserID,

		profileRegistry:                  profileRegistry,
		leaderboardRegistry:              leaderboardRegistry,
		sbmm:                             skillBasedMatchmaker,
		broadcasterRegistrationBySession: &broadcasterRegistrationBySession,

		placeholderEmail: config.GetRuntime().Environment["PLACEHOLDER_EMAIL_DOMAIN"],
		linkDeviceURL:    config.GetRuntime().Environment["LINK_DEVICE_URL"],
	}

	evrPipeline.matchmakingRegistry = NewMatchmakingRegistry(logger, matchRegistry, matchmaker, metrics, db, nk, config, evrPipeline)

	// Create a timer to periodically clear the backfill queue
	go func() {
		interval := 3 * time.Minute
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-evrPipeline.ctx.Done():
				ticker.Stop()
				return

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

func (p *EvrPipeline) SetApiServer(apiServer *ApiServer) {
	p.apiServer = apiServer
}

func (p *EvrPipeline) Stop() {}

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
		pipelineFn = p.lobbyFindSessionRequest
	case *evr.LobbyCreateSessionRequest:
		pipelineFn = p.lobbyCreateSessionRequest
	case *evr.LobbyJoinSessionRequest:
		pipelineFn = p.lobbyJoinSessionRequest
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
	case *evr.BroadcasterSessionStarted:
		pipelineFn = p.broadcasterSessionStarted
	case *evr.GameServerJoinAttempt:
		pipelineFn = p.broadcasterPlayerAccept
	case *evr.BroadcasterPlayerRemoved:
		pipelineFn = p.broadcasterPlayerRemoved
	case *evr.BroadcasterPlayerSessionsLocked:
		pipelineFn = p.broadcasterPlayerSessionsLocked
	case *evr.BroadcasterPlayerSessionsUnlocked:
		pipelineFn = p.broadcasterPlayerSessionsUnlocked
	case *evr.BroadcasterSessionEnded:
		pipelineFn = p.broadcasterSessionEnded

	default:
		pipelineFn = func(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
			logger.Warn("Received unhandled message", zap.Any("message", in))
			return nil
		}
	}

	if idmessage, ok := in.(evr.IdentifyingMessage); ok {
		// Validate the user identifier
		if !idmessage.GetEvrID().Valid() {
			logger.Error("Invalid evr id", zap.String("evr_id", idmessage.GetEvrID().String()))
			return false
		}
		// If the message is an identifying message, validate the session and evr id.
		if err := session.ValidateSession(idmessage.GetSessionID(), idmessage.GetEvrID()); err != nil {
			logger.Error("Invalid session", zap.Error(err))
			// Disconnect the client if the session is invalid.
			return false
		}
	}

	if err := p.discordRegistry.ProcessRequest(session.Context(), session, in); err != nil {
		logger.Warn("Discord Bot logger error", zap.Error(err))
	}

	// If the message requires authentication, check if the session is authenticated.
	if requireAuthed {
		// If the session is not authenticated, log the error and return.
		if session != nil && session.UserID() == uuid.Nil {
			logger.Debug("Session not authenticated")
			// As a work around to the serverdb connection getting lost and needing to reauthenticate
			if err := p.attemptOutOfBandAuthentication(session); err != nil {
				// If the session is now authenticated, continue processing the request.
				logger.Debug("Failed to authenticate session with discordId and password", zap.Error(err))
				return false
			}
		}
	}

	evrId, ok := session.Context().Value(ctxEvrIDKey{}).(evr.EvrId)
	if ok {
		logger = logger.With(zap.String("uid", session.UserID().String()), zap.String("sid", session.ID().String()), zap.String("uname", session.Username()), zap.String("evrid", evrId.String()))
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
	// TODO FIXME Catch the match rejection message and translate it to an evr message
	// TODO FIXME Catch the match leave message and translate it to an evr message
	p := session.evrPipeline

	switch in.Message.(type) {
	case *rtapi.Envelope_StreamData:
		return nil, session.SendBytes([]byte(in.GetStreamData().GetData()), true)
	case *rtapi.Envelope_MatchData:
		if in.GetMatchData().GetOpCode() == OpCodeEVRPacketData {
			return nil, session.SendBytes(in.GetMatchData().GetData(), true)
		}
	}

	verbose, ok := session.Context().Value(ctxVerboseKey{}).(bool)
	if !ok {
		verbose = false
	}

	// DM the user on discord
	if !strings.HasPrefix(session.Username(), "broadcaster:") && verbose {
		content := ""
		switch in.Message.(type) {
		case *rtapi.Envelope_StatusPresenceEvent, *rtapi.Envelope_MatchPresenceEvent, *rtapi.Envelope_StreamPresenceEvent:
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
					partyGroupName, _, err = GetPartyGroupID(session.Context(), session.pipeline.db, userID)
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
			if dg := p.discordRegistry.GetBot(); dg == nil {
				// No discord bot
			} else if discordID, err := GetDiscordIDByUserID(session.Context(), session.pipeline.db, session.UserID().String()); err != nil {
				logger.Warn("Failed to get discord ID", zap.Error(err))
			} else if channel, err := dg.UserChannelCreate(discordID); err != nil {
				logger.Warn("Failed to create DM channel", zap.Error(err))
			} else if _, err = dg.ChannelMessageSend(channel.ID, content); err != nil {
				logger.Warn("Failed to send message to user", zap.Error(err))
			}
		}
	}
	return nil, nil
}

// relayMatchData relays the data to the match by determining the match id from the session or user id.
func (p *EvrPipeline) relayMatchData(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	var matchID MatchID
	var err error
	if message, ok := in.(evr.MatchSessionMessage); ok {
		if matchID, err = NewMatchID(message.MatchSessionID(), p.node); err != nil {
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
	p.matchRegistry.SendData(matchID.UUID(), matchID.Node(), session.UserID(), session.ID(), session.Username(), matchID.Node(), opCode, requestJson, true, time.Now().UTC().UnixNano()/int64(time.Millisecond))

	return nil
}

// configRequest handles the config request.
func (p *EvrPipeline) attemptOutOfBandAuthentication(session *sessionWS) error {
	ctx := session.Context()
	// If the session is already authenticated, return.
	if session.UserID() != uuid.Nil {
		return nil
	}
	userPassword, ok := ctx.Value(ctxAuthPasswordKey{}).(string)
	if !ok {
		return nil
	}

	discordId, ok := ctx.Value(ctxAuthDiscordIDKey{}).(string)
	if !ok {
		return nil
	}

	// Get the account for this discordId
	userId, err := GetUserIDByDiscordID(ctx, p.db, discordId)
	if err != nil {
		return fmt.Errorf("out of band for discord ID %s: %v", discordId, err)
	}

	// The account was found.
	account, err := GetAccount(ctx, session.logger, session.pipeline.db, session.statusRegistry, uuid.FromStringOrNil(userId))
	if err != nil {
		return fmt.Errorf("out of band Auth: %s: %v", discordId, err)
	}

	// Do email authentication
	userId, username, _, err := AuthenticateEmail(ctx, session.logger, session.pipeline.db, account.Email, userPassword, "", false)
	if err != nil {
		return fmt.Errorf("out of band Auth: %s: %v", discordId, err)
	}

	return session.BroadcasterSession(uuid.FromStringOrNil(userId), "broadcaster:"+username, 0)
}
