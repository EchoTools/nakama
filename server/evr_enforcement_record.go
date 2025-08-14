package server

import (
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
