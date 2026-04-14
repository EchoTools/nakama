package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

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
	AllowPrivateLobbies    *bool  `json:"allow_private_lobbies,omitempty"`    // Limit the user to only joining private lobbies (defaults to guild group setting)
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

	userID, _ := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if userID == "" {
		return "", runtime.NewError("unauthorized", StatusUnauthenticated)
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

	// Check permissions (global operator, global bot, or guild enforcer)
	isGlobalOperator, isGlobalBot, _, gg, err := RequireEnforcerOperatorOrBot(ctx, db, nk, userID, groupID)
	if err != nil {
		logger.Error("Permission check failed", zap.Error(err))
		return "", err
	}

	// Resolve allow_private_lobbies: use guild group default if not explicitly set
	allowPrivateLobbies := false
	if request.AllowPrivateLobbies != nil {
		allowPrivateLobbies = *request.AllowPrivateLobbies
	} else if gg != nil {
		allowPrivateLobbies = gg.GetKickPlayerAllowPrivates()
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
		record := journal.AddRecord(groupID, enforcerUserID, enforcerDiscordID, request.UserNotice, request.ModeratorNotes, request.RequireCommunityValues, allowPrivateLobbies, suspensionDuration)
		recordsByGroupID[groupID] = append(recordsByGroupID[groupID], record)
	} else if voidActiveSuspensions {
		// Void active suspensions
		currentGroupID := groupID
		groupIDs := []string{currentGroupID}

		// Add inherited groups
		if gg != nil && gg.SuspensionInheritanceGroupIDs != nil {
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

	// Save the enforcement journal and sync to profile with retry logic for concurrent writes
	if err := SyncJournalAndProfileWithRetry(ctx, nk, targetUserID, journal); err != nil {
		logger.Error("Failed to write enforcement data", zap.Error(err))
		return "", runtime.NewError("Failed to save enforcement data", StatusInternalError)
	}

	// Kick the player from active sessions
	if kickPlayer {
		// Kick the player from matches belonging to this guild. Sessions stay
		// connected; future match joins are refused at authorization time.
		suspendedGuildIDs := []string{groupID}
		if gg != nil && gg.SuspensionInheritanceGroupIDs != nil {
			suspendedGuildIDs = append(suspendedGuildIDs, gg.SuspensionInheritanceGroupIDs...)
		}
		_nk := nk.(*RuntimeGoNakamaModule)
		EnforceGuildSuspension(ctx, RuntimeLoggerToZapLogger(logger), nk, _nk.sessionRegistry, targetUserID, suspendedGuildIDs)
		actions = append(actions, "kicked from current match")
		cnt = len(presences)

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

	sendRPCAuditMessage(
		ctx,
		logger,
		nk,
		"enforcement/kick",
		groupID,
		userID,
		fmt.Sprintf("target_user_id=%s sessions_kicked=%d user_notice=%q actions=%s", targetUserID, cnt, EscapeDiscordMarkdown(request.UserNotice), strings.Join(actions, "; ")),
	)

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

	userID, _ := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if userID == "" {
		return "", runtime.NewError("unauthorized", StatusUnauthenticated)
	}

	isOperator, _, gg, err := RequireEnforcerOrOperator(ctx, db, nk, userID, request.GroupID)
	if err != nil {
		// Allow guild owners to view journals even if they are not enforcers/operators.
		var runtimeErr *runtime.Error
		isPermissionDenied := errors.As(err, &runtimeErr) && runtimeErr.Code == StatusPermissionDenied
		isOwner := gg != nil && gg.IsOwner(userID)
		if isPermissionDenied && isOwner {
			// Owner bypass: guild owner can view enforcement journals even without enforcer role
		} else {
			logger.Error("Permission check failed", zap.Error(err))
			return "", err
		}
	}

	// Determine if the caller's note visibility should be restricted
	redactOtherNotes := gg != nil && gg.RestrictEnforcerNoteVisibility && !isOperator && !gg.IsAuditor(userID)
	if isOwner := gg != nil && gg.IsOwner(userID); isOwner {
		redactOtherNotes = false // owners get auditor-level access
	}

	// List all journals containing this group_id
	query := fmt.Sprintf("+value.guild_ids:%s", Query.EscapeIndexValue(request.GroupID))

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
				if redactOtherNotes {
					// Copy records and redact notes on records the caller didn't create
					redacted := make([]GuildEnforcementRecord, len(records))
					copy(redacted, records)
					for i := range redacted {
						if redacted[i].EnforcerUserID != userID {
							redacted[i].AuditorNotes = ""
							redacted[i].EditLog = nil
						}
					}
					filteredJournal.RecordsByGroupID[request.GroupID] = redacted
				} else {
					filteredJournal.RecordsByGroupID[request.GroupID] = records
				}
				hasRecords = true
			}
			if voids, ok := journal.VoidsByRecordIDByGroupID[request.GroupID]; ok && len(voids) > 0 {
				if redactOtherNotes {
					// Redact void notes for records the caller didn't create
					redactedVoids := make(map[string]GuildEnforcementRecordVoid, len(voids))
					records := journal.RecordsByGroupID[request.GroupID]
					for recordID, v := range voids {
						// Find the original record to check ownership
						isOwned := false
						for _, r := range records {
							if r.ID == recordID && r.EnforcerUserID == userID {
								isOwned = true
								break
							}
						}
						if !isOwned {
							v.Notes = ""
						}
						redactedVoids[recordID] = v
					}
					filteredJournal.VoidsByRecordIDByGroupID[request.GroupID] = redactedVoids
				} else {
					filteredJournal.VoidsByRecordIDByGroupID[request.GroupID] = voids
				}
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

// EnforcementRecordEditRequest represents the request to edit an enforcement record
type EnforcementRecordEditRequest struct {
	GroupID             string `json:"group_id"`                        // Guild group ID (required)
	TargetUserID        string `json:"target_user_id"`                  // Target user ID with the enforcement record (required)
	RecordID            string `json:"record_id"`                       // Record ID to edit (required)
	Expiry              int64  `json:"expiry"`                          // New expiry timestamp (optional, if provided overrides duration)
	UserNotice          string `json:"user_notice"`                     // User-facing notice message (optional)
	AuditorNotes        string `json:"auditor_notes"`                   // Internal moderator notes (optional, can exceed 200 chars)
	AllowPrivateLobbies *bool  `json:"allow_private_lobbies,omitempty"` // Whether the user can join private lobbies (optional, nil means no change)
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

	userID, _ := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if userID == "" {
		return "", runtime.NewError("unauthorized", StatusUnauthenticated)
	}

	// Check permissions (global operator or guild enforcer)
	isOperator, _, gg, err := RequireEnforcerOrOperator(ctx, db, nk, userID, request.GroupID)
	if err != nil {
		logger.Error("Permission check failed", zap.Error(err))
		return "", err
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

	// Preserve existing notes when toggle restricts visibility and caller is not the record creator
	if gg != nil && gg.RestrictEnforcerNoteVisibility && !isOperator && !gg.IsAuditor(userID) && record.EnforcerUserID != userID {
		newAuditorNotes = record.AuditorNotes
	}

	newAllowPrivates := record.AllowPrivateLobbies
	if request.AllowPrivateLobbies != nil {
		newAllowPrivates = *request.AllowPrivateLobbies
	}

	// Ensure at least one field is actually changing
	if newExpiry.Equal(record.Expiry) &&
		newUserNotice == record.UserNoticeText &&
		newAuditorNotes == record.AuditorNotes &&
		newAllowPrivates == record.AllowPrivateLobbies {
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
	updatedRecord := journal.EditRecord(request.GroupID, request.RecordID, userID, callerDiscordID, newExpiry, newUserNotice, newAuditorNotes, newAllowPrivates)
	if updatedRecord == nil {
		return "", runtime.NewError("Failed to edit enforcement record", StatusInternalError)
	}

	// Save the journal and sync to profile with retry logic for concurrent writes
	if err := SyncJournalAndProfileWithRetry(ctx, nk, request.TargetUserID, journal); err != nil {
		logger.Error("Failed to write enforcement data", zap.Error(err))
		return "", runtime.NewError("Failed to save enforcement data", StatusInternalError)
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

	sendRPCAuditMessage(
		ctx,
		logger,
		nk,
		"enforcement/record/edit",
		request.GroupID,
		userID,
		fmt.Sprintf("target_user_id=%s record_id=%s new_expiry=%d new_notice=%q", request.TargetUserID, request.RecordID, newExpiry.Unix(), EscapeDiscordMarkdown(newUserNotice)),
	)

	// Redact sensitive fields in response when caller shouldn't see notes
	responseRecord := updatedRecord
	if gg != nil && gg.RestrictEnforcerNoteVisibility && !isOperator && !gg.IsAuditor(userID) && responseRecord.EnforcerUserID != userID {
		redacted := *responseRecord
		redacted.AuditorNotes = ""
		redacted.EditLog = nil
		responseRecord = &redacted
	}

	response := EnforcementRecordEditResponse{
		Success: true,
		Message: "Enforcement record updated successfully",
		Record:  responseRecord,
	}

	responseData, err := json.Marshal(response)
	if err != nil {
		logger.Error("Failed to marshal response", zap.Error(err))
		return "", runtime.NewError("Failed to create response", StatusInternalError)
	}

	return string(responseData), nil
}
