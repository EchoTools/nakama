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

	lobbyParams.GroupID = label.GetGroupID()
	lobbyParams.Mode = label.Mode

	// SEC-5: re-validate the client-claimed moderator role against the guild
	// that actually owns this lobby.
	//
	// NewLobbyParametersFromRequest validated it against the group ID on the
	// request, but a LobbyJoinSessionRequest carries no group ID — the check
	// there fell back to the user's *active* guild. The lobby being joined can
	// belong to a different guild, so an enforcer of guild A could otherwise
	// enter a guild-B lobby as TeamModerator (which exempts them from the
	// player count, the moderator slot pool, early-quit tracking and the
	// post-match social transition). Fails closed: no session parameters means
	// no verified moderator.
	//
	// IsModerator is re-scoped in step with GroupID and Mode just above: from
	// here down, every guild-derived field on lobbyParams describes the lobby
	// being joined rather than the request that asked for it.
	sessionParams, _ := LoadParams(ctx)
	lobbyParams.IsModerator = sessionParams != nil &&
		isModeratorOfGroup(sessionParams.isGlobalOperator, sessionParams.guildGroups, lobbyParams.GroupID, lobbyParams.UserID.String())

	if lobbyParams.Role == evr.TeamModerator && !lobbyParams.IsModerator {
		logger.Warn("Downgrading unverified moderator role claim",
			zap.String("uid", lobbyParams.UserID.String()),
			zap.String("gid", lobbyParams.GroupID.String()),
			zap.String("mid", matchID.UUID.String()))
		p.nk.MetricsCounterAdd("lobby_moderator_role_downgraded", map[string]string{"group_id": lobbyParams.GroupID.String()}, 1)
		lobbyParams.Role = evr.TeamUnassigned
	}

	// Do authorization checks related to the lobby's guild.
	if err := p.lobbyAuthorize(ctx, logger, session, lobbyParams); err != nil {
		return err
	}

	presence, err := EntrantPresenceFromSession(session, lobbyParams.PartyID, lobbyParams.Role, lobbyParams.GetRating(), label.GetGroupID().String(), 0, "")
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
		// Send the error to the client after a short delay (clients may spam join requests).
		// Tied to session context so the goroutine exits immediately on disconnect.
		go func() {
			select {
			case <-time.After(3 * time.Second):
			case <-session.Context().Done():
				return
			}
			if err := SendEVRMessages(session, false, LobbySessionFailureFromError(label.Mode, label.GetGroupID(), err)); err != nil {
				logger.Debug("Failed to send error message", zap.Error(err))
			}
		}()
		return fmt.Errorf("lobbyJoin: %w", err)
	}
	return nil
}
