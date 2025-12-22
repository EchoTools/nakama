package server

import (
	"time"
)

// GuildEnforcementEditEntry represents a single edit to an enforcement record.
// This is used for internal audit tracking and is not displayed to users.
type GuildEnforcementEditEntry struct {
	EditorUserID    string    `json:"editor_user_id"`    // Nakama user ID of the editor
	EditorDiscordID string    `json:"editor_discord_id"` // Discord ID of the editor
	EditedAt        time.Time `json:"edited_at"`         // When the edit was made

	// Previous values (before edit)
	PreviousExpiry         time.Time `json:"previous_expiry"`
	PreviousUserNoticeText string    `json:"previous_user_notice"`
	PreviousAuditorNotes   string    `json:"previous_auditor_notes"`

	// New values (after edit)
	NewExpiry         time.Time `json:"new_expiry"`
	NewUserNoticeText string    `json:"new_user_notice"`
	NewAuditorNotes   string    `json:"new_auditor_notes"`
}

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

	// EditLog contains the history of edits made to this record (internal only, not displayed)
	EditLog []GuildEnforcementEditEntry `json:"edit_log,omitempty"`
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
