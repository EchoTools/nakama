package service

import (
	"context"
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/echotools/vrmlgo/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

func (d *DiscordAppBot) handleVRMLVerify(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
	// accountLinkCommandHandler handles the account link command from Discord
	var (
		nk = d.nk
		db = d.db
	)

	logger = logger.WithFields(map[string]any{
		"discord_id": i.Member.User.ID,
		"username":   i.Member.User.Username,
		"uid":        userID,
	})

	editResponseFn := func(format string, a ...any) error {
		content := fmt.Sprintf(format, a...)
		_, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &content})
		return err
	}

	profile, err := EVRProfileLoad(ctx, nk, userID)
	if err != nil {
		return fmt.Errorf("failed to load profile: %w", err)
	}

	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags: discordgo.MessageFlagsLoading | discordgo.MessageFlagsEphemeral,
		},
	}); err != nil {
		logger.WithField("error", err).Error("Failed to send interaction response")
	}

	if profile.VRMLUserID() != "" {
		// User is already linked
		// Check if the User is valid.

		// Retrieve the VRML user data
		vg := vrmlgo.New("")
		m, err := vg.Member(profile.VRMLUserID(), vrmlgo.WithUseCache(false))
		if err != nil {
			return fmt.Errorf("failed to get member data: %w", err)
		}
		vrmlUser := m.User

		if vrmlUser.GetDiscordID() != i.Member.User.ID {
			// VRML User is linked to a different Discord account
			logger.Warn("Discord ID mismatch")
			vrmlLink := fmt.Sprintf("[%s](https://vrmasterleague.com/EchoArena/Users/%s)", vrmlUser.UserName, vrmlUser.ID)
			thisDiscordTag := fmt.Sprintf("%s#%s", user.Username, user.Discriminator)
			return editResponseFn(fmt.Sprintf("VRML account %s is currently linked to %s. Please relink the VRML account to this Discord account (%s).", vrmlLink, vrmlUser.DiscordTag, thisDiscordTag))
		}

		// Queue the event to count matches and assign entitlements
		if err := SendEvent(ctx, nk, &EventVRMLAccountLink{
			UserID:     userID,
			VRMLUserID: vrmlUser.ID,
		}); err != nil {
			return fmt.Errorf("failed to queue VRML account linked event: %w", err)
		}

		return editResponseFn("Your [VRML account](%s) is already linked. Reverifying your entitlements...", "https://vrmasterleague.com/Users/"+profile.VRMLUserID())
	}

	// User is not linked
	go func() {
		// Start the OAuth flow

		vars, _ := ctx.Value(runtime.RUNTIME_CTX_ENV).(map[string]string)

		// Start the OAuth flow
		timeoutDuration := 5 * time.Minute
		flow, err := NewVRMLOAuthFlow(vars["VRML_OAUTH_CLIENT_ID"], vars["VRML_OAUTH_REDIRECT_URL"], timeoutDuration)
		if err != nil {
			logger.WithField("error", err).Error("Failed to start OAuth flow")
			return
		}

		// Send the link to the user
		if err := editResponseFn(fmt.Sprintf("To assign your cosmetics, [Verify your VRML account](%s)", flow.url)); err != nil {
			logger.WithField("error", err).Error("Failed to edit response")
		}

		var token string
		var vrmlUser *vrmlgo.User

		// Wait for the token to be returned
		select {
		case <-time.After(timeoutDuration):
			editResponseFn("OAuth flow timed out. Please run the command again.")
			return // Timeout
		case token = <-flow.tokenCh:
			// Token received
			vg := vrmlgo.New(token)
			vrmlUser, err = vg.Me(vrmlgo.WithUseCache(false))
			if err != nil {
				logger.Error("Failed to get VRML user data")
				return
			}
		}
		logger = logger.WithFields(map[string]any{
			"vrml_id":         vrmlUser.ID,
			"vrml_username":   vrmlUser.UserName,
			"vrml_discord_id": vrmlUser.GetDiscordID(),
		})

		if vrmlUser.GetDiscordID() != i.Member.User.ID {
			logger.Warn("Discord ID mismatch")
			// VRML User is linked to a different Discord account
			vrmlLink := fmt.Sprintf("[%s](https://vrmasterleague.com/EchoArena/Users/%s)", vrmlUser.UserName, vrmlUser.ID)
			thisDiscordTag := fmt.Sprintf("%s#%s", user.Username, user.Discriminator)
			if err := editResponseFn(fmt.Sprintf("VRML account %s is currently linked to %s. Please relink the VRML account to this Discord account (%s).", vrmlLink, vrmlUser.DiscordTag, thisDiscordTag)); err != nil {
				logger.WithField("error", err).Error("Failed to edit response")
			}
			return
		}

		// Link the accounts
		if err := LinkVRMLAccount(ctx, db, nk, userID, vrmlUser.ID); err != nil {
			if err, ok := err.(*AccountAlreadyLinkedError); ok {
				// Account is already linked to another user
				logger.WithField("owner_user_id", err.OwnerUserID).Error("Account already linked to another user.")
				if err := editResponseFn("Account already owned by another user, [Contact EchoVRCE](%s) if you need to unlink it.", ServiceSettings().ReportURL); err != nil {
					logger.WithField("error", err).Error("Failed to edit response")
				}
				return
			}
			logger.WithField("error", err).Error("Failed to link accounts")
			if err := editResponseFn("Failed to link accounts"); err != nil {
				logger.WithField("error", err).Error("Failed to edit response")
			}
			return
		}
		logger.Info("Linked VRML account")
		if err := editResponseFn(fmt.Sprintf("Your VRML account (`%s`) has been verified/linked. It will take a few minutes--up to a few hours--to update your entitlements.", vrmlUser.UserName)); err != nil {
			logger.WithField("error", err).Error("Failed to edit response")
			return
		}

	}()

	return nil
}
