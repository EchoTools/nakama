package server

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

// LobbyAuthorizationResult contains the result of lobby authorization checks
type LobbyAuthorizationResult struct {
	Authorized   bool
	ErrorCode    string
	ErrorMessage string
	AuditMessage string
	Profile      *evr.ServerProfile
}

// LobbyAuthorizationContext contains all the information needed for authorization
type LobbyAuthorizationContext struct {
	Session     Session
	LobbyParams *LobbySessionParameters
	GuildGroup  *GuildGroup
	UserID      string
	GroupID     string
	Params      *SessionParameters
}

// LobbyAuthorizer interface for testable authorization logic
type LobbyAuthorizer interface {
	Authorize(ctx context.Context, logger *zap.Logger, authCtx *LobbyAuthorizationContext) *LobbyAuthorizationResult
}

// DefaultLobbyAuthorizer implements the LobbyAuthorizer interface
type DefaultLobbyAuthorizer struct {
	pipeline *EvrPipeline
}

// NewDefaultLobbyAuthorizer creates a new DefaultLobbyAuthorizer
func NewDefaultLobbyAuthorizer(pipeline *EvrPipeline) *DefaultLobbyAuthorizer {
	return &DefaultLobbyAuthorizer{pipeline: pipeline}
}

// Authorize performs comprehensive lobby authorization checks
func (a *DefaultLobbyAuthorizer) Authorize(ctx context.Context, logger *zap.Logger, authCtx *LobbyAuthorizationContext) *LobbyAuthorizationResult {
	result := &LobbyAuthorizationResult{Authorized: false}

	// Check guild membership
	if membershipResult := a.checkGuildMembership(ctx, authCtx); !membershipResult.Authorized {
		return membershipResult
	}

	// Check suspensions
	if suspensionResult := a.checkSuspensions(ctx, authCtx); !suspensionResult.Authorized {
		return suspensionResult
	}

	// Check account age
	if ageResult := a.checkAccountAge(ctx, authCtx); !ageResult.Authorized {
		return ageResult
	}

	// Check VPN restrictions
	if vpnResult := a.checkVPNRestrictions(ctx, authCtx); !vpnResult.Authorized {
		return vpnResult
	}

	// Check feature restrictions
	if featureResult := a.checkFeatureRestrictions(ctx, authCtx); !featureResult.Authorized {
		return featureResult
	}

	// Generate profile
	profile, err := a.generateProfile(ctx, logger, authCtx)
	if err != nil {
		result.ErrorCode = "profile_generation_failed"
		result.ErrorMessage = fmt.Sprintf("Failed to generate profile: %v", err)
		return result
	}

	result.Authorized = true
	result.Profile = profile
	return result
}

// Helper methods for individual checks (extracted from the original lobbyAuthorize function)
func (a *DefaultLobbyAuthorizer) checkGuildMembership(ctx context.Context, authCtx *LobbyAuthorizationContext) *LobbyAuthorizationResult {
	result := &LobbyAuthorizationResult{Authorized: true}

	if authCtx.GuildGroup.IsPrivate() {
		if authCtx.GuildGroup.RoleMap.Member != "" && !authCtx.GuildGroup.IsMember(authCtx.UserID) {
			result.Authorized = false
			result.ErrorCode = "not_member"
			result.ErrorMessage = "You are not a member of this private guild."
			result.AuditMessage = "not a member of private guild"
			return result
		} else {
			// If the member role is not set, the user must be a member of the guild
			if guildMember, err := a.pipeline.discordCache.GuildMember(authCtx.GuildGroup.GuildID, authCtx.LobbyParams.DiscordID); err != nil {
				if err != nil && !IsDiscordErrorCode(err, discordgo.ErrCodeUnknownMember) {
					a.pipeline.logger.Warn("Failed to get guild member. failing open.", zap.String("guild_id", authCtx.GuildGroup.GuildID), zap.Error(err))
				}
			} else if guildMember == nil || guildMember.Pending {
				result.Authorized = false
				result.ErrorCode = "not_member"
				result.ErrorMessage = "You are not a member of this private guild."
				result.AuditMessage = "not a member of private guild"
				return result
			}
		}
	}

	return result
}

func (a *DefaultLobbyAuthorizer) checkSuspensions(ctx context.Context, authCtx *LobbyAuthorizationContext) *LobbyAuthorizationResult {
	result := &LobbyAuthorizationResult{Authorized: true}

	// Check if user is suspended from the group
	if authCtx.GuildGroup.IsSuspended(authCtx.UserID, &authCtx.Params.xpID) {
		result.Authorized = false
		result.ErrorCode = "suspended_user"
		result.ErrorMessage = "You are suspended from this guild. (role-based)"
		result.AuditMessage = "is suspended via role: <@&" + authCtx.GuildGroup.RoleMap.Suspended + ">"
		return result
	}

	// Check suspension records by game mode
	var suspensionRecord GuildEnforcementRecord
	if recordsByGameMode := authCtx.Params.gameModeSuspensionsByGroupID[authCtx.GroupID]; len(recordsByGameMode) > 0 {
		for gameMode, r := range recordsByGameMode {
			if gameMode != authCtx.LobbyParams.Mode {
				continue // Skip records for other game modes
			}
			if r.IsExpired() || r.Expiry.Before(suspensionRecord.Expiry) {
				continue // Skip expired records
			}
			if r.UserID != authCtx.UserID {
				// The suspension is for an alternate account
				if authCtx.Params.ignoreDisabledAlternates {
					continue // User is excluded from suspension checks
				}
				if authCtx.GuildGroup.RejectPlayersWithSuspendedAlternates {
					suspensionRecord = r
				}
			} else {
				suspensionRecord = r
			}
		}

		if !suspensionRecord.Expiry.IsZero() {
			const maxMessageLength = 64
			var metricTag, auditLog string

			reason := suspensionRecord.UserNoticeText

			if suspensionRecord.UserID == authCtx.UserID {
				auditLog = fmt.Sprintf("suspended (expires <t:%d:R>): `%s`", suspensionRecord.Expiry.Unix(), suspensionRecord.UserNoticeText)
				metricTag = "suspended_user"
			} else {
				auditLog = fmt.Sprintf("suspended alternate (<@!%s>) (expires <t:%d:R>): `%s`", suspensionRecord.UserID, suspensionRecord.Expiry.Unix(), suspensionRecord.UserNoticeText)
				metricTag = "suspended_alt_user"
			}

			if suspensionRecord.SuspensionExcludesPrivateLobbies() {
				auditLog = fmt.Sprintf("Rejected limited access user (expires <t:%d:R>): `%s`", suspensionRecord.Expiry.Unix(), suspensionRecord.UserNoticeText)
				reason = "Public Access Denied: " + reason
				metricTag = "limited_access_user"
			}

			expires := fmt.Sprintf(" [exp: %s]", FormatDuration(time.Until(suspensionRecord.Expiry)))
			if len(reason)+len(expires) > maxMessageLength {
				reason = reason[:maxMessageLength-len(expires)-3] + "..."
			}

			reason = reason + expires
			result.Authorized = false
			result.ErrorCode = metricTag
			result.ErrorMessage = reason
			result.AuditMessage = auditLog
			return result
		}
	}

	return result
}

func (a *DefaultLobbyAuthorizer) checkAccountAge(ctx context.Context, authCtx *LobbyAuthorizationContext) *LobbyAuthorizationResult {
	result := &LobbyAuthorizationResult{Authorized: true}

	if authCtx.GuildGroup.MinimumAccountAgeDays > 0 && !authCtx.GuildGroup.IsAccountAgeBypass(authCtx.UserID) {
		// Check the account creation date
		t, err := discordgo.SnowflakeTimestamp(authCtx.LobbyParams.DiscordID)
		if err != nil {
			result.Authorized = false
			result.ErrorCode = "account_age_check_failed"
			result.ErrorMessage = "Failed to check account age"
			result.AuditMessage = fmt.Sprintf("failed to get discord snowflake timestamp: %v", err)
			return result
		}

		if t.After(time.Now().AddDate(0, 0, -authCtx.GuildGroup.MinimumAccountAgeDays)) {
			accountAge := time.Since(t).Hours() / 24
			result.Authorized = false
			result.ErrorCode = "account_age"
			result.ErrorMessage = fmt.Sprintf("Your account age (%d days) is too new (<%d days) to join this guild. ", int(accountAge), authCtx.GuildGroup.MinimumAccountAgeDays)
			result.AuditMessage = fmt.Sprintf("account age (%d > %d days.", int(accountAge), authCtx.GuildGroup.MinimumAccountAgeDays)
			return result
		}
	}

	return result
}

func (a *DefaultLobbyAuthorizer) checkVPNRestrictions(ctx context.Context, authCtx *LobbyAuthorizationContext) *LobbyAuthorizationResult {
	result := &LobbyAuthorizationResult{Authorized: true}

	if authCtx.GuildGroup.BlockVPNUsers && authCtx.Params.isVPN && !authCtx.GuildGroup.IsVPNBypass(authCtx.UserID) && authCtx.Params.ipInfo != nil {
		if authCtx.Params.ipInfo.FraudScore() >= authCtx.GuildGroup.FraudScoreThreshold {
			// Log detailed VPN rejection information
			fields := []*discordgo.MessageEmbedField{
				{Name: "Player", Value: fmt.Sprintf("<@%s>", authCtx.LobbyParams.DiscordID), Inline: false},
				{Name: "IP Address", Value: authCtx.Session.ClientIP(), Inline: true},
				{Name: "Data Provider", Value: authCtx.Params.ipInfo.DataProvider(), Inline: true},
				{Name: "Score", Value: fmt.Sprintf("%d", authCtx.Params.ipInfo.FraudScore()), Inline: true},
				{Name: "ISP", Value: authCtx.Params.ipInfo.ISP(), Inline: true},
				{Name: "Organization", Value: authCtx.Params.ipInfo.Organization(), Inline: true},
				{Name: "ASN", Value: fmt.Sprintf("%d", authCtx.Params.ipInfo.ASN()), Inline: true},
				{Name: "City", Value: authCtx.Params.ipInfo.City(), Inline: true},
				{Name: "Region", Value: authCtx.Params.ipInfo.Region(), Inline: true},
				{Name: "Country", Value: authCtx.Params.ipInfo.CountryCode(), Inline: true},
			}

			embed := &discordgo.MessageEmbed{
				Title:  "VPN User Rejected",
				Fields: fields,
				Color:  0xff0000, // Red color
			}
			message := &discordgo.MessageSend{
				Embeds: []*discordgo.MessageEmbed{embed},
			}
			if _, err := a.pipeline.appBot.dg.ChannelMessageSendComplex(authCtx.GuildGroup.AuditChannelID, message); err != nil {
				a.pipeline.logger.Warn("Failed to send audit message", zap.String("channel_id", authCtx.GuildGroup.AuditChannelID), zap.Error(err))
			}

			guildName := "This guild"
			if gg := authCtx.Params.guildGroups[authCtx.GroupID]; gg != nil {
				guildName = gg.Group.Name
			}

			result.Authorized = false
			result.ErrorCode = "vpn_user"
			result.ErrorMessage = fmt.Sprintf("Disable VPN to join %s", guildName)
			result.AuditMessage = fmt.Sprintf("vpn probability score %d >= %d", authCtx.Params.ipInfo.FraudScore(), authCtx.GuildGroup.FraudScoreThreshold)
			return result
		}
	}

	return result
}

func (a *DefaultLobbyAuthorizer) checkFeatureRestrictions(ctx context.Context, authCtx *LobbyAuthorizationContext) *LobbyAuthorizationResult {
	result := &LobbyAuthorizationResult{Authorized: true}

	if len(authCtx.GuildGroup.AllowedFeatures) > 0 {
		allowedFeatures := authCtx.GuildGroup.AllowedFeatures
		for _, feature := range authCtx.Params.supportedFeatures {
			if !slices.Contains(allowedFeatures, feature) {
				result.Authorized = false
				result.ErrorCode = "feature_not_allowed"
				result.ErrorMessage = fmt.Sprintf("You are not allowed to join this guild with the feature `%s` enabled.", feature)
				result.AuditMessage = fmt.Sprintf("feature `%s` not allowed in this guild", feature)
				return result
			}
		}
	}

	return result
}

func (a *DefaultLobbyAuthorizer) generateProfile(ctx context.Context, logger *zap.Logger, authCtx *LobbyAuthorizationContext) (*evr.ServerProfile, error) {
	profile, err := UserServerProfileFromParameters(ctx, logger, a.pipeline.db, a.pipeline.nk, authCtx.Params, authCtx.GroupID, []evr.Symbol{authCtx.LobbyParams.Mode}, authCtx.LobbyParams.Mode)
	if err != nil {
		return nil, err
	}

	if authCtx.GuildGroup.IsEnforcer(authCtx.UserID) && authCtx.Params.isGoldNameTag.Load() && profile.DeveloperFeatures == nil {
		profile.DeveloperFeatures = &evr.DeveloperFeatures{}
	}

	return profile, nil
}