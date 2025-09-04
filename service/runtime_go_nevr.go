package service

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server"
	"github.com/heroiclabs/nakama/v3/social"
	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protojson"
)

var _ = runtime.NakamaModule(RuntimeGoNEVRModule{})

type RuntimeGoNEVRModule struct {
	*server.RuntimeGoNakamaModule
	zapLogger            *zap.Logger
	logger               *zap.Logger
	db                   *sql.DB
	protojsonMarshaler   *protojson.MarshalOptions
	config               server.Config
	socialClient         *social.Client
	leaderboardCache     server.LeaderboardCache
	leaderboardRankCache server.LeaderboardRankCache
	leaderboardScheduler server.LeaderboardScheduler
	sessionRegistry      server.SessionRegistry
	sessionCache         server.SessionCache
	statusRegistry       server.StatusRegistry
	matchRegistry        server.MatchRegistry
	partyRegistry        server.PartyRegistry
	tracker              server.Tracker
	metrics              server.Metrics
	streamManager        server.StreamManager
	router               server.MessageRouter
	eventFn              server.RuntimeEventCustomFunction
	node                 string
	matchCreateFn        server.RuntimeMatchCreateFunction
	satori               runtime.Satori
	fleetManager         runtime.FleetManager
	storageIndex         server.StorageIndex
}

func NewRuntimeGoNEVRModule(logger *zap.Logger, db *sql.DB, protojsonMarshaler *protojson.MarshalOptions, config server.Config, socialClient *social.Client, leaderboardCache server.LeaderboardCache, leaderboardRankCache server.LeaderboardRankCache, leaderboardScheduler server.LeaderboardScheduler, sessionRegistry server.SessionRegistry, sessionCache server.SessionCache, statusRegistry server.StatusRegistry, matchRegistry server.MatchRegistry, partyRegistry server.PartyRegistry, tracker server.Tracker, metrics server.Metrics, streamManager server.StreamManager, router server.MessageRouter, storageIndex server.StorageIndex, satoriClient runtime.Satori) *RuntimeGoNEVRModule {
	return &RuntimeGoNEVRModule{
		logger:               logger,
		db:                   db,
		protojsonMarshaler:   protojsonMarshaler,
		config:               config,
		socialClient:         socialClient,
		leaderboardCache:     leaderboardCache,
		leaderboardRankCache: leaderboardRankCache,
		leaderboardScheduler: leaderboardScheduler,
		sessionRegistry:      sessionRegistry,
		sessionCache:         sessionCache,
		statusRegistry:       statusRegistry,
		matchRegistry:        matchRegistry,
		partyRegistry:        partyRegistry,
		tracker:              tracker,
		metrics:              metrics,
		streamManager:        streamManager,
		router:               router,
		storageIndex:         storageIndex,

		node: config.GetName(),

		satori: satoriClient,
	}
}

func (n *RuntimeGoNEVRModule) Logger() *zap.Logger {
	return n.zapLogger
}

// LobbyGet returns the MatchLabel for a given match ID
func (n *RuntimeGoNEVRModule) LobbyGet(ctx context.Context, matchID string) (*MatchLabel, error) {
	match, err := n.MatchGet(ctx, matchID)
	if err != nil {
		return nil, err
	} else if match == nil {
		return nil, ErrMatchNotFound
	}

	label := MatchLabel{}
	if err = json.Unmarshal([]byte(match.GetLabel().GetValue()), &label); err != nil {
		return nil, err
	}
	if label.GroupID == nil {
		label.GroupID = &uuid.Nil
	}
	return &label, nil
}

// MatchLabelList returns a list of MatchLabels based on the provided parameters
func (n *RuntimeGoNEVRModule) MatchLabelList(ctx context.Context, limit int, minSize, maxSize int, query string) ([]*MatchLabel, error) {
	var minSizePtr, maxSizePtr *int
	if minSize > 0 {
		minSizePtr = &minSize
	}
	if maxSize > 0 {
		maxSizePtr = &maxSize
	}
	match, err := n.MatchList(ctx, limit, true, "", minSizePtr, maxSizePtr, query)
	if err != nil {
		return nil, err
	} else if match == nil {
		return nil, ErrMatchNotFound
	}

	var labels []*MatchLabel

	for _, m := range match {
		label := MatchLabel{}
		if err = json.Unmarshal([]byte(m.GetLabel().GetValue()), &label); err != nil {
			return nil, err
		}
		if label.GroupID == nil {
			label.GroupID = &uuid.Nil
		}
		labels = append(labels, &label)
	}

	return labels, nil
}

func (n *RuntimeGoNEVRModule) StorableRead(ctx context.Context, userID string, dst StorableAdapter, create bool) error {
	return StorableRead(ctx, n.zapLogger, n.db, n.storageIndex, n.metrics, userID, dst, create)
}

func (n *RuntimeGoNEVRModule) StorableWrite(ctx context.Context, userID string, src StorableAdapter) error {
	return StorableWrite(ctx, n.zapLogger, n.db, n.storageIndex, n.metrics, userID, src)
}
