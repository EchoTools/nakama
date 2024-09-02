package server

import (
	"context"
	"errors"
	"fmt"

	"go.uber.org/zap"
)

func (p *EvrPipeline) loadNextMatchFromDB(ctx context.Context, logger *zap.Logger, session *sessionWS) (MatchID, error) {

	config, err := LoadMatchmakingSettings(ctx, p.runtimeModule, session.UserID().String())
	if err != nil {
		return MatchID{}, fmt.Errorf("failed to load matchmaking config: %w", err)
	}

	if config.NextMatchID.IsNil() {
		return MatchID{}, nil
	}

	defer func() {
		config.NextMatchID = MatchID{}
		err = StoreMatchmakingSettings(ctx, p.runtimeModule, session.UserID().String(), config)
		if err != nil {
			logger.Warn("Failed to clear matchmaking config", zap.Error(err))
		}
	}()

	return config.NextMatchID, nil
}

// lobbyJoinSessionRequest is a request to join a specific existing session.
func (p *EvrPipeline) lobbyJoin(ctx context.Context, logger *zap.Logger, session *sessionWS, params SessionParameters) error {

	matchID, _ := NewMatchID(params.CurrentMatchID.UUID, p.node)

	label, err := MatchLabelByID(ctx, p.runtimeModule, matchID)
	if err != nil {
		return errors.Join(NewLobbyErrorf(InternalError, "failed to load match label"), err)
	} else if label == nil {
		logger.Warn("Match not found", zap.String("mid", matchID.UUID.String()))
		return ErrMatchNotFound
	}

	// Do authorization checks related to the lobby's guild.
	if err := p.authorizeGuildGroupSession(ctx, session, label.GetGroupID().String()); err != nil {
		return err
	}

	presences, err := EntrantPresencesFromSessionIDs(logger, p.sessionRegistry, params.PartyID, params.GroupID, nil, params.Role, session.id)
	if err != nil {
		return errors.Join(NewLobbyErrorf(InternalError, "failed to create presences"), err)
	}

	if err := p.LobbyJoinEntrants(ctx, logger, matchID, params.Role, presences); err != nil {
		return err
	}

	return nil
}
