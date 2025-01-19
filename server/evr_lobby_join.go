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

	params, ok := LoadParams(session.Context())
	if !ok {
		return fmt.Errorf("failed to get session parameters")
	}

	label, err := MatchLabelByID(ctx, p.runtimeModule, matchID)
	if err != nil {
		return fmt.Errorf("failed to load match label: %w", err)
	} else if label == nil {
		logger.Warn("Match not found", zap.String("mid", matchID.UUID.String()))
		return ErrMatchNotFound
	}

	lobbyParams.GroupID = label.GetGroupID()
	lobbyParams.Mode = label.Mode

	groupID := label.GetGroupID().String()

	// Do authorization checks related to the lobby's guild.
	if err := p.lobbyAuthorize(ctx, logger, session, lobbyParams); err != nil {
		return err
	}

	// Generate a profile for this group
	profile, err := NewUserServerProfile(ctx, p.db, p.runtimeModule, params.account, params.xpID, groupID, []evr.Symbol{label.Mode}, label.Mode)
	if err != nil {
		return fmt.Errorf("failed to create user server profile: %w", err)
	}

	if _, err := p.profileCache.Store(session.ID(), *profile); err != nil {
		return fmt.Errorf("failed to cache profile: %w", err)
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
