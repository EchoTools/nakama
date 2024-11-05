package server

import (
	"context"
	"fmt"
	"time"

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
func (p *EvrPipeline) lobbyJoin(ctx context.Context, logger *zap.Logger, session *sessionWS, params *LobbySessionParameters) error {

	matchID, _ := NewMatchID(params.CurrentMatchID.UUID, p.node)

	label, err := MatchLabelByID(ctx, p.runtimeModule, matchID)
	if err != nil {
		return fmt.Errorf("failed to load match label: %w", err)
	} else if label == nil {
		logger.Warn("Match not found", zap.String("mid", matchID.UUID.String()))
		return ErrMatchNotFound
	}

	// Do authorization checks related to the lobby's guild.
	if err := p.authorizeGuildGroupSession(ctx, session, label.GetGroupID().String()); err != nil {
		return err
	}

	if time.Since(label.CreatedAt) < 10*time.Second {
		return NewLobbyError(ServerIsLocked, "Server is too new.")
	}
	presences, err := EntrantPresencesFromSessionIDs(logger, p.sessionRegistry, params.PartyID, params.GroupID, params.Rating, params.Role, session.id)
	if err != nil {
		return fmt.Errorf("failed to create presences: %w", err)
	}

	presences[0].RoleAlignment = params.Role
	if err := p.LobbyJoinEntrants(logger, label, presences); err != nil {
		// Send the error to the client
		if err := SendEVRMessages(session, LobbySessionFailureFromError(label.Mode, label.GetGroupID(), err)); err != nil {
			logger.Debug("Failed to send error message", zap.Error(err))
		}
	}
	return nil
}
