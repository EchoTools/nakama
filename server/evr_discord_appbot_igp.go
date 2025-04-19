package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

func (d *DiscordAppBot) handleInGamePanel(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
	ctx := context.Background()
	// find the user in the streams
	presences, err := d.nk.StreamUserList(StreamModeService, userID, "", StreamLabelLoginService, false, true)
	if err != nil {
		return fmt.Errorf("failed to list igp users: %w", err)
	}

	if len(presences) == 0 {
		return errors.New("you must be in the game to use this command")
	}

	var presence runtime.Presence
	for _, p := range presences {
		if p.GetUserId() == userID {
			presence = p
			break
		}
	}

	_nk := d.nk.(*RuntimeGoNakamaModule)

	session := _nk.sessionRegistry.Get(uuid.FromStringOrNil(presence.GetSessionId()))
	if session == nil {
		return errors.New("you must be in the game to use this command")
	}

	// IGP: Potential error causing code
	params, ok := LoadParams(session.Context())
	if !ok {
		return errors.New("failed to load params")
	}

	wasGold := params.isGoldNameTag.Toggle()

	content := "You are now using the gold name tag."
	if wasGold {
		content = "You are no longer using the gold name tag."
	}
	_ = content

	components, err := d.ModPanelMessageEmbed(ctx, logger, d.nk, member.User.ID)
	if err != nil {
		if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Flags:   discordgo.MessageFlagsEphemeral,
				Content: "Failed to load mod panel: " + err.Error(),
			},
		}); err != nil {
			logger.Error("Failed to send mod panel error message", zap.Error(err))
			return nil
		}
	}

	response := &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags:   discordgo.MessageFlagsEphemeral,
			Content: "Select a player to kick",
			Components: []discordgo.MessageComponent{
				discordgo.ActionsRow{
					Components: components,
				},
			},
		},
	}
	return s.InteractionRespond(i.Interaction, response)
}
