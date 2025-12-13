package server

import (
	"fmt"
	"strings"
	"time"
)

type GuildEnforcementRecord struct {
	ID                      string    `json:"id"`
	UserID                  string    `json:"user_id"`
	GroupID                 string    `json:"group_id"`
	EnforcerUserID          string    `json:"enforcer_user_id"`
	EnforcerDiscordID       string    `json:"enforcer_discord_id"`
	CreatedAt               time.Time `json:"created_at"`
	UpdatedAt               time.Time `json:"updated_at"`
	UserNoticeText          string    `json:"suspension_notice"`
	Expiry                  time.Time `json:"suspension_expiry"`
	CommunityValuesRequired bool      `json:"community_values_required"`
	AuditorNotes            string    `json:"notes"`
	AllowPrivateLobbies     bool      `json:"allow_private_lobbies"`
	RuleViolated            string    `json:"rule_violated,omitempty"`            // Specific rule that was violated (for standardized reasons)
	IsPubliclyVisible       bool      `json:"is_publicly_visible,omitempty"`      // Whether this record should appear in public logs
	DMNotificationSent      bool      `json:"dm_notification_sent,omitempty"`     // Tracks if DM notification was successfully sent
	DMNotificationAttempted time.Time `json:"dm_notification_attempted,omitempty"` // When DM notification was attempted
}

func (r GuildEnforcementRecord) IsSuspension() bool {
	return !r.Expiry.IsZero()
}

func (r GuildEnforcementRecord) SuspensionExcludesPrivateLobbies() bool {
	return r.AllowPrivateLobbies
}
func (r GuildEnforcementRecord) IsExpired() bool {
	return time.Now().After(r.Expiry)
}

func (r GuildEnforcementRecord) RequiresCommunityValues() bool {
	return r.CommunityValuesRequired
}

// StandardEnforcementRules defines common rule violation categories for standardized enforcement
var StandardEnforcementRules = []string{
	"Harassment or Hate Speech",
	"Cheating or Exploiting",
	"Toxic Behavior",
	"Inappropriate Content",
	"Spam or Advertising",
	"Team Griefing",
	"Match Abandonment",
	"Voice/Chat Abuse",
	"Impersonation",
	"Other Rule Violation",
}

// GetNotificationMessage returns a formatted message for DM notification to the user
func (r GuildEnforcementRecord) GetNotificationMessage(guildName string) string {
	var parts []string

	// Header
	if r.IsSuspension() {
		parts = append(parts, "⚠️ **Enforcement Action: Suspension**")
	} else {
		parts = append(parts, "⚠️ **Enforcement Action: Kick**")
	}

	// Guild context
	if guildName != "" {
		parts = append(parts, fmt.Sprintf("**Guild:** %s", guildName))
	}

	// Reason
	reasonLine := "**Reason:** "
	if r.RuleViolated != "" {
		reasonLine += r.RuleViolated
		if r.UserNoticeText != "" && r.UserNoticeText != r.RuleViolated {
			reasonLine += " - " + r.UserNoticeText
		}
	} else if r.UserNoticeText != "" {
		reasonLine += r.UserNoticeText
	} else {
		reasonLine += "Policy violation"
	}
	parts = append(parts, reasonLine)

	// Duration and expiry
	if r.IsSuspension() {
		duration := r.Expiry.Sub(r.CreatedAt)
		expiryTimestamp := fmt.Sprintf("<t:%d:R>", r.Expiry.UTC().Unix())
		parts = append(parts, fmt.Sprintf("**Duration:** %s (expires %s)", FormatDuration(duration), expiryTimestamp))

		// Access restrictions
		if r.AllowPrivateLobbies {
			parts = append(parts, "**Access:** Private lobbies allowed during suspension")
		} else {
			parts = append(parts, "**Access:** All gameplay suspended")
		}
	}

	// Community values requirement
	if r.CommunityValuesRequired {
		parts = append(parts, "")
		parts = append(parts, "⚠️ **Action Required:** You must review and acknowledge community values before returning.")
	}

	// Footer with instructions
	parts = append(parts, "")
	parts = append(parts, "ℹ️ For more details, use the `/whoami` command in Discord or in-game.")
	parts = append(parts, "If you believe this action was made in error, please contact a guild administrator.")

	return strings.Join(parts, "\n")
}
