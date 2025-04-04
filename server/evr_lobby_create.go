package server

import (
	"context"
	"strings"
	"time"

	"go.uber.org/zap"
)

func (p *EvrPipeline) lobbyCreate(ctx context.Context, logger *zap.Logger, session Session, params *LobbySessionParameters) (MatchID, error) {
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

	latestRTTs := params.latencyHistory.LatestRTTs()

	queryAddon := ServiceSettings().Matchmaking.QueryAddons.Create
	label, err := LobbyGameServerAllocate(ctx, NewRuntimeGoLogger(logger), nk, params.GroupID.String(), latestRTTs, settings, []string{params.RegionCode}, false, false, queryAddon)
	if err != nil {
		if strings.Contains("bad request:", err.Error()) {
			err = NewLobbyErrorf(BadRequest, "required features not supported")
		}
		logger.Warn("Failed to allocate game server", zap.Error(err), zap.Any("settings", settings))
		return MatchID{}, err
	}

	// Return the prepared session
	return label.ID, nil
}
