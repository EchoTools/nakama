package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// EnforcementKickRequest represents the request payload for the kick/enforcement RPC
type EnforcementKickRequest struct {
	GroupID                string `json:"group_id"`                           // Guild group ID to add the enforcement to (required)
	TargetUserID           string `json:"target_user_id"`                     // User ID of the player being kicked or suspended (required)
	EnforcerID             string `json:"enforcer_id,omitempty"`              // Enforcer user ID (only allowed for global operators, defaults to caller)
	UserNotice             string `json:"user_notice"`                        // Reason for the kick (displayed to the user; 48 character max)
	SuspensionDuration     string `json:"suspension_duration,omitempty"`      // Suspension duration (e.g. 1m, 2h, 3d, 4w)
	ModeratorNotes         string `json:"moderator_notes,omitempty"`          // Notes for the audit log (other moderators)
	AllowPrivateLobbies    bool   `json:"allow_private_lobbies,omitempty"`    // Limit the user to only joining private lobbies
	RequireCommunityValues bool   `json:"require_community_values,omitempty"` // Require the user to accept the community values before they can rejoin
}

// EnforcementKickResponse represents the response from the kick/enforcement RPC
type EnforcementKickResponse struct {
	Success          bool     `json:"success"`
	Message          string   `json:"message"`
	SessionsKicked   int      `json:"sessions_kicked"`
	Actions          []string `json:"actions"`
	SuspensionExpiry int64    `json:"suspension_expiry,omitempty"` // Unix timestamp
}

// EnforcementKickRPC is the RPC handler for kicking and adding enforcement to players
func EnforcementKickRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	var request EnforcementKickRequest
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return "", runtime.NewError("Invalid request payload", StatusInvalidArgument)
	}

	// Get the caller's user ID from context
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("User ID not found in context", StatusUnauthenticated)
	}

	// Validate required fields
	if request.GroupID == "" {
		return "", runtime.NewError("group_id is required", StatusInvalidArgument)
	}

	if request.TargetUserID == "" {
		return "", runtime.NewError("target_user_id is required", StatusInvalidArgument)
	}

	// Disallow self-kick
	if request.TargetUserID == userID {
		return "", runtime.NewError("You cannot kick yourself", StatusInvalidArgument)
	}

	// Validate user notice
	if request.UserNotice == "" {
		return "", runtime.NewError("user_notice is required", StatusInvalidArgument)
	}
	if len(request.UserNotice) > 48 {
		return "", runtime.NewError("user_notice must be less than 48 characters", StatusInvalidArgument)
	}

	groupID := request.GroupID

	// Check if the caller is a global operator
	isGlobalOperator, err := CheckSystemGroupMembership(ctx, db, userID, GroupGlobalOperators)
	if err != nil {
		logger.Error("Failed to check global operator status", zap.Error(err))
		return "", runtime.NewError("Failed to check permissions", StatusInternalError)
	}

	// Check if the caller is a global bot
	isGlobalBot, err := CheckSystemGroupMembership(ctx, db, userID, GroupGlobalBots)
	if err != nil {
		logger.Error("Failed to check global bot status", zap.Error(err))
		return "", runtime.NewError("Failed to check permissions", StatusInternalError)
	}

	// Determine enforcer user ID
	enforcerUserID := userID
	if request.EnforcerID != "" {
		if !isGlobalOperator && !isGlobalBot {
			return "", runtime.NewError("Only global operators or global bots can specify an enforcer_id", StatusPermissionDenied)
		}
		enforcerUserID = request.EnforcerID
	}

	// Target user ID is always from the request
	targetUserID := request.TargetUserID

	// Load guild group to check permissions
	gg, err := GuildGroupLoad(ctx, nk, groupID)
	if err != nil {
		logger.Error("Failed to load guild group", zap.Error(err))
		return "", runtime.NewError("Failed to load guild group", StatusInternalError)
	}

	// Check if the caller is an enforcer
	isEnforcer := gg.IsEnforcer(userID)
	if !isEnforcer && !isGlobalOperator {
		return "", runtime.NewError("You must be a guild enforcer or global operator to use this command", StatusPermissionDenied)
	}

	// Parse suspension duration if provided
	var suspensionDuration time.Duration
	var suspensionExpiry time.Time
	if request.SuspensionDuration != "" {
		suspensionDuration, err = parseSuspensionDuration(request.SuspensionDuration)
		if err != nil {
			return "", runtime.NewError(fmt.Sprintf("Duration parse error: %s. Use a number followed by m, h, d, or w (e.g., 30m, 1h, 2d, 1w) or compound durations (e.g., 2h30m)", err.Error()), StatusInvalidArgument)
		}

		if suspensionDuration == 0 {
			// Zero duration means void existing suspension
			suspensionExpiry = time.Now()
		} else {
			suspensionExpiry = time.Now().Add(suspensionDuration)
		}
	}

	// Get the user's active presences
	presences, err := nk.StreamUserList(StreamModeService, targetUserID, "", StreamLabelMatchService, false, true)
	if err != nil {
		logger.Error("Failed to list user streams", zap.Error(err))
		return "", runtime.NewError("Failed to get user sessions", StatusInternalError)
	}

	var (
		cnt                   = 0
		actions               = make([]string, 0)
		voidActiveSuspensions = !suspensionExpiry.IsZero() && time.Now().After(suspensionExpiry)
		addSuspension         = !suspensionExpiry.IsZero() && time.Now().Before(suspensionExpiry)
		recordsByGroupID      = make(map[string][]GuildEnforcementRecord, 1)
		voids                 = make(map[string]GuildEnforcementRecordVoid, 0)
		kickPlayer            = false
	)

	// Determine if we should kick the player
	if !voidActiveSuspensions {
		kickPlayer = true
	}

	// Load or create enforcement journal
	journal := NewGuildEnforcementJournal(targetUserID)
	if err := StorableRead(ctx, nk, targetUserID, journal, false); err != nil && status.Code(err) != codes.NotFound {
		logger.Error("Failed to read enforcement journal", zap.Error(err))
		return "", runtime.NewError("Failed to read enforcement journal", StatusInternalError)
	}

	// Add suspension record or void existing suspensions
	if addSuspension {
		// Get enforcer's Discord ID for audit log
		enforcerDiscordID := ""
		account, err := nk.AccountGetId(ctx, enforcerUserID)
		if err != nil {
			logger.Error("Failed to load enforcer account for Discord ID", zap.Error(err), zap.String("enforcer_user_id", enforcerUserID))
		} else if account != nil && account.CustomId != "" {
			enforcerDiscordID = account.CustomId
		}

		// Add a new suspension record
		actions = append(actions, fmt.Sprintf("suspension expires <t:%d:R>", suspensionExpiry.UTC().Unix()))
		record := journal.AddRecord(groupID, enforcerUserID, enforcerDiscordID, request.UserNotice, request.ModeratorNotes, request.RequireCommunityValues, request.AllowPrivateLobbies, suspensionDuration)
		recordsByGroupID[groupID] = append(recordsByGroupID[groupID], record)
	} else if voidActiveSuspensions {
		// Void active suspensions
		currentGroupID := groupID
		groupIDs := []string{currentGroupID}

		// Add inherited groups
		if gg.SuspensionInheritanceGroupIDs != nil {
			groupIDs = append(groupIDs, gg.SuspensionInheritanceGroupIDs...)
		}

		// Void any active suspensions for this group and any inherited groups
		for _, gID := range groupIDs {
			if recordsByGroupID[gID] == nil {
				recordsByGroupID[gID] = make([]GuildEnforcementRecord, 0)
			}

			for _, record := range journal.GroupRecords(gID) {
				// Ignore expired or already-voided records
				if record.IsExpired() || journal.IsVoid(gID, record.ID) || journal.IsVoid(currentGroupID, record.ID) {
					continue
				}
				actions = append(actions, fmt.Sprintf("suspension voided: <t:%d:R> (expires <t:%d:R>): %s", record.CreatedAt.Unix(), record.Expiry.Unix(), record.UserNoticeText))

				recordsByGroupID[currentGroupID] = append(recordsByGroupID[currentGroupID], record)

				details := request.UserNotice
				if request.ModeratorNotes != "" {
					details += "\n" + request.ModeratorNotes
				}
				void := journal.VoidRecord(currentGroupID, record.ID, enforcerUserID, "", details)
				voids[void.RecordID] = void
			}
		}
	}

	// Save the enforcement journal and sync to profile
	if err := SyncJournalAndProfile(ctx, nk, targetUserID, journal); err != nil {
		logger.Error("Failed to write enforcement data", zap.Error(err))
		return "", runtime.NewError("Failed to save enforcement data", StatusInternalError)
	}

	// Kick the player from active sessions
	if kickPlayer {
		for _, p := range presences {
			// Match only the target user
			if p.GetUserId() != targetUserID {
				continue
			}

			label, _ := MatchLabelByID(ctx, nk, MatchIDFromStringOrNil(p.GetStatus()))
			if label == nil {
				continue
			}

			// Don't kick game servers
			if label.GameServer.SessionID.String() == p.GetSessionId() {
				continue
			}

			permissions := make([]string, 0)

			// Check if the user is the match owner of a private match
			if label.SpawnedBy == userID && label.IsPrivate() {
				permissions = append(permissions, "match owner")
			}

			// Check if the user is the game server operator
			if label.GameServer.OperatorID.String() == userID {
				permissions = append(permissions, "game server operator")
			}

			if isGlobalOperator {
				permissions = append(permissions, "global operator")
			}

			if isEnforcer && label.GetGroupID().String() == groupID {
				permissions = append(permissions, "enforcer")
			}

			if len(permissions) == 0 {
				actions = append(actions, "user's match is not from this guild")
				continue
			}

			// Kick the player from the match
			if err := KickPlayerFromMatch(ctx, nk, label.ID, targetUserID); err != nil {
				actions = append(actions, fmt.Sprintf("failed to kick player from [%s] (error: %s)", label.Mode.String(), err.Error()))
				continue
			}

			actions = append(actions, fmt.Sprintf("kicked from [%s] session. (%s) [%s]", label.Mode.String(), request.UserNotice, fmt.Sprintf("%v", permissions)))
			cnt++
		}

		// Disconnect the user if they are a global operator or enforcer
		if isGlobalOperator || isEnforcer {
			go func() {
				time.Sleep(5 * time.Second)
				// Just disconnect the user, wholesale
				if count, err := DisconnectUserID(ctx, nk, targetUserID, true, true, false); err != nil {
					logger.Warn("Failed to disconnect user", zap.Error(err))
				} else if count > 0 {
					logger.Info("Disconnected user from login/match service", zap.Int("sessions", count), zap.String("target_user_id", targetUserID))
				}
			}()
		}

		if cnt == 0 {
			actions = append(actions, "no active sessions found")
		}
	}

	// Prepare response
	response := EnforcementKickResponse{
		Success:        true,
		SessionsKicked: cnt,
		Actions:        actions,
		Message:        fmt.Sprintf("Enforcement action completed. %d sessions kicked.", cnt),
	}

	if !suspensionExpiry.IsZero() && time.Now().Before(suspensionExpiry) {
		response.SuspensionExpiry = suspensionExpiry.Unix()
	}

	responseData, err := json.Marshal(response)
	if err != nil {
		logger.Error("Failed to marshal response", zap.Error(err))
		return "", runtime.NewError("Failed to create response", StatusInternalError)
	}

	return string(responseData), nil
}

type EnforcementJournalListRequest struct {
	GroupID string `json:"group_id"`
}

type EnforcementJournalListResponse struct {
	Journals []GuildEnforcementJournal `json:"journals"`
}

func EnforcementJournalListRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	var request EnforcementJournalListRequest
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return "", runtime.NewError("Invalid request payload", StatusInvalidArgument)
	}

	if request.GroupID == "" {
		return "", runtime.NewError("group_id is required", StatusInvalidArgument)
	}

	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("User ID not found in context", StatusUnauthenticated)
	}

	// Check if the caller is a global operator
	isGlobalOperator, err := CheckSystemGroupMembership(ctx, db, userID, GroupGlobalOperators)
	if err != nil {
		logger.Error("Failed to check global operator status", zap.Error(err))
		return "", runtime.NewError("Failed to check permissions", StatusInternalError)
	}

	if !isGlobalOperator {
		// Check if the caller is the guild owner
		isOwner, err := checkGroupOwner(ctx, db, userID, request.GroupID)
		if err != nil {
			logger.Error("Failed to check guild ownership", zap.Error(err))
			return "", runtime.NewError("Failed to check permissions", StatusInternalError)
		}
		if !isOwner {
			return "", runtime.NewError("Permission denied: not a guild owner", StatusPermissionDenied)
		}
	}

	// List all journals containing this group_id
	query := fmt.Sprintf("+value.guild_ids:%s", request.GroupID)

	journals := make([]GuildEnforcementJournal, 0)
	cursor := ""
	for {
		// SystemUserID is used as the caller for storage index list to bypass permissions,
		// as we are manually checking permissions above.
		objs, nextCursor, err := nk.StorageIndexList(ctx, SystemUserID, StorageIndexEnforcementJournal, query, 100, nil, cursor)
		if err != nil {
			logger.Error("Failed to list enforcement journals", zap.Error(err))
			return "", runtime.NewError("Failed to list enforcement journals", StatusInternalError)
		}

		for _, obj := range objs.Objects {
			journal, err := GuildEnforcementJournalFromStorageObject(obj)
			if err != nil {
				logger.Warn("Failed to unmarshal enforcement journal", zap.Error(err), zap.String("user_id", obj.GetUserId()))
				continue
			}

			// Filter the journal to only include records for the requested group
			filteredJournal := *journal // Shallow copy
			filteredJournal.RecordsByGroupID = make(map[string][]GuildEnforcementRecord)
			filteredJournal.VoidsByRecordIDByGroupID = make(map[string]map[string]GuildEnforcementRecordVoid)
			filteredJournal.GuildIDs = []string{request.GroupID}

			hasRecords := false
			if records, ok := journal.RecordsByGroupID[request.GroupID]; ok && len(records) > 0 {
				filteredJournal.RecordsByGroupID[request.GroupID] = records
				hasRecords = true
			}
			if voids, ok := journal.VoidsByRecordIDByGroupID[request.GroupID]; ok && len(voids) > 0 {
				filteredJournal.VoidsByRecordIDByGroupID[request.GroupID] = voids
				hasRecords = true
			}

			if hasRecords {
				journals = append(journals, filteredJournal)
			}
		}

		if nextCursor == "" {
			break
		}
		cursor = nextCursor
	}

	response := EnforcementJournalListResponse{
		Journals: journals,
	}

	responseData, err := json.Marshal(response)
	if err != nil {
		logger.Error("Failed to marshal response", zap.Error(err))
		return "", runtime.NewError("Failed to create response", StatusInternalError)
	}

	return string(responseData), nil
}

func checkGroupOwner(ctx context.Context, db *sql.DB, userID, groupID string) (bool, error) {
	// Check if the user is a superadmin (owner) of the group
	query := `
		SELECT count(1) FROM group_edge 
		WHERE source_id = $1 AND destination_id = $2 AND state = $3
	`
	var count int
	if err := db.QueryRowContext(ctx, query, userID, groupID, int(api.UserGroupList_UserGroup_SUPERADMIN)).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

// EnforcementRecordEditRequest represents the request to edit an enforcement record
type EnforcementRecordEditRequest struct {
	GroupID      string `json:"group_id"`       // Guild group ID (required)
	TargetUserID string `json:"target_user_id"` // Target user ID with the enforcement record (required)
	RecordID     string `json:"record_id"`      // Record ID to edit (required)
	Expiry       int64  `json:"expiry"`         // New expiry timestamp (optional, if provided overrides duration)
	UserNotice   string `json:"user_notice"`    // User-facing notice message (optional)
	AuditorNotes string `json:"auditor_notes"`  // Internal moderator notes (optional, can exceed 200 chars)
}

// EnforcementRecordEditResponse represents the response from editing an enforcement record
type EnforcementRecordEditResponse struct {
	Success bool                    `json:"success"`
	Message string                  `json:"message"`
	Record  *GuildEnforcementRecord `json:"record"`
}

// EnforcementRecordEditRPC handles editing of enforcement records via portal interface
// Permissions: Global operators or guild enforcers (same as Discord edit modal)
func EnforcementRecordEditRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	var request EnforcementRecordEditRequest
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return "", runtime.NewError("Invalid request payload", StatusInvalidArgument)
	}

	// Validate required fields
	if request.GroupID == "" {
		return "", runtime.NewError("group_id is required", StatusInvalidArgument)
	}
	if request.TargetUserID == "" {
		return "", runtime.NewError("target_user_id is required", StatusInvalidArgument)
	}
	if request.RecordID == "" {
		return "", runtime.NewError("record_id is required", StatusInvalidArgument)
	}

	// Get the caller's user ID from context
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("User ID not found in context", StatusUnauthenticated)
	}

	// Check if the caller is a global operator
	isGlobalOperator, err := CheckSystemGroupMembership(ctx, db, userID, GroupGlobalOperators)
	if err != nil {
		logger.Error("Failed to check global operator status", zap.Error(err))
		return "", runtime.NewError("Failed to check permissions", StatusInternalError)
	}

	// Load the guild group to check if caller is an enforcer
	gg, err := GuildGroupLoad(ctx, nk, request.GroupID)
	if err != nil {
		logger.Error("Failed to load guild group", zap.Error(err))
		return "", runtime.NewError("Failed to load guild group", StatusInternalError)
	}

	// Check if the caller is an enforcer
	isEnforcer := gg.IsEnforcer(userID)
	if !isEnforcer && !isGlobalOperator {
		return "", runtime.NewError("You must be a guild enforcer or global operator to edit enforcement records", StatusPermissionDenied)
	}

	// Load the enforcement journal
	journal := NewGuildEnforcementJournal(request.TargetUserID)
	if err := StorableRead(ctx, nk, request.TargetUserID, journal, false); err != nil && status.Code(err) != codes.NotFound {
		logger.Error("Failed to read enforcement journal", zap.Error(err))
		return "", runtime.NewError("Failed to read enforcement journal", StatusInternalError)
	}

	// Get the specific record
	record := journal.GetRecord(request.GroupID, request.RecordID)
	if record == nil {
		return "", runtime.NewError("Enforcement record not found", StatusNotFound)
	}

	// Check if already voided
	if journal.IsVoid(request.GroupID, request.RecordID) {
		return "", runtime.NewError("This record has already been voided and cannot be edited", StatusInvalidArgument)
	}

	// Prepare new values (use existing values if not provided)
	newExpiry := record.Expiry
	if request.Expiry > 0 {
		newExpiry = time.Unix(request.Expiry, 0).UTC()
	}

	newUserNotice := record.UserNoticeText
	if request.UserNotice != "" {
		// Validate user notice length (Discord limit)
		if len(request.UserNotice) > 48 {
			return "", runtime.NewError("user_notice must be less than 48 characters", StatusInvalidArgument)
		}
		newUserNotice = request.UserNotice
	}

	newAuditorNotes := record.AuditorNotes
	if request.AuditorNotes != "" {
		newAuditorNotes = request.AuditorNotes
	}

	// Ensure at least one field is actually changing
	if newExpiry.Equal(record.Expiry) &&
		newUserNotice == record.UserNoticeText &&
		newAuditorNotes == record.AuditorNotes {
		return "", runtime.NewError("No changes specified for enforcement record", StatusInvalidArgument)
	}
	// Get Discord ID for audit log (optional)
	callerDiscordID := ""
	account, err := nk.AccountGetId(ctx, userID)
	if err != nil {
		logger.Error("Failed to load caller account for Discord ID", zap.Error(err), zap.String("user_id", userID))
	} else if account != nil && account.CustomId != "" {
		callerDiscordID = account.CustomId
	}

	// Edit the record (this creates the edit log entry)
	updatedRecord := journal.EditRecord(request.GroupID, request.RecordID, userID, callerDiscordID, newExpiry, newUserNotice, newAuditorNotes)
	if updatedRecord == nil {
		return "", runtime.NewError("Failed to edit enforcement record", StatusInternalError)
	}

	// Save the journal
	if err := StorableWrite(ctx, nk, request.TargetUserID, journal); err != nil {
		logger.Error("Failed to write enforcement journal", zap.Error(err))
		return "", runtime.NewError("Failed to save enforcement record", StatusInternalError)
	}

	// Log the edit
	logger.Info("Enforcement record edited via portal",
		zap.String("group_id", request.GroupID),
		zap.String("target_user_id", request.TargetUserID),
		zap.String("record_id", request.RecordID),
		zap.String("editor_user_id", userID),
		zap.String("previous_notice", record.UserNoticeText),
		zap.String("new_notice", newUserNotice),
		zap.Time("previous_expiry", record.Expiry),
		zap.Time("new_expiry", newExpiry),
	)

	response := EnforcementRecordEditResponse{
		Success: true,
		Message: "Enforcement record updated successfully",
		Record:  updatedRecord,
	}

	responseData, err := json.Marshal(response)
	if err != nil {
		logger.Error("Failed to marshal response", zap.Error(err))
		return "", runtime.NewError("Failed to create response", StatusInternalError)
	}

	return string(responseData), nil
}
