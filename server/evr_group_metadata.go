package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type GroupMetadata struct {
	GuildID                            string            `json:"guild_id"`                      // The guild ID
	MinimumAccountAgeDays              int               `json:"minimum_account_age_days"`      // The minimum account age in days to be able to play echo on this guild's sessions
	MembersOnlyMatchmaking             bool              `json:"members_only_matchmaking"`      // Restrict matchmaking to members only (when this group is the active one)
	DisableCreateCommand               bool              `json:"disable_create_command"`        // Disable the public allocate command
	LogAlternateAccounts               bool              `json:"log_alternate_accounts"`        // Log alternate accounts
	EnforcersHaveGoldNames             bool              `json:"moderators_have_gold_names"`    // Enforcers have gold display names
	RoleMap                            GuildGroupRoles   `json:"roles"`                         // The roles text displayed on the main menu
	MatchmakingChannelIDs              map[string]string `json:"matchmaking_channel_ids"`       // The matchmaking channel IDs
	EnforcementNoticeChannelID         string            `json:"enforcement_notice_channel_id"` // The enforcement notice channel
	AuditChannelID                     string            `json:"audit_channel_id"`              // The audit channel
	ErrorChannelID                     string            `json:"error_channel_id"`              // The error channel
	CommandChannelID                   string            `json:"command_channel_id"`            // The command channel
	BlockVPNUsers                      bool              `json:"block_vpn_users"`               // Block VPN users
	FraudScoreThreshold                int               `json:"fraud_score_threshold"`         // The fraud score threshold
	AllowedFeatures                    []string          `json:"allowed_features"`              // Allowed features
	AlternateAccountNotificationExpiry time.Time         `json:"alt_notification_threshold"`    // Show alternate notifications newer than this time.
	EnableEnforcementCountInNames      bool              `json:"enable_enforcement_count_in_names"`
}

func NewGuildGroupMetadata(guildID string) *GroupMetadata {
	return &GroupMetadata{
		GuildID:               guildID,
		MatchmakingChannelIDs: make(map[string]string),
		AllowedFeatures:       make([]string, 0),
	}
}

func (g *GroupMetadata) MarshalMap() map[string]any {
	m := make(map[string]any)
	data, _ := json.Marshal(g)
	_ = json.Unmarshal(data, &m)
	return m
}

func (g *GroupMetadata) MarshalToMap() (map[string]interface{}, error) {

	guildGroupBytes, err := json.Marshal(g)
	if err != nil {
		return nil, err
	}

	var guildGroupMap map[string]interface{}
	err = json.Unmarshal(guildGroupBytes, &guildGroupMap)
	if err != nil {
		return nil, err
	}

	return guildGroupMap, nil
}

func GroupMetadataLoad(ctx context.Context, db *sql.DB, groupID string) (*GroupMetadata, error) {
	// Look for an existing account.
	query := "SELECT metadata FROM groups WHERE id = $1"
	var dbGuildMetadataJSON string
	var found = true
	var err error
	if err = db.QueryRowContext(ctx, query, groupID).Scan(&dbGuildMetadataJSON); err != nil {
		if err == sql.ErrNoRows {
			found = false
		} else {
			return nil, fmt.Errorf("error finding guild metadata: %w", err)
		}
	}
	if !found {
		return nil, status.Error(codes.NotFound, "guild ID not found")
	}

	metadata := &GroupMetadata{}
	if err := json.Unmarshal([]byte(dbGuildMetadataJSON), metadata); err != nil {
		return nil, status.Error(codes.Internal, "error unmarshalling guild metadata")
	}
	return metadata, nil
}

func GroupMetadataSave(ctx context.Context, db *sql.DB, groupID string, metadata *GroupMetadata) error {
	// Save the account.
	query := "UPDATE groups SET update_time = now(), metadata = $1 WHERE id = $2"
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return status.Error(codes.Internal, "error marshalling guild metadata")
	}
	if _, err := db.ExecContext(ctx, query, string(metadataJSON), groupID); err != nil {
		return fmt.Errorf("error saving guild metadata: %w", err)
	}
	return nil
}
