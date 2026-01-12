package server

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/echotools/vrmlgo/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

func (d *DiscordAppBot) handleUnlinkVRML(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
	nk := d.nk
	db := d.db

	logger = logger.WithFields(map[string]any{
		"admin_discord_id": user.ID,
		"admin_username":   user.Username,
		"admin_uid":        userID,
	})

	// Get the identifier from the command options
	options := i.ApplicationCommandData().Options
	if len(options) == 0 {
		return simpleInteractionResponse(s, i, "No identifier provided.")
	}
	identifier := strings.TrimSpace(options[0].StringValue())

	// Defer the initial response
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags: discordgo.MessageFlagsLoading | discordgo.MessageFlagsEphemeral,
		},
	}); err != nil {
		logger.WithField("error", err).Error("Failed to send interaction response")
		return err
	}

	// Helper to edit the response with formatted text content
	editResponseFn := func(format string, a ...any) error {
		content := fmt.Sprintf(format, a...)
		_, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &content})
		return err
	}

	// Helper to edit the response with an embed and components
	editEmbedResponseFn := func(embed *discordgo.MessageEmbed, components []discordgo.MessageComponent) error {
		_, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Embeds:     &[]*discordgo.MessageEmbed{embed},
			Components: &components,
		})
		return err
	}

	// Find the target user and their VRML link data
	var targetUserID string
	var targetDiscordID string
	var vrmlUserID string
	var vrmlSummary *VRMLPlayerSummary

	// Try to identify the target by different methods
	// 1. Check if it's a Discord ID (numeric string or <@ID> mention format)
	if strings.HasPrefix(identifier, "<@") && strings.HasSuffix(identifier, ">") {
		// Extract Discord ID from mention
		targetDiscordID = strings.Trim(identifier, "<@!>")
	} else if _, err := strconv.ParseUint(identifier, 10, 64); err == nil && len(identifier) >= 17 {
		// Looks like a Discord ID (17+ digits)
		targetDiscordID = identifier
	}

	if targetDiscordID != "" {
		// Look up by Discord ID
		targetUserID = d.cache.DiscordIDToUserID(targetDiscordID)
		if targetUserID == "" {
			return editResponseFn("User not found for Discord ID: %s", targetDiscordID)
		}
	}

	// 2. If not found, check if it's a VRML user ID or player ID
	if targetUserID == "" {
		// Try as VRML user ID first
		vrmlUserID = identifier
		ownerID, err := GetVRMLAccountOwner(ctx, nk, vrmlUserID)
		if err != nil {
			return editResponseFn("Error looking up VRML user ID: %v", err)
		}
		if ownerID != "" {
			targetUserID = ownerID
		} else {
			// Try as VRML player ID
			vg := vrmlgo.New("")
			vrmlPlayer, err := vg.Player(identifier)
			if err != nil || vrmlPlayer == nil || vrmlPlayer.User.UserID == "" {
				return editResponseFn("Could not find user by identifier: %s\nTry using VRML user ID, VRML player ID, Discord ID, or Discord @mention", identifier)
			}
			vrmlUserID = vrmlPlayer.User.UserID
			ownerID, err := GetVRMLAccountOwner(ctx, nk, vrmlUserID)
			if err != nil {
				return editResponseFn("Error looking up VRML account owner: %v", err)
			}
			if ownerID == "" {
				return editResponseFn("VRML player %s (%s) is not linked to any user", vrmlPlayer.ThisGame.PlayerName, identifier)
			}
			targetUserID = ownerID
		}
	}

	// At this point we should have a targetUserID
	if targetUserID == "" {
		return editResponseFn("Could not find user by identifier: %s", identifier)
	}

	// Load the target user's profile to get VRML data
	profile, err := EVRProfileLoad(ctx, nk, targetUserID)
	if err != nil {
		return editResponseFn("Failed to load profile for user: %v", err)
	}

	vrmlUserID = profile.VRMLUserID()
	if vrmlUserID == "" {
		return editResponseFn("User does not have a VRML account linked.")
	}

	// Get the target user's Discord ID if we don't have it yet
	if targetDiscordID == "" {
		targetDiscordID, err = GetDiscordIDByUserID(ctx, db, targetUserID)
		if err != nil {
			logger.WithField("error", err).Warn("Failed to get Discord ID")
			targetDiscordID = "unknown"
		}
	}

	// Load VRML player summary
	vrmlSummary = &VRMLPlayerSummary{}
	if err := StorableRead(ctx, nk, targetUserID, vrmlSummary, false); err != nil {
		logger.WithField("error", err).Warn("Failed to load VRML summary")
		vrmlSummary = nil
	}

	// Get account creation time
	account, err := nk.AccountGetId(ctx, targetUserID)
	if err != nil {
		return editResponseFn("Failed to get account: %v", err)
	}
	linkDate := account.GetUser().GetCreateTime()

	// Build the embed with VRML link data
	embed := &discordgo.MessageEmbed{
		Title:       "VRML Account Unlink Confirmation",
		Color:       EmbedColorRed,
		Description: fmt.Sprintf("Target: <@%s>", targetDiscordID),
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		Fields:      []*discordgo.MessageEmbedField{},
	}

	// Add VRML user data
	if vrmlSummary != nil && vrmlSummary.User != nil {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "VRML User",
			Value:  fmt.Sprintf("[%s](https://vrmasterleague.com/Users/%s)", vrmlSummary.User.UserName, vrmlUserID),
			Inline: true,
		})
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "VRML User ID",
			Value:  fmt.Sprintf("`%s`", vrmlUserID),
			Inline: true,
		})
	} else {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "VRML User ID",
			Value:  fmt.Sprintf("`%s`\n[View Profile](https://vrmasterleague.com/Users/%s)", vrmlUserID, vrmlUserID),
			Inline: false,
		})
	}

	// Add player data if available
	if vrmlSummary != nil && vrmlSummary.Player != nil {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "VRML Player",
			Value:  fmt.Sprintf("[%s](https://vrmasterleague.com/EchoArena/Players/%s)", vrmlSummary.Player.ThisGame.PlayerName, vrmlSummary.Player.ThisGame.PlayerID),
			Inline: true,
		})
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Player ID",
			Value:  fmt.Sprintf("`%s`", vrmlSummary.Player.ThisGame.PlayerID),
			Inline: true,
		})
	}

	// Add match counts if available
	if vrmlSummary != nil && len(vrmlSummary.MatchCountsBySeasonByTeam) > 0 {
		totalMatches := 0
		for _, teamCounts := range vrmlSummary.MatchCountsBySeasonByTeam {
			for _, count := range teamCounts {
				totalMatches += count
			}
		}
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Total VRML Matches",
			Value:  fmt.Sprintf("%d", totalMatches),
			Inline: true,
		})
	}

	// Add link creation date
	embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
		Name:   "Account Created",
		Value:  fmt.Sprintf("<t:%d:F>", linkDate.Seconds),
		Inline: false,
	})

	embed.Footer = &discordgo.MessageEmbedFooter{
		Text: "Click 'Unlink' to confirm the unlink action.",
	}

	// Create the unlink button
	// Encode the target user ID and VRML user ID in the custom ID
	customID := fmt.Sprintf("unlink-vrml-confirm:%s:%s", targetUserID, vrmlUserID)
	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Label:    "Unlink VRML Account",
					Style:    discordgo.DangerButton,
					CustomID: customID,
					Emoji:    &discordgo.ComponentEmoji{Name: "🔓"},
				},
			},
		},
	}

	return editEmbedResponseFn(embed, components)
}
