package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

// EarlyQuitViewRequest is the request payload for viewing a player's early quit state.
type EarlyQuitViewRequest struct {
	GroupID      string `json:"group_id"`
	TargetUserID string `json:"target_user_id"`
}

// EarlyQuitViewResponse contains the player's early quit state and guild-scoped stats.
type EarlyQuitViewResponse struct {
	UserID    string                   `json:"user_id"`
	GroupID   string                   `json:"group_id"`
	State     *EarlyQuitStateView      `json:"state"`
	Guild     *EarlyQuitGuildView      `json:"guild"`
	Override  *EarlyQuitGuildOverride   `json:"override,omitempty"`
	Records   []QuitRecord             `json:"recent_records"`
}

// EarlyQuitStateView is the read-only view of global state.
type EarlyQuitStateView struct {
	PenaltyTimestamp    int64 `json:"penalty_ts"`
	NumEarlyQuits       int32 `json:"num_early_quits"`
	NumSteadyMatches    int32 `json:"num_steady_matches"`
	NumSteadyEarlyQuits int32 `json:"num_steady_early_quits"`
	PenaltyLevel        int32 `json:"penalty_level"`
	SteadyPlayerLevel   int32 `json:"steady_player_level"`
	TotalCompleted      int32 `json:"total_completed_matches"`
	MatchmakingTier     int32 `json:"matchmaking_tier"`
	PenaltyActive       bool  `json:"penalty_active"`
}

// EarlyQuitGuildView is the guild-derived stats from history.
type EarlyQuitGuildView struct {
	Quits       int32   `json:"quits"`
	Completions int32   `json:"completions"`
	QuitRate    float64 `json:"quit_rate"`
}

// EarlyQuitViewRPC returns a player's early quit state, guild-specific stats, and overrides.
func EarlyQuitViewRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	var request EarlyQuitViewRequest
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return "", runtime.NewError("Invalid request payload", StatusInvalidArgument)
	}
	if request.GroupID == "" || request.TargetUserID == "" {
		return "", runtime.NewError("group_id and target_user_id are required", StatusInvalidArgument)
	}

	callerUserID, _ := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if callerUserID == "" {
		return "", runtime.NewError("unauthorized", StatusUnauthenticated)
	}

	// Enforce: caller must be enforcer or operator for this guild
	_, _, _, _, err := RequireEnforcerOperatorOrBot(ctx, db, nk, callerUserID, request.GroupID)
	if err != nil {
		return "", err
	}

	// Load player state
	state := NewEarlyQuitPlayerState()
	if err := StorableRead(ctx, nk, request.TargetUserID, state, false); err != nil {
		logger.Debug("No early quit state found", zap.Error(err))
	}

	// Load history for guild stats
	history := NewEarlyQuitHistory(request.TargetUserID)
	if err := StorableRead(ctx, nk, request.TargetUserID, history, false); err != nil {
		logger.Debug("No early quit history found", zap.Error(err))
		history = NewEarlyQuitHistory(request.TargetUserID)
	}

	guildQuits, guildCompletions := history.GetGuildQuitStats(request.GroupID)
	var guildQuitRate float64
	if total := guildQuits + guildCompletions; total > 0 {
		guildQuitRate = float64(guildQuits) / float64(total)
	}

	// Get recent guild-specific records (last 30 days)
	recentRecords := history.GetGuildRecords(request.GroupID)
	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	filtered := make([]QuitRecord, 0, len(recentRecords))
	for _, r := range recentRecords {
		if r.QuitTime.After(cutoff) {
			filtered = append(filtered, r)
		}
	}

	// Build response
	var override *EarlyQuitGuildOverride
	if state.GuildOverrides != nil {
		override = state.GuildOverrides[request.GroupID]
	}

	resp := EarlyQuitViewResponse{
		UserID:  request.TargetUserID,
		GroupID: request.GroupID,
		State: &EarlyQuitStateView{
			PenaltyTimestamp:    state.PenaltyTimestamp,
			NumEarlyQuits:       state.NumEarlyQuits,
			NumSteadyMatches:    state.NumSteadyMatches,
			NumSteadyEarlyQuits: state.NumSteadyEarlyQuits,
			PenaltyLevel:        state.PenaltyLevel,
			SteadyPlayerLevel:   state.SteadyPlayerLevel,
			TotalCompleted:      state.TotalCompletedMatches,
			MatchmakingTier:     state.MatchmakingTier,
			PenaltyActive:       state.IsPenaltyActive(),
		},
		Guild: &EarlyQuitGuildView{
			Quits:       guildQuits,
			Completions: guildCompletions,
			QuitRate:    guildQuitRate,
		},
		Override: override,
		Records:  filtered,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		return "", runtime.NewError("Failed to marshal response", StatusInternalError)
	}
	return string(data), nil
}

// EarlyQuitModifyRequest is the request payload for modifying a player's early quit state.
type EarlyQuitModifyRequest struct {
	GroupID        string `json:"group_id"`
	TargetUserID   string `json:"target_user_id"`
	Action         string `json:"action"` // "set_penalty", "reset", "set_exempt"
	PenaltyLevel   *int32 `json:"penalty_level,omitempty"`
	Exempt         *bool  `json:"exempt,omitempty"`
	ModeratorNotes string `json:"moderator_notes,omitempty"`
}

// EarlyQuitModifyResponse is the response for a successful modification.
type EarlyQuitModifyResponse struct {
	Success bool                   `json:"success"`
	Message string                 `json:"message"`
	State   *EarlyQuitStateView    `json:"state"`
}

// EarlyQuitModifyRPC modifies a player's early quit state (enforcer/operator only).
func EarlyQuitModifyRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	var request EarlyQuitModifyRequest
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return "", runtime.NewError("Invalid request payload", StatusInvalidArgument)
	}
	if request.GroupID == "" || request.TargetUserID == "" {
		return "", runtime.NewError("group_id and target_user_id are required", StatusInvalidArgument)
	}
	if request.Action == "" {
		return "", runtime.NewError("action is required (set_penalty, reset, set_exempt)", StatusInvalidArgument)
	}

	callerUserID, _ := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if callerUserID == "" {
		return "", runtime.NewError("unauthorized", StatusUnauthenticated)
	}

	// Enforce: caller must be enforcer or operator for this guild
	_, _, _, _, err := RequireEnforcerOperatorOrBot(ctx, db, nk, callerUserID, request.GroupID)
	if err != nil {
		return "", err
	}

	// Prevent self-modification
	if request.TargetUserID == callerUserID {
		return "", runtime.NewError("Cannot modify your own early quit state", StatusInvalidArgument)
	}

	// Load current state
	state := NewEarlyQuitPlayerState()
	if err := StorableRead(ctx, nk, request.TargetUserID, state, true); err != nil {
		return "", runtime.NewError("Failed to load early quit state", StatusInternalError)
	}

	// Load config for penalty resolution
	serviceConfig := loadEarlyQuitServiceConfigOrDefault(ctx, nk)

	var message string

	switch request.Action {
	case "set_penalty":
		if request.PenaltyLevel == nil {
			return "", runtime.NewError("penalty_level is required for set_penalty action", StatusInvalidArgument)
		}
		level := *request.PenaltyLevel
		if level < 0 || level > MaxEarlyQuitPenaltyLevel {
			return "", runtime.NewError(fmt.Sprintf("penalty_level must be between 0 and %d", MaxEarlyQuitPenaltyLevel), StatusInvalidArgument)
		}

		// Set guild override
		if state.GuildOverrides == nil {
			state.GuildOverrides = make(map[string]*EarlyQuitGuildOverride)
		}
		if state.GuildOverrides[request.GroupID] == nil {
			state.GuildOverrides[request.GroupID] = &EarlyQuitGuildOverride{}
		}
		state.GuildOverrides[request.GroupID].PenaltyLevel = &level
		state.GuildOverrides[request.GroupID].ModeratorNotes = request.ModeratorNotes

		// Also update global penalty and lockout from config
		state.PenaltyLevel = level
		_, lockoutSec := ResolvePenaltyLevel(state.NumEarlyQuits, serviceConfig)
		_ = lockoutSec // Use config lockout for the resolved level
		// Find lockout for the SET level specifically
		for _, pl := range serviceConfig.PenaltyLevels {
			if int32(pl.PenaltyLevel) == level {
				lockoutSec = int32(pl.MMLockoutSec)
				break
			}
		}
		if lockoutSec > 0 {
			state.PenaltyTimestamp = time.Now().Unix() + int64(lockoutSec)
		} else {
			state.PenaltyTimestamp = 0
		}

		message = fmt.Sprintf("Penalty level set to %d", level)

	case "reset":
		state.NumEarlyQuits = 0
		state.NumSteadyEarlyQuits = 0
		state.PenaltyTimestamp = 0

		// Re-resolve penalty from config (should be level 0 with 0 quits)
		level, _ := ResolvePenaltyLevel(0, serviceConfig)
		state.PenaltyLevel = level
		state.SteadyPlayerLevel = ResolveSteadyPlayerLevel(state.NumSteadyMatches, 0, serviceConfig)

		// Clear guild override
		if state.GuildOverrides != nil {
			delete(state.GuildOverrides, request.GroupID)
		}

		// Re-resolve tier
		serviceSettings := ServiceSettings()
		state.UpdateTier(serviceSettings.Matchmaking.EarlyQuitTier1Threshold)

		message = "Early quit stats reset"

	case "set_exempt":
		if request.Exempt == nil {
			return "", runtime.NewError("exempt is required for set_exempt action", StatusInvalidArgument)
		}
		if state.GuildOverrides == nil {
			state.GuildOverrides = make(map[string]*EarlyQuitGuildOverride)
		}
		if state.GuildOverrides[request.GroupID] == nil {
			state.GuildOverrides[request.GroupID] = &EarlyQuitGuildOverride{}
		}
		state.GuildOverrides[request.GroupID].Exempt = *request.Exempt
		state.GuildOverrides[request.GroupID].ModeratorNotes = request.ModeratorNotes

		if *request.Exempt {
			message = "Player exempted from early quit tracking in this guild"
		} else {
			message = "Player early quit exemption removed"
		}

	default:
		return "", runtime.NewError("Invalid action. Must be: set_penalty, reset, set_exempt", StatusInvalidArgument)
	}

	// Write updated state
	if err := StorableWrite(ctx, nk, request.TargetUserID, state); err != nil {
		return "", runtime.NewError("Failed to save early quit state", StatusInternalError)
	}

	// Auto-notify the player
	if messageTrigger := globalEarlyQuitMessageTrigger.Load(); messageTrigger != nil {
		messageTrigger.SendEarlyQuitUpdateNotification(ctx, request.TargetUserID, state)
	}

	// Audit log
	details := fmt.Sprintf("action=%s target=%s group=%s", request.Action, request.TargetUserID, request.GroupID)
	if request.PenaltyLevel != nil {
		details += fmt.Sprintf(" penalty_level=%d", *request.PenaltyLevel)
	}
	if request.Exempt != nil {
		details += fmt.Sprintf(" exempt=%v", *request.Exempt)
	}
	sendRPCAuditMessage(ctx, logger, nk, "earlyquit/modify", request.GroupID, callerUserID, details)

	resp := EarlyQuitModifyResponse{
		Success: true,
		Message: message,
		State: &EarlyQuitStateView{
			PenaltyTimestamp:    state.PenaltyTimestamp,
			NumEarlyQuits:       state.NumEarlyQuits,
			NumSteadyMatches:    state.NumSteadyMatches,
			NumSteadyEarlyQuits: state.NumSteadyEarlyQuits,
			PenaltyLevel:        state.PenaltyLevel,
			SteadyPlayerLevel:   state.SteadyPlayerLevel,
			TotalCompleted:      state.TotalCompletedMatches,
			MatchmakingTier:     state.MatchmakingTier,
			PenaltyActive:       state.IsPenaltyActive(),
		},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		return "", runtime.NewError("Failed to marshal response", StatusInternalError)
	}
	return string(data), nil
}

// loadEarlyQuitServiceConfigOrDefault loads the system-wide early quit config or returns defaults.
func loadEarlyQuitServiceConfigOrDefault(ctx context.Context, nk runtime.NakamaModule) *evr.SNSEarlyQuitConfig {
	cfg := evr.NewDefaultSNSEarlyQuitConfig()
	storable := &EarlyQuitServiceConfigStorable{SNSEarlyQuitConfig: cfg}
	if err := StorableRead(ctx, nk, "", storable, false); err != nil {
		return evr.NewDefaultSNSEarlyQuitConfig()
	}
	return storable.SNSEarlyQuitConfig
}
