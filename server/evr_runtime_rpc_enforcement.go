package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/heroiclabs/nakama-common/runtime"
)

// EnforcementJournalQueryRequest represents the request to query enforcement logs
type EnforcementJournalQueryRequest struct {
	UserID   string   `json:"user_id"`             // Target user ID to query enforcement logs for
	GroupIDs []string `json:"group_ids,omitempty"` // Optional: Filter to specific guild group IDs
}

// EnforcementJournalEntry represents a single enforcement entry with privacy filters applied
type EnforcementJournalEntry struct {
	ID                      string       `json:"id"`
	UserID                  string       `json:"user_id"`
	GroupID                 string       `json:"group_id"`
	EnforcerUserID          *string      `json:"enforcer_user_id,omitempty"`    // Only included if requester has auditor+ privilege for this guild
	EnforcerDiscordID       *string      `json:"enforcer_discord_id,omitempty"` // Only included if requester has auditor+ privilege for this guild
	CreatedAt               TimeRFC3339  `json:"created_at"`
	UpdatedAt               TimeRFC3339  `json:"updated_at"`
	UserNoticeText          string       `json:"suspension_notice"`
	Expiry                  TimeRFC3339  `json:"suspension_expiry,omitempty"`
	CommunityValuesRequired bool         `json:"community_values_required"`
	AuditorNotes            *string      `json:"notes,omitempty"` // Only included if requester has auditor+ privilege for this guild
	AllowPrivateLobbies     bool         `json:"allow_private_lobbies"`
	IsVoided                bool         `json:"is_voided"`
	VoidedAt                *TimeRFC3339 `json:"voided_at,omitempty"`
	VoidedByUserID          *string      `json:"voided_by_user_id,omitempty"`
	VoidedByDiscordID       *string      `json:"voided_by_discord_id,omitempty"`
	VoidNotes               *string      `json:"void_notes,omitempty"`
}

// EnforcementJournalQueryResponse represents the filtered response
type EnforcementJournalQueryResponse struct {
	UserID  string                    `json:"user_id"`
	Entries []EnforcementJournalEntry `json:"entries"`
}

// EnforcementJournalQueryRPC handles querying enforcement logs with fine-grained privacy filters
//
// This RPC endpoint provides access to enforcement journal entries for a specified player.
// Access controls and privacy filters are applied based on:
//   - The requester's guild memberships
//   - The requester's privilege level (enforcer/auditor) in each guild
//
// Input (JSON):
//
//	{
//	  "user_id": "nakama-user-id",
//	  "group_ids": ["group-id-1", "group-id-2"]  // Optional: filter to specific guilds
//	}
//
// Response (JSON):
//
//	{
//	  "user_id": "nakama-user-id",
//	  "entries": [
//	    {
//	      "id": "record-id",
//	      "user_id": "nakama-user-id",
//	      "group_id": "group-id",
//	      "enforcer_user_id": "enforcer-nakama-id",  // Only if auditor+ for this guild
//	      "enforcer_discord_id": "discord-id",       // Only if auditor+ for this guild
//	      "created_at": "2024-01-01T00:00:00Z",
//	      "updated_at": "2024-01-01T00:00:00Z",
//	      "suspension_notice": "User-facing notice",
//	      "suspension_expiry": "2024-01-08T00:00:00Z",
//	      "community_values_required": false,
//	      "notes": "Internal notes",                 // Only if auditor+ for this guild
//	      "allow_private_lobbies": true,
//	      "is_voided": false
//	    }
//	  ]
//	}
//
// Error Cases:
//   - StatusUnauthenticated (16): No valid authentication
//   - StatusInvalidArgument (3): Invalid user_id or group_ids
//   - StatusPermissionDenied (7): Not authorized to view any enforcement logs
//   - StatusNotFound (5): User not found or has no enforcement journal
//
// Privacy Filters:
//   - Only entries for guilds the requester has access to are returned
//   - enforcer_user_id, enforcer_discord_id, and notes fields are only included
//     if the requester has auditor privilege in that specific guild
//   - Entries for guilds the requester doesn't have access to are excluded entirely
func EnforcementJournalQueryRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	// Parse the request
	var request EnforcementJournalQueryRequest
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return "", runtime.NewError("invalid request payload", StatusInvalidArgument)
	}

	// Validate required fields
	if request.UserID == "" {
		return "", runtime.NewError("user_id is required", StatusInvalidArgument)
	}

	// Get the caller's user ID from context
	callerID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", runtime.NewError("authentication required", StatusUnauthenticated)
	}

	// Check if caller has global private data access
	hasGlobalAccess := false
	var err error
	if hasGlobalAccess, err = CheckSystemGroupMembership(ctx, db, callerID, GroupGlobalPrivateDataAccess); err != nil {
		return "", runtime.NewError(fmt.Sprintf("failed to check global access: %s", err.Error()), StatusInternalError)
	}

	// Get caller's guild memberships and permissions
	var callerMemberships map[string]guildGroupPermissions
	if !hasGlobalAccess {
		vars, ok := ctx.Value(runtime.RUNTIME_CTX_VARS).(map[string]string)
		if !ok {
			vars = make(map[string]string)
		}
		callerMemberships, err = MembershipsFromSessionVars(vars)
		if err != nil {
			return "", runtime.NewError(fmt.Sprintf("failed to get caller memberships: %s", err.Error()), StatusInternalError)
		}
	}

	// Load the target user's enforcement journal
	journals, err := EnforcementJournalsLoad(ctx, nk, []string{request.UserID})
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("failed to load enforcement journal: %s", err.Error()), StatusInternalError)
	}

	journal, exists := journals[request.UserID]
	if !exists || journal == nil {
		// No enforcement journal exists for this user - return empty results
		response := EnforcementJournalQueryResponse{
			UserID:  request.UserID,
			Entries: []EnforcementJournalEntry{},
		}
		responseJSON, err := json.Marshal(response)
		if err != nil {
			return "", runtime.NewError("failed to marshal response", StatusInternalError)
		}
		return string(responseJSON), nil
	}

	// Determine which group IDs to include
	var targetGroupIDs []string
	if len(request.GroupIDs) > 0 {
		// Filter requested group IDs to only those the caller has access to
		for _, groupID := range request.GroupIDs {
			if hasGlobalAccess {
				targetGroupIDs = append(targetGroupIDs, groupID)
			} else if _, hasAccess := callerMemberships[groupID]; hasAccess {
				targetGroupIDs = append(targetGroupIDs, groupID)
			}
		}
	} else {
		// No specific group IDs requested - include all groups caller has access to
		if hasGlobalAccess {
			// Global access - include all groups in the journal
			for groupID := range journal.RecordsByGroupID {
				targetGroupIDs = append(targetGroupIDs, groupID)
			}
			// Also include groups with voids
			for groupID := range journal.VoidsByRecordIDByGroupID {
				found := false
				for _, gid := range targetGroupIDs {
					if gid == groupID {
						found = true
						break
					}
				}
				if !found {
					targetGroupIDs = append(targetGroupIDs, groupID)
				}
			}
		} else {
			// Limited access - include only groups the caller is a member of
			for groupID := range callerMemberships {
				targetGroupIDs = append(targetGroupIDs, groupID)
			}
		}
	}

	// Check if caller has access to at least one group
	if len(targetGroupIDs) == 0 {
		return "", runtime.NewError("no access to enforcement logs for the requested guilds", StatusPermissionDenied)
	}

	// Build the filtered response
	var entries []EnforcementJournalEntry

	for _, groupID := range targetGroupIDs {
		records := journal.GroupRecords(groupID)
		voids := journal.GroupVoids(groupID)

		// Determine if caller has auditor privilege for this guild
		hasAuditorPrivilege := hasGlobalAccess
		if !hasGlobalAccess {
			if perms, ok := callerMemberships[groupID]; ok {
				hasAuditorPrivilege = perms.IsAuditor
			}
		}

		for _, record := range records {
			entry := EnforcementJournalEntry{
				ID:                      record.ID,
				UserID:                  record.UserID,
				GroupID:                 record.GroupID,
				CreatedAt:               TimeRFC3339(record.CreatedAt),
				UpdatedAt:               TimeRFC3339(record.UpdatedAt),
				UserNoticeText:          record.UserNoticeText,
				CommunityValuesRequired: record.CommunityValuesRequired,
				AllowPrivateLobbies:     record.AllowPrivateLobbies,
				IsVoided:                journal.IsVoid(groupID, record.ID),
			}

			// Set expiry if it's a suspension
			if !record.Expiry.IsZero() {
				expiry := TimeRFC3339(record.Expiry)
				entry.Expiry = expiry
			}

			// Include privileged fields only if caller has auditor privilege
			if hasAuditorPrivilege {
				entry.EnforcerUserID = &record.EnforcerUserID
				entry.EnforcerDiscordID = &record.EnforcerDiscordID
				if record.AuditorNotes != "" {
					entry.AuditorNotes = &record.AuditorNotes
				}
			}

			// Include void information if voided
			if voidInfo, found := voids[record.ID]; found {
				voidedAt := TimeRFC3339(voidInfo.VoidedAt)
				entry.VoidedAt = &voidedAt

				// Include void details only if caller has auditor privilege
				if hasAuditorPrivilege {
					entry.VoidedByUserID = &voidInfo.AuthorID
					entry.VoidedByDiscordID = &voidInfo.AuthorDiscordID
					if voidInfo.Notes != "" {
						entry.VoidNotes = &voidInfo.Notes
					}
				}
			}

			entries = append(entries, entry)
		}
	}

	// Prepare the response
	response := EnforcementJournalQueryResponse{
		UserID:  request.UserID,
		Entries: entries,
	}

	responseJSON, err := json.Marshal(response)
	if err != nil {
		return "", runtime.NewError("failed to marshal response", StatusInternalError)
	}

	return string(responseJSON), nil
}
