package service

import (
	"context"
	"fmt"
	"slices"
	"time"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/heroiclabs/nakama/v3/server"
	"go.uber.org/zap"
)

type NextMatchMetadata struct {
	MatchID   MatchID
	Role      string
	DiscordID string
}

// lobbyJoinSessionRequest is a request to join a specific existing session.
func (p *EvrPipeline) lobbyJoin(ctx context.Context, logger *zap.Logger, session *sessionEVR, lobbyParams *LobbySessionParameters, matchID MatchID) error {

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

		if !slices.Contains([]RoleIndex{AnyTeam, Moderator, SocialLobbyParticipant}, lobbyParams.Role) {
			return fmt.Errorf("invalid role for social lobby: %d", lobbyParams.Role)
		}

		if lobbyParams.Role == AnyTeam {
			lobbyParams.Role = SocialLobbyParticipant
		}
	}

	presence.RoleAlignment = lobbyParams.Role
	if err := p.LobbyJoinEntrants(server.NewRuntimeGoLogger(logger), label, presence); err != nil {
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
