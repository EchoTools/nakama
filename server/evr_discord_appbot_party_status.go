package server

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/thriftrw/ptr"
	"go.uber.org/zap"
)

func (d *DiscordAppBot) handlePartyStatus(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, _ *discordgo.User, _ *discordgo.Member, userID string, _ string) error {

	nk := d.nk
	// Check if this user is online and currently in a party.
	groupName, partyUUID, err := GetLobbyGroupID(ctx, d.db, userID)
	if err != nil {
		return fmt.Errorf("failed to get party group ID: %w", err)
	}

	if groupName == "" {
		return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Flags:   discordgo.MessageFlagsEphemeral,
				Content: "You do not have a party group set. use `/party group` to set one.",
			},
		})
	}

	go func() {
		// "Close" the embed on return
		defer func() {
			if _, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
				Content: ptr.String("Party Status (Expired)"),
				Embeds: &[]*discordgo.MessageEmbed{{
					Title: "Party has been empty for more than 2 minutes.",
				}},
			}); err != nil {
				logger.Error("Failed to edit interaction response", zap.Error(err))
			}
		}()

		embeds := make([]*discordgo.MessageEmbed, 4)
		// Create all four embeds for the party members.
		for i := range embeds {
			embeds[i] = &discordgo.MessageEmbed{
				Title: "*Empty Slot*",
			}
		}

		partyStr := partyUUID.String()

		var message *discordgo.Message
		var err error
		var members []runtime.Presence
		var lastDiscordIDs []string
		var memberCache = make(map[string]*discordgo.Member)

		updateInterval := 3 * time.Second
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		emptyTimer := time.NewTimer(120 * time.Second)
		defer emptyTimer.Stop()

		// Loop until the party is empty for 2 minutes.
		for {

			select {
			case <-emptyTimer.C:

				// Check if the party is still empty.
				if len(members) > 0 {
					emptyTimer.Reset(120 * time.Second)
					continue
				}
				return

			case <-ticker.C:
				ticker.Reset(updateInterval)
			}

			// List the party members.
			members, err = nk.StreamUserList(StreamModeParty, partyStr, "", d.pipeline.node, false, true)
			if err != nil {
				logger.Error("Failed to list stream users", zap.Error(err))
				return
			}

			emptyState := &LobbySessionParameters{}

			matchmakingStates := make(map[string]*LobbySessionParameters, len(members)) // If they are matchmaking
			for _, p := range members {
				matchmakingStates[p.GetUserId()] = emptyState
			}

			discordIDs := make([]string, 0, len(members))
			for _, p := range members {
				discordIDs = append(discordIDs, d.cache.UserIDToDiscordID(p.GetUserId()))
			}

			slices.Sort(discordIDs)

			groupID := d.cache.GuildIDToGroupID(i.GuildID)

			// List who is matchmaking
			currentlyMatchmaking, err := nk.StreamUserList(StreamModeMatchmaking, groupID, "", "", false, true)
			if err != nil {
				logger.Error("Failed to list stream users", zap.Error(err))
				return
			}

			idleColor := 0x0000FF // no one is matchmaking

			for _, p := range currentlyMatchmaking {
				if _, ok := matchmakingStates[p.GetUserId()]; ok {

					// Unmarshal the user status
					if err := json.Unmarshal([]byte(p.GetStatus()), matchmakingStates[p.GetUserId()]); err != nil {
						logger.Error("Failed to unmarshal user status", zap.Error(err))
						continue
					}

					idleColor = 0xFF0000 // Someone is matchmaking
				}
			}

			// Update the embeds with the current party members.
			for j := range embeds {
				if j < len(discordIDs) {

					// Set the display name
					var member *discordgo.Member
					var ok bool

					if member, ok = memberCache[discordIDs[j]]; !ok {
						if member, err = d.cache.GuildMember(i.GuildID, discordIDs[j]); err != nil {
							logger.Error("Failed to get guild member", zap.Error(err))
							embeds[j].Title = "*Unknown User*"
							continue
						} else if member != nil {
							memberCache[discordIDs[j]] = member
						} else {
							embeds[j].Title = "*Unknown User*"
							continue
						}
					}
					displayName := member.DisplayName()
					if displayName == "" {
						displayName = member.User.Username
					}
					embeds[j].Title = displayName
					embeds[j].Thumbnail = &discordgo.MessageEmbedThumbnail{
						URL: member.User.AvatarURL(""),
					}
					userID := d.cache.DiscordIDToUserID(member.User.ID)
					if state, ok := matchmakingStates[userID]; ok && state != emptyState {

						embeds[j].Color = 0x00FF00
						embeds[j].Description = "Matchmaking"
					} else {

						embeds[j].Color = idleColor
						embeds[j].Description = "In Party"
					}
				} else {
					embeds[j].Title = "*Empty Slot*"
					embeds[j].Color = 0xCCCCCC
				}
			}

			if message == nil {

				// Send the initial message
				if err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Flags:  discordgo.MessageFlagsEphemeral,
						Embeds: embeds,
					},
				}); err != nil {
					logger.Error("Failed to send interaction response", zap.Error(err))
					return
				}

			} else if slices.Equal(discordIDs, lastDiscordIDs) {
				// No changes, skip the update.
				continue
			}

			lastDiscordIDs = discordIDs

			// Edit the message with the updated party members.
			if message, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
				Content: ptr.String("Party Status"),
				Embeds:  &embeds,
			}); err != nil {
				logger.Error("Failed to edit interaction response", zap.Error(err))
				return
			}

		}
	}()
	return nil
}
