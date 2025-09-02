package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"
	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

func (d *DiscordAppBot) linkHeadset(ctx context.Context, logger runtime.Logger, user *discordgo.Member, linkCode string) error {

	var (
		nk        = d.nk
		groupID   = d.cache.GuildIDToGroupID(user.GuildID)
		discordID = user.User.ID
		username  = user.User.Username
	)

	// Validate the link code as a 4 character string
	if len(linkCode) != 4 {
		return errors.New("invalid link code: link code must be (4) letters long (i.e. ABCD)")
	}

	if err := func() error {

		// Exchange the link code for a device auth.
		ticket, err := ExchangeLinkCode(ctx, nk, logger, linkCode)
		if err != nil {
			return fmt.Errorf("failed to exchange link code: %w", err)
		}

		tags := map[string]string{
			"group_id":     groupID,
			"headset_type": normalizeHeadsetType(ticket.LoginProfile.SystemInfo.HeadsetType),
			"is_pcvr":      fmt.Sprintf("%t", ticket.LoginProfile.BuildNumber != evr.StandaloneBuildNumber),
			"new_account":  "false",
		}

		// Authenticate/create an account.

		userID, _, created, err := d.nk.AuthenticateCustom(ctx, discordID, username, true)
		if err != nil {
			return fmt.Errorf("failed to authenticate (or create) user %s: %w", discordID, err)
		}
		if created {
			tags["new_account"] = "true"
		}
		// Add the user to the group.
		if err := d.nk.GroupUsersAdd(ctx, SystemUserID, groupID, []string{userID}); err != nil {
			return fmt.Errorf("error joining group: %w", err)
		}

		// Link the device to the account.
		if err := nk.LinkDevice(ctx, userID, ticket.XPID.String()); err != nil {
			return fmt.Errorf("failed to link headset: %w", err)
		}
		d.metrics.CustomCounter("link_headset", tags, 1)
		// Set the client IP as authorized in the LoginHistory
		history := NewLoginHistory(userID)
		if err := StorableReadNk(ctx, nk, userID, history, true); err != nil {
			return fmt.Errorf("failed to load login history: %w", err)
		}
		history.Update(ticket.XPID, ticket.ClientIP, &ticket.LoginProfile, true)

		if err := StorableWriteNk(ctx, nk, userID, history); err != nil {
			return fmt.Errorf("failed to save login history: %w", err)
		}

		return nil
	}(); err != nil {
		logger.WithFields(map[string]interface{}{
			"discord_id": discordID,
			"link_code":  linkCode,
			"error":      err,
		}).Error("Failed to link headset")
		return err
	}
	return nil
}

func (d *DiscordAppBot) handleLinkHeadset(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {

	options := i.ApplicationCommandData().Options
	if len(options) == 0 {
		return errors.New("no options provided")
	}
	linkCode := options[0].StringValue()

	if user == nil {
		return nil
	}
	member, err := s.GuildMember(i.GuildID, user.ID)
	if err != nil {
		logger.WithField("user_id", user.ID).Error("Failed to get guild member")
	}

	if err := d.linkHeadset(ctx, logger, member, linkCode); err != nil {
		return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Flags:   discordgo.MessageFlagsEphemeral,
				Content: err.Error(),
			},
		})
	}

	content := "Your headset has been linked. Restart your game."

	d.cache.QueueSyncMember(i.GuildID, user.ID, true)

	// Send the response
	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags:   discordgo.MessageFlagsEphemeral,
			Content: content,
		},
	})
}

func (d *DiscordAppBot) handleUnlinkHeadset(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
	nk := d.nk
	options := i.ApplicationCommandData().Options
	if len(options) == 0 {

		account, err := nk.AccountGetId(ctx, userID)
		if err != nil {
			logger.Error("Failed to get account", zap.Error(err))
			return err
		}
		if len(account.Devices) == 0 {
			return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Flags:   discordgo.MessageFlagsEphemeral,
					Content: "No headsets are linked to this account.",
				},
			})
		}

		loginHistory := NewLoginHistory(userID)
		if err := StorableReadNk(ctx, nk, userID, loginHistory, true); err != nil {
			logger.Error("Failed to load login history", zap.Error(err))
			return err
		}

		options := make([]discordgo.SelectMenuOption, 0, len(account.Devices))
		for _, device := range account.Devices {

			description := ""
			xpid, err := evr.ParseEvrId(device.GetId())
			if err != nil {
				continue
			}
			if ts, ok := loginHistory.GetXPI(*xpid); ok {
				hours := int(time.Since(ts).Hours())
				if hours < 1 {
					minutes := int(time.Since(ts).Minutes())
					if minutes < 1 {
						description = "Just now"
					} else {
						description = fmt.Sprintf("%d minutes ago", minutes)
					}
				} else if hours < 24 {
					description = fmt.Sprintf("%d hours ago", hours)
				} else {
					description = fmt.Sprintf("%d days ago", int(time.Since(ts).Hours()/24))
				}
			}

			options = append(options, discordgo.SelectMenuOption{
				Label: device.GetId(),
				Value: device.GetId(),
				Emoji: &discordgo.ComponentEmoji{
					Name: "🔗",
				},
				Description: description,
			})
		}

		response := &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Flags:   discordgo.MessageFlagsEphemeral,
				Content: "Select a device to unlink",
				Components: []discordgo.MessageComponent{
					discordgo.ActionsRow{
						Components: []discordgo.MessageComponent{
							discordgo.SelectMenu{
								// Select menu, as other components, must have a customID, so we set it to this value.
								CustomID:    "unlink-headset",
								Placeholder: "<select a device to unlink>",
								Options:     options,
							},
						},
					},
				},
			},
		}
		return s.InteractionRespond(i.Interaction, response)
	}
	xpid := options[0].StringValue()
	// Validate the link code as a 4 character string

	if user == nil {
		return nil
	}

	if err := func() error {

		return nk.UnlinkDevice(ctx, userID, xpid)

	}(); err != nil {
		return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Flags:   discordgo.MessageFlagsEphemeral,
				Content: err.Error(),
			},
		})
	}
	d.metrics.CustomCounter("unlink_headset", nil, 1)
	content := "Your headset has been unlinked. Restart your game."
	d.cache.QueueSyncMember(i.GuildID, user.ID, false)

	if err := d.cache.updateLinkStatus(ctx, user.ID); err != nil {
		return fmt.Errorf("failed to update link status: %w", err)
	}

	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags:   discordgo.MessageFlagsEphemeral,
			Content: content,
		},
	})
}
