package server

import (
	"context"
	"fmt"

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

		if role := socialLobbyEntrantRole(lobbyParams.Role); role != lobbyParams.Role {
			logger.Debug("Social lobby entrant role set to social",
				zap.String("mid", matchID.UUID.String()),
				zap.Int("requested_role", lobbyParams.Role),
				zap.Int("role", role))
			lobbyParams.Role = role
		}
	}

	presence.RoleAlignment = lobbyParams.Role
	return p.lobbyJoinEntrant(logger, session, label, presence)
}

// socialLobbyEntrantRole is the role an entrant takes in a social lobby. A social lobby has no
// teams, so a team role carries no meaning there: the game client sends whatever team it was
// started with (-lobbyteam), and a private social create from a client started with -lobbyteam 3
// arrives as TeamBlue. Every role except TeamModerator becomes TeamSocial. The moderator claim
// has already been verified against the lobby's guild by the caller.
func socialLobbyEntrantRole(role int) int {
	if role == evr.TeamModerator {
		return evr.TeamModerator
	}
	return evr.TeamSocial
}

// lobbyJoinEntrant seats presence in the match label names. A refusal is returned, not answered:
// the client is sent its one LobbySessionFailure by whoever owns the request, which for a lobby
// session request is answerLobbySessionRequestError. The find paths that join speculatively (a party
// reservation, following the leader) fall through or retry on a refusal, and their client must not be
// told its find failed while the find goes on.
func (p *EvrPipeline) lobbyJoinEntrant(logger *zap.Logger, session *sessionWS, label *MatchLabel, presence *EvrMatchPresence) error {
	if err := p.LobbyJoinEntrants(logger, label, presence); err != nil {
		return fmt.Errorf("lobbyJoin: %w", err)
	}
	return nil
}
