package server

import (
	"context"
	"errors"
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

	latestRTTs := params.latencyHistory.Load().LatestRTTs()

	queryAddon := ServiceSettings().Matchmaking.QueryAddons.Create
	label, err := LobbyGameServerAllocate(ctx, NewRuntimeGoLogger(logger), nk, []string{params.GroupID.String()}, latestRTTs, settings, []string{params.RegionCode}, false, false, queryAddon)
	if err != nil {
		// Check if this is a region fallback error - for lobby create, auto-select closest
		var regionErr ErrMatchmakingNoServersInRegion
		if errors.As(err, &regionErr) && regionErr.FallbackInfo != nil {
			logger.Info("Auto-selecting closest server for lobby creation (no servers in requested region)",
				zap.String("requested_region", params.RegionCode),
				zap.String("selected_region", regionErr.FallbackInfo.ClosestRegion),
				zap.Int("latency_ms", regionErr.FallbackInfo.ClosestLatencyMs))

			// Allocate without region requirement to get the closest server
			label, err = LobbyGameServerAllocate(ctx, NewRuntimeGoLogger(logger), nk, []string{params.GroupID.String()}, latestRTTs, settings, nil, false, false, queryAddon)
		}

		if err != nil {
			if strings.Contains("bad request:", err.Error()) {
				err = NewLobbyErrorf(BadRequest, "required features not supported")
			}
			logger.Warn("Failed to allocate game server", zap.Error(err), zap.Any("settings", settings))
			return MatchID{}, err
		}
	}

	// Return the prepared session
	return label.ID, nil
}
