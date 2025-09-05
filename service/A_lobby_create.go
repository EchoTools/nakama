package service

import (
	"context"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server"
	"go.uber.org/zap"
)

func (p *Pipeline) lobbyCreate(ctx context.Context, logger *zap.Logger, session server.Session, params *LobbySessionParameters) (MatchID, error) {
	nk := p.nk

	// Do authorization checks related to the guild.
	if err := p.lobbyAuthorize(ctx, logger, session, params); err != nil {
		logger.Warn("Failed to authorize create session request", zap.Error(err))
		return MatchID{}, err
	}

	settings := &LobbySessionSettings{
		Mode:             params.Mode,
		Level:            params.Level,
		ScheduledTime:    time.Now().UTC(),
		CreatorID:        session.UserID(),
		GroupID:          params.GroupID,
		RequiredFeatures: params.RequiredFeatures,
		TeamAlignments:   map[uuid.UUID]RoleIndex{session.UserID(): params.Role},
	}

	latestRTTs := params.latencyHistory.Load().LatestRTTs()

	queryAddon := ServiceSettings().Matchmaking.QueryAddons.Create
	label, err := LobbyGameServerAllocate(ctx, server.NewRuntimeGoLogger(logger), nk, []string{params.GroupID.String()}, latestRTTs, settings, []string{params.RegionCode}, false, false, queryAddon)
	if err != nil {
		if strings.Contains(err.Error(), "bad request:") {
			err = NewLobbyErrorf(BadRequest, "required features not supported")
		}
		logger.Warn("Failed to allocate game server", zap.Error(err), zap.Any("settings", settings))
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
