package server

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

type NextMatchMetadata struct {
	MatchID   MatchID
	Role      string
	DiscordID string
}

// lobbyJoinSessionRequest is a request to join a specific existing session.
func (p *EvrPipeline) lobbyJoin(ctx context.Context, logger *zap.Logger, session *sessionWS, lobbyParams *LobbySessionParameters, matchID MatchID) error {

	label, err := MatchLabelByID(ctx, p.nk, matchID)
	if err != nil {
		return fmt.Errorf("failed to load match label: %w", err)
	} else if label == nil {
		logger.Warn("Match not found", zap.String("mid", matchID.UUID.String()))
		return ErrMatchNotFound
	}
	if label.Mode != evr.ModeArenaPublic && label.Mode != evr.ModeCombatPublic {
		LeavePartyStream(session)
	}

	lobbyParams.GroupID = label.GetGroupID()
	lobbyParams.Mode = label.Mode

	// Check if this is a secondary connection and prevent social lobby access
	sessionCount := p.sessionRegistry.CountSessionsForUser(session.UserID())
	if sessionCount > 1 && (label.Mode == evr.ModeSocialPublic || label.Mode == evr.ModeSocialPrivate) {
		logger.Warn("Secondary connection attempted to join social lobby",
			zap.String("user_id", session.UserID().String()),
			zap.String("session_id", session.ID().String()),
			zap.String("mode", label.Mode.String()))
		return NewLobbyError(SecondaryConnectionRestricted, "secondary connections cannot access social lobbies")
	}

	// Do authorization checks related to the lobby's guild.
	if err := p.lobbyAuthorize(ctx, logger, session, lobbyParams); err != nil {
		return err
	}

	presence, err := EntrantPresenceFromSession(session, lobbyParams.PartyID, lobbyParams.Role, lobbyParams.GetRating(), lobbyParams.GetRankPercentile(), label.GetGroupID().String(), 0, "")
	if err != nil {
		return fmt.Errorf("failed to create presences: %w", err)
	}

	switch label.Mode {
	case evr.ModeSocialPublic, evr.ModeSocialPrivate:

		if !slices.Contains([]int{evr.TeamUnassigned, evr.TeamModerator, evr.TeamSocial}, lobbyParams.Role) {
			return fmt.Errorf("invalid role for social lobby: %d", lobbyParams.Role)
		}

		if lobbyParams.Role == evr.TeamUnassigned {
			lobbyParams.Role = evr.TeamSocial
		}
	}

	presence.RoleAlignment = lobbyParams.Role
	if err := p.LobbyJoinEntrants(logger, label, presence); err != nil {
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
