package service

import (
	"fmt"
	"strings"

	nkrtapi "github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama/v3/server"
	"go.uber.org/zap"
)

func (p *EvrPipeline) processPartyMessage(logger *zap.Logger, session server.Session, in *nkrtapi.Envelope) (err error) {

	// If this is the party leader, then update the label with the party message contents.

	content := ""
	switch in.Message.(type) {
	case *nkrtapi.Envelope_Party:
		discordIDs := make([]string, 0)
		leader := in.GetParty().GetLeader()
		userIDs := make([]string, 0)

		// Put leader first
		if leader != nil {
			userIDs = append(userIDs, leader.GetUserId())
		}
		for _, m := range in.GetParty().GetPresences() {
			if m.GetUserId() == leader.GetUserId() {
				continue
			}
			userIDs = append(userIDs, m.GetUserId())
		}
		partyGroupName := ""
		var err error
		for _, userID := range userIDs {
			if partyGroupName == "" {
				partyGroupName, _, err = GetLobbyGroupID(session.Context(), p.db, userID)
				if err != nil {
					logger.Warn("Failed to get party group ID", zap.Error(err))
				}
			}
			if discordID, err := GetDiscordIDByUserID(session.Context(), p.db, userID); err != nil {
				logger.Warn("Failed to get discord ID", zap.Error(err))
				discordIDs = append(discordIDs, userID)
			} else {
				discordIDs = append(discordIDs, fmt.Sprintf("<@%s>", discordID))
			}
		}

		content = fmt.Sprintf("Active party `%s`: %s", partyGroupName, strings.Join(discordIDs, ", "))

	case *nkrtapi.Envelope_PartyLeader:
		if discordID, err := GetDiscordIDByUserID(session.Context(), p.db, in.GetPartyLeader().GetPresence().GetUserId()); err != nil {
			logger.Warn("Failed to get discord ID", zap.Error(err))
			content = fmt.Sprintf("Party leader: %s", in.GetPartyLeader().GetPresence().GetUsername())
		} else {
			content = fmt.Sprintf("New party leader: <@%s>", discordID)
		}

	case *nkrtapi.Envelope_PartyJoinRequest:

	case *nkrtapi.Envelope_PartyPresenceEvent:
		event := in.GetPartyPresenceEvent()
		joins := make([]string, 0)

		for _, join := range event.GetJoins() {
			if join.GetUserId() != session.UserID().String() {
				if discordID, err := GetDiscordIDByUserID(session.Context(), p.db, join.GetUserId()); err != nil {
					logger.Warn("Failed to get discord ID", zap.Error(err))
					joins = append(joins, join.GetUsername())
				} else {
					joins = append(joins, fmt.Sprintf("<@%s>", discordID))
				}
			}
		}
		leaves := make([]string, 0)
		for _, leave := range event.GetLeaves() {
			if discordID, err := GetDiscordIDByUserID(session.Context(), p.db, leave.GetUserId()); err != nil {
				logger.Warn("Failed to get discord ID", zap.Error(err))
				leaves = append(leaves, leave.GetUsername())
			} else {
				leaves = append(leaves, fmt.Sprintf("<@%s>", discordID))
			}
		}

		if len(joins) > 0 {
			content += fmt.Sprintf("Party join: %s\n", strings.Join(joins, ", "))
		}
		if len(leaves) > 0 {
			content += fmt.Sprintf("Party leave: %s\n", strings.Join(leaves, ", "))
		}
	}
	return nil
}
