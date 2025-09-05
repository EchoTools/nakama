package service

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/heroiclabs/nakama-common/runtime"
	"go.mongodb.org/mongo-driver/mongo"
	"go.uber.org/zap"
)

type NEVRLobby struct{}

func InitModule(ctx context.Context, runtimeLogger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) error {
	vars := ctx.Value(runtime.RUNTIME_CTX_ENV).(map[string]string)
	d := ctx.Value(PipelineDependenciesKey{}).(*PipelineDependencies)
	logger := d.Logger

	if _, err := ServiceSettingsLoad(ctx, nk); err != nil {
		runtimeLogger.WithField("err", err).Error("Failed to load global settings")
		panic(err)
	}

	InitializeHooks(initializer)

	// Create the core groups
	if err := CreateCoreGroups(ctx, db, nk); err != nil {
		return fmt.Errorf("unable to create core groups: %w", err)
	}

	// Register the "matchmaking" handler
	if err := initializer.RegisterMatch(EVRLobbySessionMatchModule, func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) (runtime.Match, error) {
		return &NEVRMatch{}, nil
	}); err != nil {
		return err
	}

	userRemoteLogJournalRegistry := NewUserRemoteLogJournalRegistry(ctx, d.Logger, nk, d.SessionRegistry)

	var (
		dg  *discordgo.Session
		err error
	)
	// Initialize the discord bot if the token is set
	if appBotToken, ok := vars["DISCORD_BOT_TOKEN"]; !ok || appBotToken == "" {
		return fmt.Errorf("DISCORD_BOT_TOKEN is not set")
	} else {
		if dg, err = discordgo.New("Bot " + appBotToken); err != nil {
			return fmt.Errorf("unable to create discord bot: %w", err)
		}
		dg.StateEnabled = true
	}
	discordBotID := ServiceSettings().DiscordBotUserID
	guildGroupRegistry := NewGuildGroupRegistry(ctx, runtimeLogger, nk, db, discordBotID)

	var statisticsQueue *StatisticsQueue
	var vrmlScanQueue *VRMLScanQueue
	if vars["VRML_REDIS_URI"] == "" || vars["VRML_OAUTH_REDIRECT_URL"] == "" || vars["VRML_OAUTH_CLIENT_ID"] == "" {
		logger.Warn("VRML OAuth configuration is not set, VRML verification will not be available.")
	} else {
		redisURI := vars["VRML_REDIS_URI"]
		vrmlOAuthRedirectURL := vars["VRML_OAUTH_REDIRECT_URL"]
		vrmlOAuthClientID := vars["VRML_OAUTH_CLIENT_ID"]
		redisClient, err := ConnectRedis(ctx, redisURI)
		if err != nil {
			return fmt.Errorf("failed to connect to Redis for VRML scan queue: %w", err)
		}
		vrmlScanQueue, err = NewVRMLScanQueue(ctx, runtimeLogger, db, nk, initializer, dg, redisClient, vrmlOAuthRedirectURL, vrmlOAuthClientID)
		if err != nil {
			return fmt.Errorf("failed to create VRML scan queue: %w", err)
		}
	}

	if vars["REDIS_URI"] == "" {
		return fmt.Errorf("REDIS_URI is not set")
	}
	redisClient, err := ConnectRedis(ctx, vars["REDIS_URI"])
	if err != nil {
		d.Logger.Fatal("Failed to connect to Redis", zap.Error(err))
	}

	// TODO: Move component creation to main.go
	var ipInfoCache *IPInfoCache
	if redisClient != nil {
		var providers []IPInfoProvider
		if vars["IPAPI_API_KEY"] != "" {
			ipqsClient, err := NewIPQSClient(logger, d.Metrics, redisClient, vars["IPQS_API_KEY"])
			if err != nil {
				logger.Fatal("Failed to create IPQS client", zap.Error(err))
			}
			providers = append(providers, ipqsClient)
		}

		ipapiClient, err := NewIPAPIClient(logger, d.Metrics, redisClient)
		if err != nil {
			logger.Fatal("Failed to create IPAPI client", zap.Error(err))
		}
		providers = append(providers, ipapiClient)

		ipInfoCache, err = NewIPInfoCache(logger, d.Metrics, providers...)
		if err != nil {
			logger.Fatal("Failed to create IP info cache", zap.Error(err))
		}
	}

	// Initialize MongoDB client for match summarization
	var mongoClient *mongo.Client
	if mongoURI := vars["MONGODB_URI"]; mongoURI != "" {
		if mongoClient, err = ConnectMongoDB(ctx, mongoURI); err != nil {
			logger.Warn("Failed to connect to MongoDB, match summarization will not be available", zap.Error(err))
		}
	} else {
		logger.Info("MongoDB URI not configured, match summarization will not be available")
	}

	// Register the event dispatch
	eventDispatch, err := NewEventDispatch(ctx, runtimeLogger, db, nk, d.SessionRegistry, d.MatchRegistry, mongoClient, redisClient, dg, statisticsQueue, vrmlScanQueue)
	if err != nil {
		return fmt.Errorf("unable to create event dispatch: %w", err)
	}

	// Register the event handler
	if err := initializer.RegisterEvent(eventDispatch.EventFn); err != nil {
		return err
	}

	// TODO: Move component creation to main.go
	discordIntegrator := NewDiscordIntegrator(ctx, logger, d.Config, d.Metrics, nk, db, dg, guildGroupRegistry)
	discordIntegrator.Start()

	// TODO: Move component creation to main.go
	appBot, err := NewDiscordAppBot(ctx, runtimeLogger, nk, db, d.Metrics, d.Config, discordIntegrator, d.StatusRegistry, d.SessionRegistry, dg, ipInfoCache, guildGroupRegistry)
	if err != nil {
		logger.Error("Failed to create app bot", zap.Error(err))

	}
	// Store the app bot in a global variable for later use
	GlobalAppBot.Store(appBot) // FIXME: TODO: This is a temporary solution, we should refactor this to avoid global state

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

	// Register HTTP Handler for the game API service
	evrPipeline := NewEvrPipeline(
		d.Logger,
		d.StartupLogger,
		db,
		nk,
		d.ProtojsonMarshaler,
		d.ProtojsonUnmarshaler,
		d.Config,
		d.Version,
		d.SocialClient,
		d.StorageIndex,
		d.LeaderboardScheduler,
		d.LeaderboardCache,
		d.LeaderboardRankCache,
		d.SessionRegistry,
		d.SessionCache,
		d.StatusRegistry,
		d.MatchRegistry,
		d.PartyRegistry,
		d.Matchmaker,
		d.Tracker,
		d.Router,
		d.StreamManager,
		d.Metrics,
		discordIntegrator,
		appBot,
		userRemoteLogJournalRegistry,
		guildGroupRegistry,
		ipInfoCache,
	)
	if err := initializer.RegisterHttp("/evr", NewSocketWSEVRAcceptor(d.Logger, d.Config, d.SessionRegistry, d.SessionCache, d.StatusRegistry, d.Matchmaker, d.Tracker, d.Metrics, d.ProtojsonMarshaler, d.ProtojsonUnmarshaler, evrPipeline), http.MethodGet); err != nil {
		return fmt.Errorf("unable to register /evr/api service: %w", err)
	}

	if err := initializer.RegisterHttp("/api/v3/{id:.*}", AppAcceptorProxyFn, http.MethodGet, http.MethodPost); err != nil {
		return fmt.Errorf("unable to register /evr/api service: %w", err)
	}

	// Register the matchmaking override
	if err := initializer.RegisterMatchmakerOverride(NewSkillBasedMatchmaker().EvrMatchmakerFn); err != nil {
		return fmt.Errorf("unable to register matchmaker override: %w", err)
	}

	lobbyBuilder := NewLobbyBuilder(runtimeLogger, nk, d.SessionRegistry, d.MatchRegistry, d.Tracker, d.Metrics)

	if err := initializer.RegisterMatchmakerMatched(lobbyBuilder.MatchmakerMatchedFn); err != nil {
		return fmt.Errorf("unable to register matchmaker matched override: %w", err)
	}

	vrmlScanQueue.Start()

	// Migrate any system level data
	go MigrateSystem(ctx, runtimeLogger, db, nk)

	// Start the service settings loop
	go ServiceSettingsLoop(ctx, runtimeLogger, nk)

	// Update the metrics with match data
	go func() {
		<-time.After(15 * time.Second)
		MetricsUpdateLoop(ctx, runtimeLogger, nk, db, d.MatchRegistry)
	}()
	return nil
}
