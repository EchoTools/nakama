package backend

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"

	"github.com/echotools/nakama/v3/service"
	"github.com/go-redis/redis"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.mongodb.org/mongo-driver/mongo"
	"go.uber.org/zap"
)

var SystemUserID = "00000000-0000-0000-0000-000000000000"

type NEVRLobby struct{}

func InitModule(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) error {

	InitializeHooks(initializer)

	// Register the "matchmaking" handler
	if err := initializer.RegisterMatch(service.EVRLobbySessionMatchModule, func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) (runtime.Match, error) {
		return &NEVRMatch{}, nil
	}); err != nil {
		return err
	}
	// Register HTTP Handler for the evr/api service

	if err := initializer.RegisterHttp("/api/v3/{id:.*}", service.AppAcceptorProxyFn, http.MethodGet, http.MethodPost); err != nil {
		return fmt.Errorf("unable to register /evr/api service: %w", err)
	}

	// Register the matchmaking override
	if err := initializer.RegisterMatchmakerOverride(service.NewSkillBasedMatchmaker().EvrMatchmakerFn); err != nil {
		return fmt.Errorf("unable to register matchmaker override: %w", err)
	}

	// Initialize MongoDB client for match summarization
	var mongoClient *mongo.Client
	if mongoURI := vars["MONGODB_URI"]; mongoURI != "" {
		if mongoClient, err = connectMongoDB(ctx, mongoURI); err != nil {
			logger.Warn("Failed to connect to MongoDB, match summarization will not be available", zap.Error(err))
		}
	} else {
		logger.Info("MongoDB URI not configured, match summarization will not be available")
	}
	// Register the event dispatch
	eventDispatch, err := NewEventDispatch(ctx, runtimeLogger, db, nk, sessionRegistry, matchRegistry, mongoClient, redisClient, dg, statisticsQueue, vrmlScanQueue)
	if err != nil {
		return nil, fmt.Errorf("unable to create event dispatch: %w", err)
	}
	// Initialize the VRML scan queue if the configuration is set
	var redisClient *redis.Client
	var vrmlScanQueue *vrml.VRMLScanQueue
	if vars["VRML_REDIS_URI"] == "" || vars["VRML_OAUTH_REDIRECT_URL"] == "" || vars["VRML_OAUTH_CLIENT_ID"] == "" {
		logger.Warn("VRML OAuth configuration is not set, VRML verification will not be available.")
	} else {
		redisURI := vars["VRML_REDIS_URI"]
		vrmlOAuthRedirectURL := vars["VRML_OAUTH_REDIRECT_URL"]
		vrmlOAuthClientID := vars["VRML_OAUTH_CLIENT_ID"]
		redisClient, err = connectRedis(ctx, redisURI)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to Redis for VRML scan queue: %w", err)
		}
		if vrmlScanQueue, err = NewVRMLScanQueue(ctx, runtimeLogger, db, nk, dg, redisClient, vrmlOAuthRedirectURL, vrmlOAuthClientID); err != nil {
			return nil, fmt.Errorf("failed to create VRML scan queue: %w", err)
		}
	}

	// Register the event handler
	if err := initializer.RegisterEvent(service.EventHandlerFn); err != nil {
		return err
	}
	return nil
}
