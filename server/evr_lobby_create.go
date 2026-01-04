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

	// If this is a private match being created from a social lobby, track the origin
	if (params.Mode == evr.ModeArenaPrivate || params.Mode == evr.ModeCombatPrivate) && !params.CurrentMatchID.IsNil() {
		// Check if the current match is a social lobby
		if currentLabel, err := MatchLabelByID(ctx, nk, params.CurrentMatchID); err == nil {
			if currentLabel.Mode == evr.ModeSocialPublic || currentLabel.Mode == evr.ModeSocialPrivate {
				// This private match originated from a social lobby
				originID := params.CurrentMatchID.UUID
				settings.OriginSocialID = &originID
				logger.Info("Private match originated from social lobby", 
					zap.String("origin_social_id", originID.String()),
					zap.String("new_mode", params.Mode.String()))
			}
		}
	}

	latestRTTs := params.latencyHistory.Load().LatestRTTs()

	queryAddon := ServiceSettings().Matchmaking.QueryAddons.Create
	label, err := LobbyGameServerAllocate(ctx, NewRuntimeGoLogger(logger), nk, []string{params.GroupID.String()}, latestRTTs, settings, []string{params.RegionCode}, false, false, queryAddon)
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
