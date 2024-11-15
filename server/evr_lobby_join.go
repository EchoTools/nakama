package server

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
)

type NextMatchMetadata struct {
	MatchID   MatchID
	Role      string
	DiscordID string
}

// lobbyJoinSessionRequest is a request to join a specific existing session.
func (p *EvrPipeline) lobbyJoin(ctx context.Context, logger *zap.Logger, session *sessionWS, params *LobbySessionParameters, matchID MatchID) error {

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

	presences, err := EntrantPresencesFromSessionIDs(logger, p.sessionRegistry, params.PartyID, params.GroupID, params.Rating, params.Role, session.id)
	if err != nil {
		return fmt.Errorf("failed to create presences: %w", err)
	}

	presences[0].RoleAlignment = params.Role
	if err := p.LobbyJoinEntrants(logger, label, presences...); err != nil {
		// Send the error to the client
		go func() {
			// Delay sending the error message to the client.
			// There are situations where the client will spam the server with join requests.
			<-time.After(3 * time.Second)
			if err := SendEVRMessages(session, false, LobbySessionFailureFromError(label.Mode, label.GetGroupID(), err)); err != nil {
				logger.Debug("Failed to send error message", zap.Error(err))
			}
		}()
	}
	return nil
}
