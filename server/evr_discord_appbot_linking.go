package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (d *DiscordAppBot) linkHeadset(ctx context.Context, logger runtime.Logger, user *discordgo.Member, linkCode string) error {

	var (
		nk        = d.nk
		groupID   = d.cache.GuildIDToGroupID(user.GuildID)
		userID    = d.cache.DiscordIDToUserID(user.User.ID) // Will be blank for new users
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
		if userID == "" {
			tags["new_account"] = "true"
			userID, _, err = authenticateOrResolveConflict(ctx, nk, discordID, username)
			if err != nil {
				return fmt.Errorf("failed to authenticate (or create) user %s: %w", discordID, err)
			}
		}

		if err := d.nk.GroupUsersAdd(ctx, SystemUserID, groupID, []string{userID}); err != nil {
			return fmt.Errorf("error joining group: %w", err)
		}

		if err := nk.LinkDevice(ctx, userID, ticket.XPID.Token()); err != nil {
			return fmt.Errorf("failed to link headset: %w", err)
		}
		d.metrics.CustomCounter("link_headset", tags, 1)
		// Set the client IP as authorized in the LoginHistory
		history := NewLoginHistory(userID)
		if err := StorableRead(ctx, nk, userID, history, true); err != nil {
			return fmt.Errorf("failed to load login history: %w", err)
		}
		history.Update(ticket.XPID, ticket.ClientIP, ticket.LoginProfile, true)

		if err := StorableWrite(ctx, nk, userID, history); err != nil {
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
		logger.WithField("user_id", user.ID).Error("failed to get guild member")
		return fmt.Errorf("failed to get guild member: %w", err)
	}
	if member == nil {
		return fmt.Errorf("guild member is nil for user_id: %s", user.ID)
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

	// Resolve target user. If a "user" option is provided, verify the caller
	// is a global operator and operate on the target's account instead.
	targetUserID := userID
	targetDiscordID := user.ID
	var targetUser *discordgo.User
	for _, opt := range options {
		if opt.Name == "user" {
			targetUser = opt.UserValue(s)
			break
		}
	}

	if targetUser != nil {
		if targetUser.Bot {
			return editInteractionResponse(s, i, "Bots don't have accounts.")
		}

		isGlobalOperator, err := CheckSystemGroupMembership(ctx, d.db, userID, GroupGlobalOperators)
		if err != nil {
			return fmt.Errorf("failed to check global operator status: %w", err)
		}
		if !isGlobalOperator {
			return editInteractionResponse(s, i, "Only global operators can unlink another user's headset.")
		}

		targetUserID = d.cache.DiscordIDToUserID(targetUser.ID)
		if targetUserID == "" {
			return editInteractionResponse(s, i, "Target user not found.")
		}
		targetDiscordID = targetUser.ID

		logger.Info("Global operator unlinking headset for another user",
			zap.String("operatorUserID", userID),
			zap.String("operatorDiscordID", user.ID),
			zap.String("targetUserID", targetUserID),
			zap.String("targetDiscordID", targetDiscordID),
		)
	}

	// Find the device-link option if provided.
	var xpid string
	for _, opt := range options {
		if opt.Name == "device-link" {
			xpid = opt.StringValue()
			break
		}
	}

	if xpid == "" {
		// No device specified — show the select menu for the target's devices.
		account, err := nk.AccountGetId(ctx, targetUserID)
		if err != nil {
			logger.Error("Failed to get account", zap.Error(err))
			return err
		}
		if len(account.Devices) == 0 {
			return editInteractionResponse(s, i, "No headsets are linked to this account.")
		}

		loginHistory := NewLoginHistory(targetUserID)
		if err := StorableRead(ctx, nk, targetUserID, loginHistory, true); err != nil {
			logger.Error("Failed to load login history", zap.Error(err))
			return err
		}

		menuOptions := make([]discordgo.SelectMenuOption, 0, len(account.Devices))
		for _, device := range account.Devices {

			description := ""
			xpidVal, err := evr.ParseEvrId(device.GetId())
			if err != nil {
				continue
			}
			if ts, ok := loginHistory.GetXPI(*xpidVal); ok {
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

			menuOptions = append(menuOptions, discordgo.SelectMenuOption{
				Label: device.GetId(),
				Value: device.GetId(),
				Emoji: &discordgo.ComponentEmoji{
					Name: "🔗",
				},
				Description: description,
			})
		}

		// Encode the target user ID in the custom ID so the component
		// callback knows whose device to unlink.
		customID := "unlink-headset"
		if targetUser != nil {
			customID = "unlink-headset:" + targetUserID
		}

		components := []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{
					discordgo.SelectMenu{
						CustomID:    customID,
						Placeholder: "<select a device to unlink>",
						Options:     menuOptions,
					},
				},
			},
		}
		content := "Select a device to unlink"
		_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content:    &content,
			Components: &components,
		})
		return err
	}

	if user == nil {
		return nil
	}

	// Only allow unlinking platform device IDs (OVR-ORG-, DMO-, DSC-, etc.).
	// Reject anything else (e.g. vrml: device links) — those must go through the dedicated VRML unlink flow.
	if _, err := evr.ParseEvrId(xpid); err != nil {
		logger.Warn("Attempted to unlink non-headset device via unlink-headset", zap.String("userID", targetUserID), zap.String("discordID", targetDiscordID), zap.String("deviceID", xpid))
		return editInteractionResponse(s, i, "Invalid device ID.")
	}

	logger.Info("Unlinking headset device", zap.String("userID", targetUserID), zap.String("discordID", targetDiscordID), zap.String("deviceID", xpid))

	if err := nk.UnlinkDevice(ctx, targetUserID, xpid); err != nil {
		return editInteractionResponse(s, i, err.Error())
	}
	d.metrics.CustomCounter("unlink_headset", nil, 1)

	content := "Headset has been unlinked. Restart the game."
	if targetUser != nil {
		content = fmt.Sprintf("Unlinked headset `%s` from <@%s>.", xpid, targetDiscordID)
	}
	d.cache.QueueSyncMember(i.GuildID, targetDiscordID, false)

	if err := d.cache.updateLinkStatus(ctx, targetDiscordID); err != nil {
		return fmt.Errorf("failed to update link status: %w", err)
	}

	return editInteractionResponse(s, i, content)
}

// authenticateOrResolveConflict attempts to create an account via AuthenticateCustom.
// If the username is already taken by a different account, it renames the conflicting
// account and retries. The Discord username is authoritative: the new user keeps it.
func authenticateOrResolveConflict(ctx context.Context, nk runtime.NakamaModule, discordID, username string) (string, string, error) {
	userID, uname, _, err := nk.AuthenticateCustom(ctx, discordID, username, true)
	if err == nil {
		return userID, uname, nil
	}

	if status.Code(err) != codes.AlreadyExists {
		return "", "", fmt.Errorf("failed to create account: %w", err)
	}

	// Username conflict. Look up who holds it.
	users, err := nk.UsersGetUsername(ctx, []string{username})
	if err != nil {
		return "", "", fmt.Errorf("failed to look up conflicting username %q: %w", username, err)
	}

	if len(users) == 0 {
		// Race condition: conflict gone. Retry once.
		userID, uname, _, err = nk.AuthenticateCustom(ctx, discordID, username, true)
		if err != nil {
			return "", "", fmt.Errorf("failed to create account after conflict resolved: %w", err)
		}
		return userID, uname, nil
	}

	conflicting := users[0]

	// Check if the conflicting account belongs to the same Discord user.
	conflictAccount, err := nk.AccountGetId(ctx, conflicting.Id)
	if err != nil {
		return "", "", fmt.Errorf("failed to resolve username conflict: %w", err)
	}
	if conflictAccount.GetCustomId() == discordID {
		// Same Discord user already owns this username. Return the existing account.
		return conflicting.Id, conflicting.Username, nil
	}

	// Rename the conflicting account: append "_" + first 4 chars of their user ID.
	suffix := conflicting.Id
	if len(suffix) > 4 {
		suffix = suffix[:4]
	}
	// Truncate base username to leave room for "_" + suffix within 128-byte limit.
	// Use rune-aware truncation to avoid splitting multi-byte UTF-8 characters.
	maxBase := 128 - 1 - len(suffix)
	base := username
	if len(base) > maxBase {
		runes := []rune(base)
		for len(string(runes)) > maxBase {
			runes = runes[:len(runes)-1]
		}
		base = string(runes)
	}
	renamedUsername := base + "_" + suffix

	if err := nk.AccountUpdateId(ctx, conflicting.Id, renamedUsername, nil, "", "", "", "", ""); err != nil {
		return "", "", fmt.Errorf("failed to resolve username conflict: %w", err)
	}

	// Retry authentication with the now-available username.
	userID, uname, _, err = nk.AuthenticateCustom(ctx, discordID, username, true)
	if err != nil {
		return "", "", fmt.Errorf("failed to create account after renaming conflicting user: %w", err)
	}
	return userID, uname, nil
}
