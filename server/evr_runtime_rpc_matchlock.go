package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

// MatchLockRPCRequest represents the request payload for the match lock RPC.
type MatchLockRPCRequest struct {
	TargetDiscordID string `json:"target_discord_id"` // Discord ID of the player to lock
	TargetUserID    string `json:"target_user_id"`    // User ID of the player to lock (alternative to discord_id)
	LeaderDiscordID string `json:"leader_discord_id"` // Discord ID of the leader to follow (empty to unlock)
	Reason          string `json:"reason"`            // Reason for the lock
}

// MatchLockRPCResponse represents the response payload for the match lock RPC.
type MatchLockRPCResponse struct {
	Success            bool   `json:"success"`
	TargetUserID       string `json:"target_user_id"`
	TargetDiscordID    string `json:"target_discord_id"`
	LeaderDiscordID    string `json:"leader_discord_id,omitempty"`
	Locked             bool   `json:"locked"`
	Reason             string `json:"reason,omitempty"`
	OperatorUserID     string `json:"operator_user_id"`
	LockedAt           int64  `json:"locked_at,omitempty"`
	Message            string `json:"message"`
	PreviousLeaderID   string `json:"previous_leader_id,omitempty"`
	PreviousLockedAt   int64  `json:"previous_locked_at,omitempty"`
	PreviousOperatorID string `json:"previous_operator_id,omitempty"`
	PreviousLockReason string `json:"previous_lock_reason,omitempty"`
}

func (r MatchLockRPCResponse) String() string {
	data, _ := json.Marshal(r)
	return string(data)
}

// MatchLockRPC handles enabling/disabling player match lock.
// This RPC is restricted to Global Operators only.
func MatchLockRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	// Parse the request
	request := MatchLockRPCRequest{}
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return "", runtime.NewError(fmt.Sprintf("Error unmarshalling request: %s", err.Error()), StatusInvalidArgument)
	}

	// Get the caller's user ID from context
	callerUserID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || callerUserID == "" {
		return "", runtime.NewError("Unauthorized: missing user context", StatusUnauthenticated)
	}

	// Check if the caller is a Global Operator
	isGlobalOperator, err := CheckSystemGroupMembership(ctx, db, callerUserID, GroupGlobalOperators)
	if err != nil {
		logger.WithFields(map[string]interface{}{
			"error":   err,
			"user_id": callerUserID,
		}).Error("Failed to check global operator membership")
		return "", runtime.NewError("Failed to verify permissions", StatusInternalError)
	}

	if !isGlobalOperator {
		logger.WithFields(map[string]interface{}{
			"caller_user_id": callerUserID,
		}).Warn("Non-operator attempted to use match lock RPC")
		return "", runtime.NewError("Permission denied: Global Operator access required", StatusPermissionDenied)
	}

	// Resolve the target user ID
	targetUserID := request.TargetUserID
	targetDiscordID := request.TargetDiscordID

	if targetDiscordID != "" && targetUserID == "" {
		targetUserID, err = GetUserIDByDiscordID(ctx, db, targetDiscordID)
		if err != nil {
			return "", runtime.NewError(fmt.Sprintf("Failed to find user by Discord ID: %s", err.Error()), StatusNotFound)
		}
	}

	if targetUserID == "" {
		return "", runtime.NewError("Target user ID or Discord ID is required", StatusInvalidArgument)
	}

	// Get the target's Discord ID if not provided
	if targetDiscordID == "" {
		targetDiscordID, _ = GetDiscordIDByUserID(ctx, db, targetUserID)
	}

	// Load the target's matchmaking settings
	settings, err := LoadMatchmakingSettings(ctx, nk, targetUserID)
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("Failed to load matchmaking settings: %s", err.Error()), StatusInternalError)
	}

	// Store previous lock state for response
	previousLeaderID := settings.MatchLockLeaderDiscordID
	previousLockedAt := settings.MatchLockEnabledAt
	previousOperatorID := settings.MatchLockOperatorUserID
	previousReason := settings.MatchLockReason

	response := MatchLockRPCResponse{
		Success:            true,
		TargetUserID:       targetUserID,
		TargetDiscordID:    targetDiscordID,
		OperatorUserID:     callerUserID,
		PreviousLeaderID:   previousLeaderID,
		PreviousLockedAt:   previousLockedAt,
		PreviousOperatorID: previousOperatorID,
		PreviousLockReason: previousReason,
	}

	if request.LeaderDiscordID != "" {
		// Enable lock
		settings.MatchLockLeaderDiscordID = request.LeaderDiscordID
		settings.MatchLockOperatorUserID = callerUserID
		settings.MatchLockReason = request.Reason
		settings.MatchLockEnabledAt = time.Now().UTC().Unix()

		response.Locked = true
		response.LeaderDiscordID = request.LeaderDiscordID
		response.Reason = request.Reason
		response.LockedAt = settings.MatchLockEnabledAt
		response.Message = fmt.Sprintf("Match lock enabled: player will follow leader %s", request.LeaderDiscordID)

		logger.WithFields(map[string]interface{}{
			"operator_user_id":   callerUserID,
			"target_user_id":     targetUserID,
			"target_discord_id":  targetDiscordID,
			"leader_discord_id":  request.LeaderDiscordID,
			"reason":             request.Reason,
			"locked_at":          settings.MatchLockEnabledAt,
			"previous_leader_id": previousLeaderID,
		}).Info("Match lock ENABLED")
	} else {
		// Disable lock
		settings.MatchLockLeaderDiscordID = ""
		settings.MatchLockOperatorUserID = ""
		settings.MatchLockReason = ""
		settings.MatchLockEnabledAt = 0

		response.Locked = false
		response.Message = "Match lock disabled: player can matchmake independently"

		logger.WithFields(map[string]interface{}{
			"operator_user_id":   callerUserID,
			"target_user_id":     targetUserID,
			"target_discord_id":  targetDiscordID,
			"previous_leader_id": previousLeaderID,
			"previous_reason":    previousReason,
		}).Info("Match lock DISABLED")
	}

	// Save the updated settings
	if err := StoreMatchmakingSettings(ctx, nk, targetUserID, settings); err != nil {
		return "", runtime.NewError(fmt.Sprintf("Failed to save matchmaking settings: %s", err.Error()), StatusInternalError)
	}

	return response.String(), nil
}

// GetMatchLockStatusRPC returns the current match lock status for a player.
// This RPC is restricted to Global Operators only.
func GetMatchLockStatusRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	// Parse the request - same structure for simplicity
	request := MatchLockRPCRequest{}
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return "", runtime.NewError(fmt.Sprintf("Error unmarshalling request: %s", err.Error()), StatusInvalidArgument)
	}

	// Get the caller's user ID from context
	callerUserID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || callerUserID == "" {
		return "", runtime.NewError("Unauthorized: missing user context", StatusUnauthenticated)
	}

	// Check if the caller is a Global Operator
	isGlobalOperator, err := CheckSystemGroupMembership(ctx, db, callerUserID, GroupGlobalOperators)
	if err != nil {
		return "", runtime.NewError("Failed to verify permissions", StatusInternalError)
	}

	if !isGlobalOperator {
		return "", runtime.NewError("Permission denied: Global Operator access required", StatusPermissionDenied)
	}

	// Resolve the target user ID
	targetUserID := request.TargetUserID
	targetDiscordID := request.TargetDiscordID

	if targetDiscordID != "" && targetUserID == "" {
		targetUserID, err = GetUserIDByDiscordID(ctx, db, targetDiscordID)
		if err != nil {
			return "", runtime.NewError(fmt.Sprintf("Failed to find user by Discord ID: %s", err.Error()), StatusNotFound)
		}
	}

	if targetUserID == "" {
		return "", runtime.NewError("Target user ID or Discord ID is required", StatusInvalidArgument)
	}

	// Get the target's Discord ID if not provided
	if targetDiscordID == "" {
		targetDiscordID, _ = GetDiscordIDByUserID(ctx, db, targetUserID)
	}

	// Load the target's matchmaking settings
	settings, err := LoadMatchmakingSettings(ctx, nk, targetUserID)
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("Failed to load matchmaking settings: %s", err.Error()), StatusInternalError)
	}

	response := MatchLockRPCResponse{
		Success:         true,
		TargetUserID:    targetUserID,
		TargetDiscordID: targetDiscordID,
		Locked:          settings.IsMatchLocked(),
		LeaderDiscordID: settings.MatchLockLeaderDiscordID,
		Reason:          settings.MatchLockReason,
		OperatorUserID:  settings.MatchLockOperatorUserID,
		LockedAt:        settings.MatchLockEnabledAt,
	}

	if response.Locked {
		response.Message = fmt.Sprintf("Player is locked to follow leader %s", settings.MatchLockLeaderDiscordID)
	} else {
		response.Message = "Player is not match locked"
	}

	return response.String(), nil
}
