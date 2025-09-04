package service

import (
	"database/sql"

	"github.com/heroiclabs/nakama/v3/server"
	"github.com/heroiclabs/nakama/v3/social"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protojson"
)

type PipelineDependenciesKey struct{}

type PipelineDependencies struct {
	DB                   *sql.DB
	Logger               *zap.Logger
	StartupLogger        *zap.Logger
	Config               server.Config
	SocialClient         *social.Client
	StorageIndex         server.StorageIndex
	LeaderboardCache     server.LeaderboardCache
	LeaderboardRankCache server.LeaderboardRankCache
	LeaderboardScheduler server.LeaderboardScheduler
	SessionRegistry      server.SessionRegistry
	SessionCache         server.SessionCache
	StatusRegistry       server.StatusRegistry
	MatchRegistry        server.MatchRegistry
	Matchmaker           *atomic.Pointer[server.LocalMatchmaker]
	PartyRegistry        server.PartyRegistry
	Tracker              server.Tracker
	Router               server.MessageRouter
	StreamManager        server.StreamManager
	Metrics              server.Metrics
	ProtojsonMarshaler   *protojson.MarshalOptions
	ProtojsonUnmarshaler *protojson.UnmarshalOptions
	Version              string
}

func (p *PipelineDependencies) SetMatchmaker(matchmaker *server.LocalMatchmaker) {
	p.Matchmaker.Store(matchmaker)
}

func (p *PipelineDependencies) GetMatchmaker() server.Matchmaker {
	if mm := p.Matchmaker.Load(); mm != nil {
		return mm
	}
	return nil
}
