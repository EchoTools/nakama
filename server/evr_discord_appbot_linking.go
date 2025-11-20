package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (d *DiscordAppBot) handleAuthenticationConflict(ctx context.Context, logger runtime.Logger, discordID string, username string) (string, error) {
	nk := d.nk

	logger.WithFields(map[string]interface{}{
		"discord_id": discordID,
		"username":   username,
	}).Info("authentication conflict: recovering...")

	// Find account by username
	users, err := nk.UsersGetUsername(ctx, []string{username})
	if err != nil {
		logger.WithFields(map[string]interface{}{
			"discord_id": discordID,
			"username":   username,
			"error":      err,
		}).Error("Couldn't lookup name")
		return "", fmt.Errorf("failed to lookup conflicting username: %w", err)
	}

	if len(users) == 0 {
		// Username exists in database but lookup returned no results (edge case)
		logger.WithFields(map[string]interface{}{
			"discord_id": discordID,
			"username":   username,
		}).Warn("Username not found in lookup despite conflict - possible race condition")
		return "", errors.New("conflicting account no longer accessible")
	}

	conflictingUser := users[0]
	conflictingUserID := conflictingUser.Id

	logger.WithFields(map[string]interface{}{
		"discord_id":          discordID,
		"username":            username,
		"conflicting_user_id": conflictingUserID,
	}).Info("Found conflicting account")

	// Look up the full account to get custom_id information
	account, err := nk.AccountGetId(ctx, conflictingUserID)
	if err != nil {
		logger.WithFields(map[string]interface{}{
			"discord_id":          discordID,
			"conflicting_user_id": conflictingUserID,
			"error":               err,
		}).Error("Failed to get account details")
		return "", fmt.Errorf("failed to get account details: %w", err)
	}

	conflictingCustomID := account.GetCustomId()

	logger.WithFields(map[string]interface{}{
		"discord_id":          discordID,
		"username":            username,
		"conflicting_user_id": conflictingUserID,
		"custom_id":           conflictingCustomID,
	}).Info("Retrieved account details - evaluating recovery path")

	// Check if the conflicting account already has a discord custom_id
	if conflictingCustomID != "" {
		if conflictingCustomID == discordID {
			// Return the existing user ID - they're already linked.
			logger.WithFields(map[string]interface{}{
				"discord_id": discordID,
				"user_id":    conflictingUserID,
				"username":   username,
			}).Info("Recovery successful: user already linked with matching custom_id")
			return conflictingUserID, nil
		}

		// This is for cases where a different user owns the conflicting account.
		// This cannot be automatically resolved.
		logger.WithFields(map[string]interface{}{
			"discord_id":       discordID,
			"conflicting_user": conflictingCustomID,
			"username":         username,
		}).Warn("Account conflict: different discord user owns the username")
		return "", errors.New(
			"Username is already linked to a different discord account. " +
				"Reference: CONFLICT_DIFFERENT_OWNER",
		)
	}

	// Link the account to the current discord user
	logger.WithFields(map[string]interface{}{
		"discord_id":          discordID,
		"conflicting_user_id": conflictingUserID,
		"username":            username,
	}).Info("Attempting to recover by linking custom_id to conflicting account")

	if err := d.linkCustomID(ctx, logger, conflictingUserID, discordID); err != nil {
		logger.WithFields(map[string]interface{}{
			"discord_id":          discordID,
			"conflicting_user_id": conflictingUserID,
			"error":               err,
		}).Error("Failed to link custom ID during conflict recovery")
		return "", fmt.Errorf("failed to recover account: %w", err)
	}

	logger.WithFields(map[string]interface{}{
		"discord_id": discordID,
		"user_id":    conflictingUserID,
		"username":   username,
	}).Info("Recovery successful: custom_id linked to conflicting account")

	return conflictingUserID, nil
}

// Update a user's "custom_id" to a given discord ID
func (d *DiscordAppBot) linkCustomID(ctx context.Context, logger runtime.Logger, userID string, customID string) error {
	nk := d.nk
	err := nk.LinkCustom(ctx, userID, customID)
	if err != nil {
		return err
	}
	logger.WithFields(map[string]interface{}{
		"user_id":   userID,
		"custom_id": customID,
	}).Info("Successfully linked custom_id")
	return nil
}

func (d *DiscordAppBot) isAlreadyExistsError(err error) bool {
	if err == nil {
		return false
	}
	// Check if the error is a gRPC AlreadyExists status error
	st, ok := status.FromError(err)
	if ok && st.Code() == codes.AlreadyExists {
		return true
	}
	// Also check the error message string for the AlreadyExists code
	// Good for cases where the error is wrapped or stringified
	errMsg := err.Error()
	return strings.Contains(errMsg, "code = AlreadyExists") || strings.Contains(errMsg, "AlreadyExists")
}

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
			userID, _, _, err = d.nk.AuthenticateCustom(ctx, discordID, username, true)
			if err != nil {
				// Check if the error is an AlreadyExists error indicating username conflict
				if d.isAlreadyExistsError(err) {
					logger.WithFields(map[string]interface{}{
						"discord_id": discordID,
						"username":   username,
					}).Info("Username conflict detected")

					// Attempt to recover from the conflict by checking the existing account
					recoveredUserID, recoveryErr := d.handleAuthenticationConflict(ctx, logger, discordID, username)
					if recoveryErr != nil {
						// Recovery failed - return the specific error to the user
						return fmt.Errorf("failed to recover from username conflict: %w", recoveryErr)
					}

					// Recovery succeeded - use the recovered user ID
					userID = recoveredUserID
					tags["new_account"] = "false"
					logger.WithFields(map[string]interface{}{
						"discord_id":     discordID,
						"recovered_user": userID,
					}).Info("Successfully recovered from username conflict")
				} else {
					// Not a username conflict - return the original error
					return fmt.Errorf("failed to authenticate (or create) user %s: %w", discordID, err)
				}
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
		if err := StorableRead(ctx, nk, userID, loginHistory, true); err != nil {
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
