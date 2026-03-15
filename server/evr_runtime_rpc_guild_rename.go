package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

// GuildPlayerRenameRequest represents the request payload for the guild/player/rename RPC.
type GuildPlayerRenameRequest struct {
	TargetUserID   string `json:"target_user_id"`   // User ID of the player to rename (required)
	GroupID        string `json:"group_id"`          // Guild group ID (required)
	NewDisplayName string `json:"new_display_name"`  // New display name (required)
	IsLocked       bool   `json:"is_locked"`         // Whether to lock the name (prevent player from changing it)
}

// GuildPlayerRenameResponse represents the response from the guild/player/rename RPC.
type GuildPlayerRenameResponse struct {
	Success  bool   `json:"success"`
	OldName  string `json:"old_name"`
	NewName  string `json:"new_name"`
	IsLocked bool   `json:"is_locked"`
}

// GuildPlayerRenameRPC handles renaming a player's per-guild display name.
// Permission: caller must be a global operator OR an enforcer in the specified guild.
func GuildPlayerRenameRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	var request GuildPlayerRenameRequest
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return "", runtime.NewError("Invalid request payload", StatusInvalidArgument)
	}

	// Get the caller's user ID from context
	callerID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || callerID == "" {
		return "", runtime.NewError("User ID not found in context", StatusUnauthenticated)
	}

	// Validate required fields
	if request.TargetUserID == "" {
		return "", runtime.NewError("target_user_id is required", StatusInvalidArgument)
	}
	if request.GroupID == "" {
		return "", runtime.NewError("group_id is required", StatusInvalidArgument)
	}
	if request.NewDisplayName == "" {
		return "", runtime.NewError("new_display_name is required", StatusInvalidArgument)
	}

	// Validate UUID formats
	targetUUID, err := uuid.FromString(request.TargetUserID)
	if err != nil {
		return "", runtime.NewError("Invalid target_user_id format", StatusInvalidArgument)
	}
	targetUserID := targetUUID.String()

	groupUUID, err := uuid.FromString(request.GroupID)
	if err != nil {
		return "", runtime.NewError("Invalid group_id format", StatusInvalidArgument)
	}
	groupID := groupUUID.String()

	// Check permissions: global operator OR guild enforcer
	isGlobalOperator := false
	if perms := PermissionsFromContext(ctx); perms != nil {
		isGlobalOperator = perms.IsGlobalOperator
	} else {
		isGlobalOperator, err = CheckSystemGroupMembership(ctx, db, callerID, GroupGlobalOperators)
		if err != nil {
			logger.Error("Failed to check operator status", zap.Error(err))
			return "", runtime.NewError("Failed to check permissions", StatusInternalError)
		}
	}

	if !isGlobalOperator {
		// Check if the caller is an enforcer in the target guild
		gg, err := GuildGroupLoad(ctx, nk, groupID)
		if err != nil {
			logger.Error("Failed to load guild group", zap.Error(err))
			return "", runtime.NewError("Failed to load guild group", StatusInternalError)
		}
		if !gg.IsEnforcer(callerID) {
			return "", runtime.NewError("Must be a guild enforcer or global operator", StatusPermissionDenied)
		}
	}

	// Sanitize and validate the new display name
	sanitizedName := sanitizeDisplayName(request.NewDisplayName)
	if sanitizedName == "" {
		return "", runtime.NewError("Invalid display name: must contain at least one letter and be between 1-24 characters", StatusInvalidArgument)
	}

	// Load the target user's EVR profile
	evrProfile, err := EVRProfileLoad(ctx, nk, targetUserID)
	if err != nil {
		logger.Error("Failed to load EVR profile", zap.Error(err), zap.String("user_id", targetUserID))
		return "", runtime.NewError("Failed to load player profile", StatusInternalError)
	}
	if evrProfile == nil {
		return "", runtime.NewError("Player profile not found", StatusNotFound)
	}

	// Get the old display name for this guild
	oldIGN := evrProfile.GetGroupIGNData(groupID)
	oldName := oldIGN.DisplayName
	if oldName == "" {
		oldName = evrProfile.Username()
	}

	// Set the new per-guild display name with override and lock flags
	evrProfile.SetGroupIGNData(groupID, GroupInGameName{
		GroupID:     groupID,
		DisplayName: sanitizedName,
		IsOverride:  true,
		IsLocked:    request.IsLocked,
	})

	// Persist the profile (writes storage + invalidates ServerProfile cache)
	if err := EVRProfileUpdate(ctx, nk, targetUserID, evrProfile); err != nil {
		logger.Error("Failed to update EVR profile", zap.Error(err), zap.String("user_id", targetUserID))
		return "", runtime.NewError("Failed to update player profile", StatusInternalError)
	}

	// Record the change in display name history
	if err := DisplayNameHistoryUpdate(ctx, nk, targetUserID, groupID, sanitizedName, evrProfile.Username(), false); err != nil {
		logger.Error("Failed to update display name history", zap.Error(err), zap.String("user_id", targetUserID))
		// Non-fatal: continue with success response
	}

	// Get caller and target discord IDs for the audit log
	callerAccount, err := nk.AccountGetId(ctx, callerID)
	callerDiscordID := callerID
	if err == nil && callerAccount != nil && callerAccount.CustomId != "" {
		callerDiscordID = callerAccount.CustomId
	}

	targetDiscordID := targetUserID
	if did := evrProfile.DiscordID(); did != "" {
		targetDiscordID = did
	}

	lockStr := "unlocked"
	if request.IsLocked {
		lockStr = "locked"
	}

	// Send audit log to guild channel if possible, otherwise global
	auditMsg := fmt.Sprintf("Guild player rename: <@%s> renamed <@%s> from `%s` to `%s` (%s) in group %s",
		callerDiscordID, targetDiscordID, oldName, sanitizedName, lockStr, groupID)

	gg, err := GuildGroupLoad(ctx, nk, groupID)
	if err == nil && gg != nil {
		if _, err := AuditLogSendGuild(dg, gg, auditMsg); err != nil {
			logger.Warn("Failed to send guild audit log", zap.Error(err))
		}
	} else {
		if err := AuditLogSend(dg, ServiceSettings().ServiceAuditChannelID, auditMsg); err != nil {
			logger.Warn("Failed to send audit log", zap.Error(err))
		}
	}

	logger.Info("Guild player rename completed",
		zap.String("caller_id", callerID),
		zap.String("target_user_id", targetUserID),
		zap.String("group_id", groupID),
		zap.String("old_name", oldName),
		zap.String("new_name", sanitizedName),
		zap.Bool("is_locked", request.IsLocked),
	)

	response := GuildPlayerRenameResponse{
		Success:  true,
		OldName:  oldName,
		NewName:  sanitizedName,
		IsLocked: request.IsLocked,
	}

	data, err := json.Marshal(response)
	if err != nil {
		logger.Error("Failed to marshal response", zap.Error(err))
		return "", runtime.NewError("Failed to create response", StatusInternalError)
	}

	return string(data), nil
}

// GuildPlayerIGNResponse represents the response from the guild/player/ign RPC.
type GuildPlayerIGNResponse struct {
	GroupIGNs map[string]GroupInGameName `json:"group_igns"`
}

// GuildPlayerIGNRPC returns per-guild IGN data for a target player.
// Permission: caller must be a global operator OR an enforcer in at least one of the target's guilds.
func GuildPlayerIGNRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	callerID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || callerID == "" {
		return "", runtime.NewError("User ID not found in context", StatusUnauthenticated)
	}

	// Parse user_id from query parameters (GET) or payload (POST)
	var targetUserID string

	if queryParams, ok := ctx.Value(runtime.RUNTIME_CTX_QUERY_PARAMS).(map[string][]string); ok {
		if userIDs, ok := queryParams["user_id"]; ok && len(userIDs) > 0 {
			targetUserID = userIDs[0]
		}
	}

	if targetUserID == "" && payload != "" {
		var req struct {
			UserID string `json:"user_id"`
		}
		if err := json.Unmarshal([]byte(payload), &req); err == nil {
			targetUserID = req.UserID
		}
	}

	if targetUserID == "" {
		return "", runtime.NewError("user_id is required", StatusInvalidArgument)
	}

	targetUUID, err := uuid.FromString(targetUserID)
	if err != nil {
		return "", runtime.NewError("Invalid user_id format", StatusInvalidArgument)
	}
	targetUserID = targetUUID.String()

	// Check permissions: global operator OR enforcer in at least one of the target's guilds
	isGlobalOperator := false
	if perms := PermissionsFromContext(ctx); perms != nil {
		isGlobalOperator = perms.IsGlobalOperator
	} else {
		isGlobalOperator, err = CheckSystemGroupMembership(ctx, db, callerID, GroupGlobalOperators)
		if err != nil {
			logger.Error("Failed to check operator status", zap.Error(err))
			return "", runtime.NewError("Failed to check permissions", StatusInternalError)
		}
	}

	if !isGlobalOperator {
		// Get the target user's guild memberships and check if caller is enforcer in any
		userGroups, _, err := nk.UserGroupsList(ctx, targetUserID, 100, nil, "")
		if err != nil {
			logger.Error("Failed to list user groups", zap.Error(err))
			return "", runtime.NewError("Failed to check guild memberships", StatusInternalError)
		}

		hasPermission := false
		for _, ug := range userGroups {
			gg, err := GuildGroupLoad(ctx, nk, ug.Group.Id)
			if err != nil {
				continue
			}
			if gg.IsEnforcer(callerID) {
				hasPermission = true
				break
			}
		}
		if !hasPermission {
			return "", runtime.NewError("Must be a guild enforcer or global operator", StatusPermissionDenied)
		}
	}

	// Load the target user's EVR profile
	evrProfile, err := EVRProfileLoad(ctx, nk, targetUserID)
	if err != nil {
		logger.Error("Failed to load EVR profile", zap.Error(err), zap.String("user_id", targetUserID))
		return "", runtime.NewError("Failed to load player profile", StatusInternalError)
	}
	if evrProfile == nil {
		return "", runtime.NewError("Player profile not found", StatusNotFound)
	}

	// Build the response with the per-guild IGN map
	groupIGNs := make(map[string]GroupInGameName)
	if evrProfile.InGameNames != nil {
		for gid, ign := range evrProfile.InGameNames {
			groupIGNs[gid] = ign
		}
	}

	response := GuildPlayerIGNResponse{
		GroupIGNs: groupIGNs,
	}

	data, err := json.Marshal(response)
	if err != nil {
		logger.Error("Failed to marshal response", zap.Error(err))
		return "", runtime.NewError("Failed to create response", StatusInternalError)
	}

	return string(data), nil
}
