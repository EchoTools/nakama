package service

import (
	"context"
	"fmt"
	"math/rand"
	"slices"

	"github.com/bwmarrin/discordgo"
	evr "github.com/echotools/nakama/v3/protocol"
	"go.uber.org/zap"
)

// IP2FAFlow handles IP-based two-factor authentication
type IP2FAFlow struct {
	pipeline *EvrPipeline
}

// NewIP2FAFlow creates a new IP2FA flow handler
func NewIP2FAFlow(pipeline *EvrPipeline) *IP2FAFlow {
	return &IP2FAFlow{pipeline: pipeline}
}

// CheckIPAuthorization checks if an IP is authorized and handles 2FA flow
func (p *EvrPipeline) CheckIPAuthorization(ctx context.Context, logger *zap.Logger, loginHistory *LoginHistory, userID string, discordID string, xpID evr.XPID, clientIP string, isPasswordAuthenticated bool, loginPayload *evr.LoginProfile) (error, bool) {
	// If IP is already authorized or user is password authenticated, allow access
	if authorized := loginHistory.IsAuthorizedIP(clientIP); authorized || isPasswordAuthenticated {
		// Update the last used time for this IP
		if isNew := loginHistory.AuthorizeIP(clientIP); isNew {
			if err := SendIPAuthorizationNotification(p.discordCache.dg, userID, clientIP); err != nil {
				// Log the error, but don't return it as it's not critical
				logger.Warn("Failed to send IP authorization notification", zap.Error(err))
			}
		}
		return nil, true
	}

	// IP is not authorized - add a pending authorization entry
	_ = loginHistory.AddPendingAuthorizationIP(xpID, clientIP, loginPayload)
	if err := StorableWriteNk(ctx, p.nk, userID, loginHistory); err != nil {
		return fmt.Errorf("failed to save login history: %w", err), false
	}

	// IP verification required
	return nil, false
}

// SendIPApprovalRequest sends an IP approval request to the user via Discord
func (p *EvrPipeline) SendIPApprovalRequest(ctx context.Context, userID string, discordID string, entry *LoginHistoryEntry, ipInfo IPInfo, activeGroupID string) error {
	// Try to send DM first
	channel, err := p.discordCache.dg.UserChannelCreate(discordID)
	if err == nil {
		// Send the verification message via DM
		embeds, components := IPVerificationEmbed(entry, ipInfo)
		_, err = p.discordCache.dg.ChannelMessageSendComplex(channel.ID, &discordgo.MessageSend{
			Embeds:     embeds,
			Components: components,
		})
		if err == nil {
			return NewLocationError{
				code:        fmt.Sprintf("%02d", entry.CreatedAt.Nanosecond()%100),
				botUsername: p.discordCache.dg.State.User.Username,
				useDMs:      true,
			}
		}
	}

	// If DM failed and it's not because user has DMs disabled, return error
	if IsDiscordErrorCode(err, discordgo.ErrCodeCannotSendMessagesToThisUser) {
		return fmt.Errorf("failed to send IP approval request: %w", err)
	} else if err != nil {
		// Log the error but continue to provide alternative verification methods
		p.logger.Warn("Failed to send IP approval DM, user may have DMs disabled", zap.String("userID", userID), zap.Error(err))
	} else if err == nil {
		return nil // Success
	}
	// If reached here, it means DMs are disabled or failed - provide alternative verification methods
	twoFactorCode := fmt.Sprintf("%02d", entry.CreatedAt.Nanosecond()%100)

	// Try to get guild info for slash command instructions
	if activeGroupID != "" {
		activeGuildID := p.discordCache.GroupIDToGuildID(activeGroupID)
		if guild, err := p.discordCache.dg.Guild(activeGuildID); err == nil {
			return &NewLocationError{
				guildName: guild.Name,
				code:      twoFactorCode,
			}
		}
	}

	// Fall back to bot username if available
	return &NewLocationError{
		botUsername: p.appBot.dg.State.User.Username,
		code:        twoFactorCode,
	}
}

// IPVerificationEmbed creates Discord embed and components for IP verification
func IPVerificationEmbed(entry *LoginHistoryEntry, ipInfo IPInfo) ([]*discordgo.MessageEmbed, []discordgo.MessageComponent) {
	code := fmt.Sprintf("%02d", entry.CreatedAt.Nanosecond()%100)
	codes := []string{code}
	numCodes := 5

	// Generate additional random codes for security
	for len(codes) < numCodes {
		s := fmt.Sprintf("%02d", rand.Intn(100))
		if slices.Contains(codes, s) {
			continue
		}
		codes = append(codes, s)
	}

	// Shuffle the codes to randomize position
	rand.Shuffle(len(codes), func(i, j int) {
		codes[i], codes[j] = codes[j], codes[i]
	})

	// Create select menu options
	options := make([]discordgo.SelectMenuOption, 0, len(codes))
	for _, c := range codes {
		options = append(options, discordgo.SelectMenuOption{
			Label: c,
			Value: entry.ClientIP + ":" + c,
		})
	}

	// Build the embed
	embed := &discordgo.MessageEmbed{
		Title:       "New Login Location",
		Description: "Please verify the login attempt.",
		Color:       0x00ff00,
		Fields: []*discordgo.MessageEmbedField{
			{
				Name:   "IP Address",
				Value:  entry.ClientIP,
				Inline: true,
			},
		},
	}

	// Add location info if available
	if ipInfo != nil {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Location (may be inaccurate)",
			Value:  fmt.Sprintf("%s, %s, %s", ipInfo.City(), ipInfo.Region(), ipInfo.CountryCode()),
			Inline: true,
		})
	}

	// Add security note
	embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
		Name:   "Note",
		Value:  "Report this message if you were not instructed (in your headset) to look for this message. Use the **Report** button below.",
		Inline: false,
	})

	// Create interactive components
	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				&discordgo.SelectMenu{
					CustomID:    "approve_ip",
					Placeholder: "Select the correct code",
					Options:     options,
				},
			},
		},
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				&discordgo.Button{
					Label:    "Report to EchoVRCE",
					Style:    discordgo.LinkButton,
					URL:      ServiceSettings().ReportURL,
					Disabled: false,
				},
			},
		},
	}

	return []*discordgo.MessageEmbed{embed}, components
}
