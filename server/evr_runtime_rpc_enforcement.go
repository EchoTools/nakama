package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/internal/intents"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// EnforcementKickRequest represents the request payload for the kick/enforcement RPC
type EnforcementKickRequest struct {
	UserID                 string `json:"user_id,omitempty"`                  // Target user ID (only allowed for global operators)
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

	// Validate user notice
	if request.UserNotice == "" {
		return "", runtime.NewError("user_notice is required", StatusInvalidArgument)
	}
	if len(request.UserNotice) > 48 {
		return "", runtime.NewError("user_notice must be less than 48 characters", StatusInvalidArgument)
	}

	// Get the caller's group ID from session vars
	vars, err := intents.SessionVarsFromRuntimeContext(ctx)
	if err != nil {
		logger.Error("Failed to get session vars", zap.Error(err))
		return "", runtime.NewError("Failed to get session context", StatusInternalError)
	}

	groupID := vars.GuildID
	if groupID == "" {
		return "", runtime.NewError("Group ID not found in session", StatusPermissionDenied)
	}

	// Check if the caller is a global operator
	isGlobalOperator, err := CheckSystemGroupMembership(ctx, db, userID, GroupGlobalOperators)
	if err != nil {
		logger.Error("Failed to check global operator status", zap.Error(err))
		return "", runtime.NewError("Failed to check permissions", StatusInternalError)
	}

	// Determine target user ID
	targetUserID := request.UserID
	if targetUserID == "" {
		// If no user ID is provided, use the caller's own ID
		targetUserID = userID
	} else if targetUserID != userID && !isGlobalOperator {
		// Only global operators can specify a different user ID
		return "", runtime.NewError("Only global operators can specify a user_id", StatusPermissionDenied)
	}

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
		// Add a new suspension record
		actions = append(actions, fmt.Sprintf("suspension expires <t:%d:R>", suspensionExpiry.UTC().Unix()))
		record := journal.AddRecord(groupID, userID, "", request.UserNotice, request.ModeratorNotes, request.RequireCommunityValues, request.AllowPrivateLobbies, suspensionDuration)
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
				void := journal.VoidRecord(currentGroupID, record.ID, userID, "", details)
				voids[void.RecordID] = void
			}
		}
	}

	// Save the enforcement journal
	if err := StorableWrite(ctx, nk, targetUserID, journal); err != nil {
		logger.Error("Failed to write enforcement journal", zap.Error(err))
		return "", runtime.NewError("Failed to save enforcement journal", StatusInternalError)
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
