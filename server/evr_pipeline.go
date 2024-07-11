package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
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

	broadcasterRegistrationBySession *MapOf[string, *MatchBroadcaster] // sessionID -> MatchBroadcaster
	matchBySessionID                 *MapOf[string, string]            // sessionID -> matchID
	loginSessionByEvrID              *MapOf[string, *sessionWS]
	backfillQueue                    *MapOf[string, *sync.Mutex] // A queue of backfills to avoid double backfill
	placeholderEmail                 string
	linkDeviceURL                    string
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
	}

	discordRegistry := NewLocalDiscordRegistry(ctx, nk, runtimeLogger, metrics, config, pipeline, dg)
	leaderboardRegistry := NewLeaderboardRegistry(NewRuntimeGoLogger(logger), nk, config.GetName())
	profileRegistry := NewProfileRegistry(nk, db, runtimeLogger, discordRegistry)

	appBot := NewDiscordAppBot(nk, runtimeLogger, metrics, pipeline, config, discordRegistry, profileRegistry, dg)

	if disable, ok := vars["DISABLE_DISCORD_BOT"]; ok && disable == "true" {
		logger.Info("Discord bot is disabled")
	} else {
		if err := appBot.InitializeDiscordBot(); err != nil {
			logger.Error("Failed to initialize app bot", zap.Error(err))
		}
	}

	if err = appBot.dg.Open(); err != nil {
		logger.Error("Failed to open discord bot connection: %w", zap.Error(err))
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

		profileRegistry:     profileRegistry,
		leaderboardRegistry: leaderboardRegistry,

		broadcasterRegistrationBySession: &MapOf[string, *MatchBroadcaster]{},
		matchBySessionID:                 &MapOf[string, string]{},
		loginSessionByEvrID:              &MapOf[string, *sessionWS]{},
		backfillQueue:                    &MapOf[string, *sync.Mutex]{},

		placeholderEmail: config.GetRuntime().Environment["PLACEHOLDER_EMAIL_DOMAIN"],
		linkDeviceURL:    config.GetRuntime().Environment["LINK_DEVICE_URL"],
	}

	evrPipeline.matchmakingRegistry = NewMatchmakingRegistry(logger, matchRegistry, matchmaker, metrics, db, nk, config, evrPipeline)

	runtime.MatchmakerMatched()

	// Create a timer to periodically clear the backfill queue
	go func() {
		interval := 3 * time.Minute
		ticker := time.NewTicker(interval)
		for {
			select {
			case <-evrPipeline.ctx.Done():
				ticker.Stop()
				return

			case <-ticker.C:
				evrPipeline.backfillQueue.Range(func(key string, value *sync.Mutex) bool {
					if value.TryLock() {
						evrPipeline.backfillQueue.Delete(key)
						value.Unlock()
					}
					return true
				})

				evrPipeline.broadcasterRegistrationBySession.Range(func(key string, value *MatchBroadcaster) bool {
					if sessionRegistry.Get(uuid.FromStringOrNil(value.SessionID)) == nil {
						logger.Debug("Housekeeping: Session not found for broadcaster", zap.String("sessionID", value.SessionID))
						evrPipeline.broadcasterRegistrationBySession.Delete(key)
					}
					return true
				})

				evrPipeline.loginSessionByEvrID.Range(func(key string, value *sessionWS) bool {
					if sessionRegistry.Get(value.ID()) == nil {
						logger.Debug("Housekeeping: Session not found for evrID", zap.String("evrID", key))
						evrPipeline.loginSessionByEvrID.Delete(key)
					}
					return true
				})

				evrPipeline.matchBySessionID.Range(func(key string, value string) bool {
					if sessionRegistry.Get(uuid.FromStringOrNil(key)) == nil {
						logger.Debug("Housekeeping: Session not found for matchID", zap.String("matchID", value))
						evrPipeline.matchBySessionID.Delete(key)
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
		pipelineFn = p.relayMatchData
	case *evr.LobbyPendingSessionCancel:
		pipelineFn = p.lobbyPendingSessionCancel

	// ServerDB service
	case *evr.BroadcasterRegistrationRequest:
		requireAuthed = false
		pipelineFn = p.broadcasterRegistrationRequest
	case *evr.BroadcasterSessionEnded:
		pipelineFn = p.broadcasterSessionEnded
	case *evr.BroadcasterPlayersAccept:
		pipelineFn = p.relayMatchData
	case *evr.BroadcasterSessionStarted:
		pipelineFn = p.relayMatchData
	case *evr.BroadcasterChallengeRequest:
		pipelineFn = p.relayMatchData
	case *evr.BroadcasterPlayersRejected:
		pipelineFn = p.relayMatchData
	case *evr.BroadcasterPlayerSessionsLocked:
		pipelineFn = p.relayMatchData
	case *evr.BroadcasterPlayerSessionsUnlocked:
		pipelineFn = p.relayMatchData
	case *evr.BroadcasterPlayerRemoved:
		pipelineFn = p.relayMatchData

	default:
		pipelineFn = func(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
			logger.Warn("Received unhandled message", zap.Any("message", in))
			return nil
		}
	}

	if idmessage, ok := in.(evr.IdentifyingMessage); ok {
		// If the message is an identifying message, validate the session and evr id.
		if err := session.ValidateSession(idmessage.SessionID(), idmessage.EvrID()); err != nil {
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

	pipelineFn := func(logger *zap.Logger, session *sessionWS, in *rtapi.Envelope) ([]evr.Message, error) {
		if logger.Core().Enabled(zap.DebugLevel) {
			logger.Debug(fmt.Sprintf("Unhandled protobuf message: %T", in.Message))
		}
		return nil, nil
	}

	switch in.Message.(type) {
	case *rtapi.Envelope_Error:
		envelope := in.GetError()
		logger.Error("Envelope_Error", zap.Int32("code", envelope.Code), zap.String("message", envelope.Message))

	case *rtapi.Envelope_MatchPresenceEvent:
		envelope := in.GetMatchPresenceEvent()
		userID := session.UserID().String()
		matchID := envelope.GetMatchId()

		for _, leave := range envelope.GetLeaves() {
			if leave.GetUserId() != userID {
				// This is not this user
				continue
			}
		}

		for _, join := range envelope.GetJoins() {
			if join.GetUserId() != userID {
				// This is not this user.
				continue
			}

			p.matchBySessionID.Store(session.ID().String(), matchID)

		}

		pipelineFn = func(_ *zap.Logger, _ *sessionWS, _ *rtapi.Envelope) ([]evr.Message, error) {
			return nil, nil
		}
	default:
		// No translation needed.
	}

	if logger.Core().Enabled(zap.DebugLevel) && in.Message != nil {
		logger.Debug(fmt.Sprintf("outgoing protobuf message: %T", in.Message))
	}

	return pipelineFn(logger, session, in)
}

// relayMatchData relays the data to the match by determining the match id from the session or user id.
func (p *EvrPipeline) relayMatchData(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {

	matchID, _, err := GetMatchBySessionID(p.runtimeModule, session.id)
	if err != nil {
		return fmt.Errorf("failed to get match by session ID: %w", err)
	}
	if matchID == nil {
		return fmt.Errorf("no match found for session ID: %s", session.id)
	}

	err = sendMatchData(p.matchRegistry, *matchID, session, in)
	if err != nil {
		return fmt.Errorf("failed to send match data: %w", err)
	}

	return nil
}

// sendMatchData sends the data to the match.
func sendMatchData(matchRegistry MatchRegistry, matchID MatchID, session *sessionWS, in evr.Message) error {

	requestJson, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}
	// Set the OpCode to the symbol of the message.
	opCode := int64(evr.SymbolOf(in))
	// Send the data to the match.
	matchRegistry.SendData(matchID.UUID(), matchID.Node(), session.UserID(), session.ID(), session.Username(), matchID.Node(), opCode, requestJson, true, time.Now().UTC().UnixNano()/int64(time.Millisecond))

	return nil
}

// configRequest handles the config request.
func (p *EvrPipeline) attemptOutOfBandAuthentication(session *sessionWS) error {
	ctx := session.Context()
	// If the session is already authenticated, return.
	if session.UserID() != uuid.Nil {
		return nil
	}
	userPassword, ok := ctx.Value(ctxPasswordKey{}).(string)
	if !ok {
		return nil
	}

	discordId, ok := ctx.Value(ctxDiscordIdKey{}).(string)
	if !ok {
		return nil
	}

	// Get the account for this discordId
	uid, err := p.discordRegistry.GetUserIdByDiscordId(ctx, discordId, false)
	if err != nil {
		return fmt.Errorf("out of band for discord ID %s: %v", discordId, err)
	}
	userId := uid.String()

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

	return session.BroadcasterSession(userId, "broadcaster:"+username)
}
