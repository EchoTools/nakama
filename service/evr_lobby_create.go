package service

import (
	"context"
	"strings"
	"time"

	"github.com/heroiclabs/nakama/v3/server"
	"go.uber.org/zap"
)

func (p *EvrPipeline) lobbyCreate(ctx context.Context, logger *zap.Logger, session server.Session, params *LobbySessionParameters) (MatchID, error) {
	nk := p.nk

	// Do authorization checks related to the guild.
	if err := p.lobbyAuthorize(ctx, logger, session, params); err != nil {
		logger.Warn("Failed to authorize create session request", zap.Error(err))
		return MatchID{}, err
	}

	settings := &MatchSettings{
		Mode:             params.Mode,
		Level:            params.Level,
		StartTime:        time.Now().UTC(),
		SpawnedBy:        session.UserID().String(),
		GroupID:          params.GroupID,
		RequiredFeatures: params.RequiredFeatures,
		TeamAlignments:   map[string]int{session.UserID().String(): params.Role},
	}

	latestRTTs := params.latencyHistory.Load().LatestRTTs()

	queryAddon := ServiceSettings().Matchmaking.QueryAddons.Create
	label, err := LobbyGameServerAllocate(ctx, server.NewRuntimeGoLogger(logger), nk, []string{params.GroupID.String()}, latestRTTs, settings, []string{params.RegionCode}, false, false, queryAddon)
	if err != nil {
		if strings.Contains("bad request:", err.Error()) {
			err = NewLobbyErrorf(BadRequest, "required features not supported")
		}
		logger.Warn("Failed to allocate game server", zap.Error(err), zap.Any("settings", settings))
		return MatchID{}, err
	}

	// Return the prepared session
	matchID := label.ID
	
	// Post to Discord sessions channel if available
	if appBot := globalAppBot.Load(); appBot != nil && appBot.sessionsManager != nil {
		// Get the guild group for this session
		if guildGroup := appBot.guildGroupRegistry.Get(params.GroupID.String()); guildGroup != nil {
			if err := appBot.sessionsManager.PostSessionMessage(label, guildGroup); err != nil {
				logger.Warn("Failed to post session message to Discord", zap.Error(err))
			}
		}
	}
	
	return matchID, nil
}
