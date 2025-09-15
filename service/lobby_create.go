package service

import (
	"context"
	"strings"
	"time"

	"github.com/heroiclabs/nakama/v3/server"
	"go.uber.org/zap"
)

func (p *Pipeline) lobbyCreate(ctx context.Context, logger *zap.Logger, session server.Session, params *LobbySessionParameters) (MatchID, error) {
	// Do authorization checks related to the guild.
	if err := p.lobbyAuthorize(ctx, logger, session, params); err != nil {
		logger.Warn("Failed to authorize create session request", zap.Error(err))
		return MatchID{}, err
	}

	latestRTTs := params.latencyHistory.Load().LatestRTTs()
	queryAddon := ServiceSettings().Matchmaking.QueryAddons.Create
	mode := Mode(params.Mode.String())
	level := Level(params.Level.String())
	userID := session.UserID().String()
	groupID := params.GroupID.String()
	teamSize := ValidGameSettings.DefaultTeamSize(mode)
	latencies := RuntimeLatenciesFromLatencyHistory(latestRTTs, userID)
	expiry := time.Now().UTC()
	queryData := LobbyAllocationMetadata{
		// Prepare the session for the match.
		SessionSettings: LobbySessionSettings{
			Mode:             mode,
			Level:            level,
			TeamSize:         teamSize,
			MatchExpiry:      expiry,
			CreatorID:        userID,
			OwnerID:          userID,
			GroupID:          groupID,
			RequiredFeatures: params.RequiredFeatures,
			TeamAlignments:   map[string]RoleIndex{session.UserID().String(): params.Role},
		},
		AllocatorGroupIDs:    []string{params.GroupID.String()},
		PreferredRegionCodes: []string{params.RegionCode},
		QueryAddon:           queryAddon,
		Latencies:            latencies,
	}

	label, err := LobbyGameServerAllocate(ctx, server.NewRuntimeGoLogger(logger), p.nk, queryData)
	if err != nil {
		if strings.Contains(err.Error(), "bad request:") {
			err = NewLobbyErrorf(BadRequest, "required features not supported")
		}
		logger.Warn("Failed to allocate game server", zap.Error(err), zap.Any("query_data", queryData))
		return MatchID{}, err
	}

	// Return the prepared session
	matchID := label.ID

	// Get the guild group for this session
	if guildGroup := p.appBot.guildGroupRegistry.Get(params.GroupID.String()); guildGroup != nil {
		if err := p.appBot.SessionsChannelManager.PostSessionMessage(label, guildGroup); err != nil {
			logger.Warn("Failed to post session message to Discord", zap.Error(err))
		}
	}

	return matchID, nil
}
