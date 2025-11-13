package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

// EnforcementJournalEntry - single enforcement record for API responses
type EnforcementJournalEntry struct {
	ID                      string `json:"id"`
	UserID                  string `json:"user_id"`
	GroupID                 string `json:"group_id"`
	EnforcerUserID          string `json:"enforcer_user_id"`
	EnforcerDiscordID       string `json:"enforcer_discord_id"`
	CreatedAt               int64  `json:"created_at_unix"`
	UpdatedAt               int64  `json:"updated_at_unix"`
	UserNoticeText          string `json:"suspension_notice"`
	Expiry                  int64  `json:"suspension_expiry_unix"`
	CommunityValuesRequired bool   `json:"community_values_required"`
	AuditorNotes            string `json:"notes"`
	AllowPrivateLobbies     bool   `json:"allow_private_lobbies"`
	IsVoided                bool   `json:"is_voided"`
	VoidedBy                string `json:"voided_by,omitempty"`
	VoidedAt                int64  `json:"voided_at_unix,omitempty"`
	VoidNotes               string `json:"void_notes,omitempty"`
}

// ListEnforcementJournalEntriesByGuildsRequest is the request payload for listing enforcement journal entries
type ListEnforcementJournalEntriesByGuildsRequest struct {
	GuildIDs []string `json:"guild_ids"`
}

// ListEnforcementJournalEntriesByGuildsResponse contains enforcement entries for requested guilds
type ListEnforcementJournalEntriesByGuildsResponse struct {
	Entries []EnforcementJournalEntry `json:"entries"`
}

// ListEnforcementJournalEntriesByGuilds is an RPC endpoint to retrieve enforcement journal entries
// for specified guilds. Access is limited to guild auditors or global operators.
// It includes entries from inherited guilds as defined in guild group metadata.
//
// Authorization:
// - Guild auditors can view entries for their guild
// - Global operators can view entries for any guild
//
// Parameters (JSON body):
// - guild_ids: Array of guild IDs to retrieve enforcement entries for (can be guild IDs or group IDs)
//
// Returns:
// - entries: Array of enforcement journal entries, including from inherited guilds
//
// Error codes:
// - 3 (InvalidArgument): Missing or invalid guild IDs
// - 5 (NotFound): Guild group not found
// - 7 (PermissionDenied): User lacks required permissions (not an auditor or operator)
// - 13 (Internal): Internal server error

func ListEnforcementJournalEntriesByGuilds(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	// Get the authenticated user
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", runtime.NewError("authentication required", StatusUnauthenticated)
	}

	// Unmarshal the request payload
	req := &ListEnforcementJournalEntriesByGuildsRequest{}
	if err := json.Unmarshal([]byte(payload), req); err != nil {
		return "", runtime.NewError("failed to unmarshal request: "+err.Error(), StatusInvalidArgument)
	}

	if len(req.GuildIDs) == 0 {
		return "", runtime.NewError("guild_ids must not be empty", StatusInvalidArgument)
	}

	// Convert guild IDs to group IDs and resolve all guilds
	groupIDs := make(map[string]string) // map[groupID]guildID
	inheritedGroupIDs := make(map[string]bool)
	isGlobalOperator := false

	// Check if user is a global operator
	isOperator, err := CheckSystemGroupMembership(ctx, db, userID, GroupGlobalOperators)
	if err != nil {
		logger.WithField("error", err).Warn("Failed to check global operator status")
		return "", runtime.NewError("failed to check permissions", StatusInternalError)
	}
	isGlobalOperator = isOperator

	// Load guild groups for the user
	userGuildGroups, err := GuildUserGroupsList(ctx, nk, nil, userID)
	if err != nil {
		logger.WithField("error", err).Warn("Failed to load user guild groups")
		return "", runtime.NewError("failed to load user guild groups", StatusInternalError)
	}

	// Process each requested guild ID
	for _, guildID := range req.GuildIDs {
		// Try to resolve guild ID to group ID
		groupID := guildID
		if uuid.FromStringOrNil(groupID) == uuid.Nil {
			// Assume it's a guild ID and convert to group ID
			resolvedGroupID, err := GetGroupIDByGuildID(ctx, db, guildID)
			if err != nil {
				logger.WithField("guildID", guildID).WithField("error", err).Warn("Failed to resolve guild ID")
				continue
			}
			groupID = resolvedGroupID
		}

		// Check permissions, user must be an auditor in the guild or a global operator
		if !isGlobalOperator {
			if gg, ok := userGuildGroups[groupID]; !ok || !gg.IsAuditor(userID) {
				return "", runtime.NewError("permission denied: user is not an auditor in the requested guild", StatusPermissionDenied)
			}
		}

		groupIDs[groupID] = guildID
		inheritedGroupIDs[groupID] = true

		// If the user is authorized, load the guild group to get inherited guild IDs
		gg, err := GuildGroupLoad(ctx, nk, groupID)
		if err != nil {
			logger.WithField("groupID", groupID).WithField("error", err).Warn("Failed to load guild group")
			continue
		}

		// Add inherited guild IDs
		for _, inheritedGroupID := range gg.SuspensionInheritanceGroupIDs {
			inheritedGroupIDs[inheritedGroupID] = true
		}
	}

	if len(groupIDs) == 0 {
		return "", runtime.NewError("no valid guild IDs provided", StatusInvalidArgument)
	}

	// Query enforcement journal entries for the user for all relevant groups (direct + inherited)
	groupIDList := make([]string, 0, len(inheritedGroupIDs))
	for gid := range inheritedGroupIDs {
		groupIDList = append(groupIDList, gid)
	}

	// Load enforcement journal for the user
	journals, err := EnforcementJournalsLoad(ctx, nk, []string{userID})
	if err != nil {
		logger.WithField("error", err).Warn("Failed to load enforcement journals")
		return "", runtime.NewError("failed to load enforcement journals", StatusInternalError)
	}

	// Collect entries from the journal for the requested groups
	entries := make([]EnforcementJournalEntry, 0)

	if journal, ok := journals[userID]; ok {
		for _, groupID := range groupIDList {
			records := journal.GroupRecords(groupID)
			voids := journal.GroupVoids(groupID)

			for _, record := range records {
				entry := EnforcementJournalEntry{
					ID:                      record.ID,
					UserID:                  record.UserID,
					GroupID:                 record.GroupID,
					EnforcerUserID:          record.EnforcerUserID,
					EnforcerDiscordID:       record.EnforcerDiscordID,
					CreatedAt:               record.CreatedAt.Unix(),
					UpdatedAt:               record.UpdatedAt.Unix(),
					UserNoticeText:          record.UserNoticeText,
					Expiry:                  record.Expiry.Unix(),
					CommunityValuesRequired: record.CommunityValuesRequired,
					AuditorNotes:            record.AuditorNotes,
					AllowPrivateLobbies:     record.AllowPrivateLobbies,
				}

				// Check if this record is voided
				if void, ok := voids[record.ID]; ok {
					entry.IsVoided = true
					entry.VoidedBy = void.AuthorID
					entry.VoidedAt = void.VoidedAt.Unix()
					entry.VoidNotes = void.Notes
				}

				entries = append(entries, entry)
			}
		}
	}

	// Build and marshal the response
	resp := ListEnforcementJournalEntriesByGuildsResponse{
		Entries: entries,
	}

	b, err := json.Marshal(resp)
	if err != nil {
		logger.WithField("error", err).Error("Failed to marshal response")
		return "", runtime.NewError("failed to marshal response", StatusInternalError)
	}

	return string(b), nil
}
