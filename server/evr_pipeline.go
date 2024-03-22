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

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/heroiclabs/nakama/v3/social"

	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protojson"
)

type EvrPipeline struct {
	sync.RWMutex

	ctx                  context.Context
	node                 string
	externalIP           net.IP
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
	statusRegistry       *StatusRegistry
	matchRegistry        MatchRegistry
	tracker              Tracker
	router               MessageRouter
	streamManager        StreamManager
	metrics              Metrics
	runtime              *Runtime
	evrRuntime           *EvrRuntime
	runtimeModule        *RuntimeGoNakamaModule
	runtimeLogger        runtime.Logger

	discordRegistry DiscordRegistry
	ipCache         *IPinfoCache

	broadcasterRegistrationBySession *MapOf[uuid.UUID, *MatchBroadcaster]
	matchBySession                   *MapOf[uuid.UUID, string]
	matchByUserId                    *MapOf[uuid.UUID, string]
	loginSessionByEvrID              *MapOf[string, *sessionWS]
	matchByEvrId                     *MapOf[string, string] // full match string by evrId token
	matchmakingRegistry              *MatchmakingRegistry

	placeholderEmail string
	linkDeviceUrl    string
}

type ctxDiscordBotTokenKey struct{}

func NewEvrPipeline(logger *zap.Logger, startupLogger *zap.Logger, db *sql.DB, protojsonMarshaler *protojson.MarshalOptions, protojsonUnmarshaler *protojson.UnmarshalOptions, config Config, version string, socialClient *social.Client, storageIndex StorageIndex, leaderboardScheduler LeaderboardScheduler, leaderboardCache LeaderboardCache, leaderboardRankCache LeaderboardRankCache, sessionRegistry SessionRegistry, sessionCache SessionCache, statusRegistry *StatusRegistry, matchRegistry MatchRegistry, matchmaker Matchmaker, tracker Tracker, router MessageRouter, streamManager StreamManager, metrics Metrics, pipeline *Pipeline, runtime *Runtime, evrRuntime *EvrRuntime) *EvrPipeline {
	// The Evr pipeline is going to be a bit "different".
	// It's going to get access to most components, because
	// of the way EVR works, it's going to need to be able
	// to access the API server, and the runtime.
	// TODO find a cleaner way to do this

	// Add the bot token to the context

	vars := config.GetRuntime().Environment

	ctx := context.WithValue(context.Background(), ctxDiscordBotTokenKey{}, vars["DISCORD_BOT_TOKEN"])
	ctx = context.WithValue(ctx, ctxNodeKey{}, config.GetName())
	nk := NewRuntimeGoNakamaModule(logger, db, protojsonMarshaler, config, socialClient, leaderboardCache, leaderboardRankCache, leaderboardScheduler, sessionRegistry, sessionCache, statusRegistry, matchRegistry, tracker, metrics, streamManager, router, storageIndex)

	// TODO Add a symbol cache that gets populated and stored back occasionally

	runtimeLogger := NewRuntimeGoLogger(logger)
	discordRegistry := NewLocalDiscordRegistry(ctx, nk, runtimeLogger, metrics)
	err := discordRegistry.InitializePartyBot(ctx, pipeline)
	if err != nil {
		logger.Error("Failed to initialize party bot", zap.Error(err))
	}

	// Configure the IPinfo cache
	ipCache := NewIPinfoCache(logger, db, vars["IPINFO_TOKEN"])

	// Every 5 minutes, store the cache.

	go func() {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		ipCache.LoadCache(ctx, nk, db)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				ipCache.StoreCache(ctx, nk, db)
			}
		}
	}()
	externalIP, err := ipCache.DetermineExternalIPAddress()
	if err != nil {
		logger.Error("Failed to determine external IP address", zap.Error(err))
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
		evrRuntime:           evrRuntime,
		runtimeModule:        nk,
		runtimeLogger:        runtimeLogger,

		discordRegistry: discordRegistry,
		ipCache:         ipCache,
		externalIP:      externalIP,

		matchmakingRegistry: NewMatchmakingRegistry(logger, matchRegistry, matchmaker, metrics, config),

		broadcasterRegistrationBySession: &MapOf[uuid.UUID, *MatchBroadcaster]{},
		matchBySession:                   &MapOf[uuid.UUID, string]{},
		matchByUserId:                    &MapOf[uuid.UUID, string]{},
		loginSessionByEvrID:              &MapOf[string, *sessionWS]{},
		matchByEvrId:                     &MapOf[string, string]{},

		placeholderEmail: config.GetRuntime().Environment["PLACEHOLDER_EMAIL_DOMAIN"],
		linkDeviceUrl:    config.GetRuntime().Environment["LINK_DEVICE_URL"],
	}
	runtime.MatchmakerMatched()
	return evrPipeline
}

func (p *EvrPipeline) SetApiServer(apiServer *ApiServer) {
	p.apiServer = apiServer
}

func (p *EvrPipeline) Stop() {}

func (p *EvrPipeline) ProcessRequestEvr(logger *zap.Logger, session *sessionWS, in evr.Message) bool {
	if in == nil {
		logger.Error("Received nil message, disconnecting client.")
		return false
	}

	if logger.Core().Enabled(zap.DebugLevel) { // remove extra heavy reflection processing
		logger.Debug(fmt.Sprintf("Received message: %T", in))
		logger = logger.With(zap.String("request", fmt.Sprintf("%s", in)))
	}

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
	case *evr.UpdateProfile:
		pipelineFn = p.updateProfile
	case *evr.OtherUserProfileRequest: // Broadcaster only via it's login connection
		pipelineFn = p.relayMatchData
	case *evr.UserServerProfileUpdateRequest: // Broadcaster only via it's login connection
		pipelineFn = p.relayMatchData
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

	// If the message requires authentication, check if the session is authenticated.
	if requireAuthed {
		// If the session is not authenticated, log the error and return.
		if session != nil && session.UserID() == uuid.Nil {
			logger.Error("Session not authenticated")
			// As a work around to the serverdb connection getting lost and needing to reauthenticate
			err := p.attemptOutOfBandAuthentication(session)
			if err == nil {
				return true
			} else {
				// If the session is now authenticated, continue processing the request.
				logger.Error("Failed to authenticate session with discordId and password", zap.Error(err))
				return false
			}
		}
	}

	err := pipelineFn(session.Context(), logger, session, in)
	if err != nil {
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

	pipelineFn := func(*zap.Logger, *sessionWS, *rtapi.Envelope) ([]evr.Message, error) {
		logger.Warn(fmt.Sprintf("Unhandled protobuf message: %T", in.Message))
		return nil, nil
	}

	switch in.Message.(type) {
	case *rtapi.Envelope_MatchPresenceEvent:
		envelope := in.GetMatchPresenceEvent()
		userID := session.UserID().String()
		matchID := envelope.GetMatchId()

		for _, leave := range envelope.GetLeaves() {
			if leave.GetUserId() != userID {
				// This is not this user
				continue
			}
			if evrId, ok := session.Context().Value(ctxEvrIDKey{}).(evr.EvrId); ok {
				p.matchByEvrId.Delete(evrId.Token())
			}
			p.matchBySession.Delete(session.ID())
			p.matchByUserId.Delete(session.UserID())
		}

		for _, join := range envelope.GetJoins() {
			if join.GetUserId() != userID {
				// This is not this user.
				continue
			}
			if evrId, ok := session.Context().Value(ctxEvrIDKey{}).(evr.EvrId); ok {
				p.matchByEvrId.Store(evrId.Token(), matchID)
			}
			p.matchBySession.Store(session.ID(), matchID)
			p.matchByUserId.Store(session.UserID(), matchID)
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
	var matchIdStr string
	var found bool
	// Based on the type of message, the match is looked up differently.
	switch in := in.(type) {
	case *evr.OtherUserProfileRequest: // Broadcaster only
		// Figure out the match id and send the data into the match.
		matchIdStr, found = p.matchByEvrId.Load(in.EvrId.String())
		if !found {
			return fmt.Errorf("match not found for evrId: %s", in.EvrId.String())
		}
	case *evr.UserServerProfileUpdateRequest: // Broadcaster only
		// Figure out the match id and send the data into the match.
		matchIdStr, found = p.matchByEvrId.Load(in.EvrId.String())
		if !found {
			return fmt.Errorf("match not found for evrId: %s", in.EvrId.String())
		}
	default:
		// broadcasters will match by session.
		matchIdStr, found = p.matchBySession.Load(session.ID())
		if !found {
			// If the match is not found by session, try to find it by user id.
			matchIdStr, found = p.matchByUserId.Load(session.UserID())
			if !found {
				return fmt.Errorf("no match found for user %s or session: %s", session.UserID(), session.ID())
			}
		}
	}
	err := sendMatchData(p.matchRegistry, matchIdStr, session, in)
	if err != nil {
		return fmt.Errorf("failed to send match data: %w", err)
	}

	return nil
}

// An Alternative way to get the data into the match. This is used when the match is not yet joined.
func (p *EvrPipeline) MatchSignalData(logger *zap.Logger, session *sessionWS, matchId string, in evr.Message) error {
	requestJson, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}
	signal := &EvrSignal{
		UserId: session.UserID().String(),
		Signal: int64(evr.SymbolOf(in)),
		Data:   requestJson,
	}
	b, err := json.Marshal(signal)
	if err != nil {
		return fmt.Errorf("failed to marshal signal: %w", err)
	}

	_, err = p.matchRegistry.Signal(session.Context(), matchId, string(b))
	if err != nil {
		return fmt.Errorf("failed to signal match: %w", err)
	}
	return nil
}

// sendMatchData sends the data to the match.
func sendMatchData(matchRegistry MatchRegistry, matchIdStr string, session *sessionWS, in evr.Message) error {
	matchIDComponents := strings.SplitN(matchIdStr, ".", 2)
	if len(matchIDComponents) != 2 {
		return fmt.Errorf("invalid match id: %s", matchIdStr)
	}
	matchId := uuid.FromStringOrNil(matchIDComponents[0])
	matchNode := matchIDComponents[1]
	requestJson, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}
	// Set the OpCode to the symbol of the message.
	opCode := int64(evr.SymbolOf(in))
	// Send the data to the match.
	matchRegistry.SendData(matchId, matchNode, session.UserID(), session.ID(), session.Username(), matchNode, opCode, requestJson, true, time.Now().UTC().UnixNano()/int64(time.Millisecond))

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
	userId, err := p.discordRegistry.GetUserIdByDiscordId(ctx, discordId, false)
	if err != nil {
		return err
	}

	// The account was found.
	account, err := GetAccount(ctx, session.logger, session.pipeline.db, session.statusRegistry, uuid.FromStringOrNil(userId))
	if err != nil {
		return err
	}

	// Do email authentication
	userId, _, _, err = AuthenticateEmail(ctx, session.logger, session.pipeline.db, account.Email, userPassword, "", false)
	if err != nil {
		return err
	}

	return session.BroadcasterSession(userId, "")
}
