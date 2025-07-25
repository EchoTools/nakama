package server

import (
	"time"
)

type GuildEnforcementRecord struct {
	ID                      string    `json:"id"`
	EnforcerUserID          string    `json:"enforcer_user_id"`
	EnforcerDiscordID       string    `json:"enforcer_discord_id"`
	CreatedAt               time.Time `json:"created_at"`
	UpdatedAt               time.Time `json:"updated_at"`
	UserNoticeText          string    `json:"suspension_notice"`
	SuspensionExpiry        time.Time `json:"suspension_expiry"`
	CommunityValuesRequired bool      `json:"community_values_required"`
	AuditorNotes            string    `json:"notes"`
	AllowPrivateLobbies     bool      `json:"allow_private_lobbies"`
}

func (r GuildEnforcementRecord) IsSuspension() bool {
	return !r.SuspensionExpiry.IsZero()
}

func (r GuildEnforcementRecord) IsLimitedAccess() bool {
	return r.IsActive() && r.AllowPrivateLobbies
}

func (r GuildEnforcementRecord) IsActive() bool {
	return !r.IsExpired()
}
func (r GuildEnforcementRecord) IsExpired() bool {
	return time.Now().After(r.SuspensionExpiry)
}

func (r GuildEnforcementRecord) RequiresCommunityValues() bool {
	return r.CommunityValuesRequired
}
