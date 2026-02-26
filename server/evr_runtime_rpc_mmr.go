package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// UpdateMMRRequest is the request payload for the player/mmr/update RPC.
type UpdateMMRRequest struct {
	UserID  string  `json:"user_id"`
	GroupID string  `json:"group_id"`
	Mode    string  `json:"mode"`    // e.g. "echo_arena", "echo_combat"
	Mu      float64 `json:"mu"`      // New Mu value
	Sigma   float64 `json:"sigma"`   // New Sigma value
}

// UpdateMMRResponse is the response payload for the player/mmr/update RPC.
type UpdateMMRResponse struct {
	Success  bool    `json:"success"`
	UserID   string  `json:"user_id"`
	GroupID  string  `json:"group_id"`
	Mode     string  `json:"mode"`
	Mu       float64 `json:"mu"`
	Sigma    float64 `json:"sigma"`
	Ordinal  float64 `json:"ordinal"`
}

// GetMMRRequest is the request payload for the player/mmr/get RPC.
type GetMMRRequest struct {
	UserID  string `json:"user_id"`
	GroupID string `json:"group_id"`
	Mode    string `json:"mode"` // e.g. "echo_arena", "echo_combat"
}

// GetMMRResponse is the response payload for the player/mmr/get RPC.
type GetMMRResponse struct {
	UserID       string   `json:"user_id"`
	GroupID      string   `json:"group_id"`
	Mode         string   `json:"mode"`
	Mu           float64  `json:"mu"`
	Sigma        float64  `json:"sigma"`
	Ordinal      float64  `json:"ordinal"`
	StaticMu     *float64 `json:"static_mu"`
	StaticSigma  *float64 `json:"static_sigma"`
	IsStatic     bool     `json:"is_static"`
}

// SetStaticMMRRequest is the request payload for the player/mmr/static RPC.
type SetStaticMMRRequest struct {
	UserID  string   `json:"user_id"`
	Enable  bool     `json:"enable"`
	Mu      *float64 `json:"mu,omitempty"`    // Required when Enable=true
	Sigma   *float64 `json:"sigma,omitempty"` // Required when Enable=true
}

// SetStaticMMRResponse is the response payload for the player/mmr/static RPC.
type SetStaticMMRResponse struct {
	Success bool     `json:"success"`
	UserID  string   `json:"user_id"`
	Enable  bool     `json:"enable"`
	Mu      *float64 `json:"mu,omitempty"`
	Sigma   *float64 `json:"sigma,omitempty"`
	Version string   `json:"version"`
}

// GetMMRRPC retrieves a player's current MMR from leaderboards and static config.
func GetMMRRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	var req GetMMRRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", runtime.NewError("Invalid request payload", StatusInvalidArgument)
	}

	if req.UserID == "" {
		return "", runtime.NewError("user_id is required", StatusInvalidArgument)
	}
	if req.GroupID == "" {
		return "", runtime.NewError("group_id is required", StatusInvalidArgument)
	}
	if req.Mode == "" {
		return "", runtime.NewError("mode is required", StatusInvalidArgument)
	}

	mode := evr.ToSymbol(req.Mode)

	// Load the team rating from leaderboards
	rating, err := MatchmakingRatingLoad(ctx, nk, req.UserID, req.GroupID, mode)
	if err != nil {
		logger.WithFields(map[string]interface{}{
			"user_id":  req.UserID,
			"group_id": req.GroupID,
			"mode":     req.Mode,
			"error":    err,
		}).Error("Failed to load MMR")
		return "", runtime.NewError("Failed to load MMR", StatusInternalError)
	}

	// Load the user's matchmaking settings to check for static MMR
	var settings MatchmakingSettings
	err = StorableRead(ctx, nk, req.UserID, &settings, false)

	resp := GetMMRResponse{
		UserID:  req.UserID,
		GroupID: req.GroupID,
		Mode:    req.Mode,
		Mu:      rating.Mu,
		Sigma:   rating.Sigma,
		Ordinal: rating.Mu - float64(rating.Z)*rating.Sigma,
	}

	// If we loaded settings successfully, include static MMR info
	if err == nil {
		resp.StaticMu = settings.StaticRatingMu
		resp.StaticSigma = settings.StaticRatingSigma
		resp.IsStatic = settings.StaticRatingMu != nil && settings.StaticRatingSigma != nil
	}

	data, err := json.Marshal(resp)
	if err != nil {
		return "", runtime.NewError("Failed to marshal response", StatusInternalError)
	}
	return string(data), nil
}

// UpdateMMRRPC updates a player's MMR leaderboard records.
// Only Global Operators can call this RPC.
func UpdateMMRRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	var req UpdateMMRRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", runtime.NewError("Invalid request payload", StatusInvalidArgument)
	}

	if req.UserID == "" {
		return "", runtime.NewError("user_id is required", StatusInvalidArgument)
	}
	if req.GroupID == "" {
		return "", runtime.NewError("group_id is required", StatusInvalidArgument)
	}
	if req.Mode == "" {
		return "", runtime.NewError("mode is required", StatusInvalidArgument)
	}

	mode := evr.ToSymbol(req.Mode)
	operatorSet := 2 // api.Operator_SET

	// Resolve the user's display name for the leaderboard record
	users, err := nk.UsersGetId(ctx, []string{req.UserID}, nil)
	if err != nil || len(users) == 0 {
		return "", runtime.NewError("User not found", StatusNotFound)
	}
	displayName := users[0].GetDisplayName()

	// Write Mu to leaderboard
	muBoardID := StatisticBoardID(req.GroupID, mode, TeamSkillRatingMuStatisticID, evr.ResetScheduleAllTime)
	muScore, muSubscore, err := Float64ToScore(req.Mu)
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("Invalid Mu value: %v", err), StatusInvalidArgument)
	}

	if _, err := nk.LeaderboardRecordWrite(ctx, muBoardID, req.UserID, displayName, muScore, muSubscore, nil, &operatorSet); err != nil {
		// Try creating the leaderboard first
		if createErr := nk.LeaderboardCreate(ctx, muBoardID, true, "desc", "set", "", nil, true); createErr != nil {
			logger.WithFields(map[string]interface{}{
				"leaderboard_id": muBoardID,
				"error":          createErr,
			}).Error("Failed to create Mu leaderboard")
			return "", runtime.NewError("Failed to write Mu leaderboard", StatusInternalError)
		}
		if _, err := nk.LeaderboardRecordWrite(ctx, muBoardID, req.UserID, displayName, muScore, muSubscore, nil, &operatorSet); err != nil {
			logger.WithFields(map[string]interface{}{
				"leaderboard_id": muBoardID,
				"error":          err,
			}).Error("Failed to write Mu leaderboard record")
			return "", runtime.NewError("Failed to write Mu leaderboard", StatusInternalError)
		}
	}

	// Write Sigma to leaderboard
	sigmaBoardID := StatisticBoardID(req.GroupID, mode, TeamSkillRatingSigmaStatisticID, evr.ResetScheduleAllTime)
	sigmaScore, sigmaSubscore, err := Float64ToScore(req.Sigma)
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("Invalid Sigma value: %v", err), StatusInvalidArgument)
	}

	if _, err := nk.LeaderboardRecordWrite(ctx, sigmaBoardID, req.UserID, displayName, sigmaScore, sigmaSubscore, nil, &operatorSet); err != nil {
		if createErr := nk.LeaderboardCreate(ctx, sigmaBoardID, true, "desc", "set", "", nil, true); createErr != nil {
			logger.WithFields(map[string]interface{}{
				"leaderboard_id": sigmaBoardID,
				"error":          createErr,
			}).Error("Failed to create Sigma leaderboard")
			return "", runtime.NewError("Failed to write Sigma leaderboard", StatusInternalError)
		}
		if _, err := nk.LeaderboardRecordWrite(ctx, sigmaBoardID, req.UserID, displayName, sigmaScore, sigmaSubscore, nil, &operatorSet); err != nil {
			logger.WithFields(map[string]interface{}{
				"leaderboard_id": sigmaBoardID,
				"error":          err,
			}).Error("Failed to write Sigma leaderboard record")
			return "", runtime.NewError("Failed to write Sigma leaderboard", StatusInternalError)
		}
	}

	// Also write the ordinal
	ordinal := req.Mu - 3*req.Sigma // Z=3 default
	ordinalBoardID := StatisticBoardID(req.GroupID, mode, TeamSkillRatingOrdinalStatisticID, evr.ResetScheduleAllTime)
	ordinalScore, ordinalSubscore, err := Float64ToScore(ordinal)
	if err == nil {
		if _, err := nk.LeaderboardRecordWrite(ctx, ordinalBoardID, req.UserID, displayName, ordinalScore, ordinalSubscore, nil, &operatorSet); err != nil {
			// Non-fatal: ordinal leaderboard may not exist yet
			if createErr := nk.LeaderboardCreate(ctx, ordinalBoardID, true, "desc", "set", "", nil, true); createErr == nil {
				nk.LeaderboardRecordWrite(ctx, ordinalBoardID, req.UserID, displayName, ordinalScore, ordinalSubscore, nil, &operatorSet)
			}
		}
	}

	callerID, _ := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	logger.WithFields(map[string]interface{}{
		"caller_id": callerID,
		"user_id":   req.UserID,
		"group_id":  req.GroupID,
		"mode":      req.Mode,
		"mu":        req.Mu,
		"sigma":     req.Sigma,
		"ordinal":   ordinal,
	}).Info("MMR updated by operator")

	resp := UpdateMMRResponse{
		Success: true,
		UserID:  req.UserID,
		GroupID: req.GroupID,
		Mode:    req.Mode,
		Mu:      req.Mu,
		Sigma:   req.Sigma,
		Ordinal: ordinal,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		return "", runtime.NewError("Failed to marshal response", StatusInternalError)
	}
	return string(data), nil
}

// SetStaticMMRRPC enables or disables static MMR for a player.
// When enabled, the player's matchmaking will use the specified static values
// instead of their dynamic leaderboard rating.
// Only Global Operators can call this RPC.
func SetStaticMMRRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	var req SetStaticMMRRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", runtime.NewError("Invalid request payload", StatusInvalidArgument)
	}

	if req.UserID == "" {
		return "", runtime.NewError("user_id is required", StatusInvalidArgument)
	}

	if req.Enable && (req.Mu == nil || req.Sigma == nil) {
		return "", runtime.NewError("mu and sigma are required when enabling static MMR", StatusInvalidArgument)
	}

	// Load the existing matchmaking settings
	var settings MatchmakingSettings
	err := StorableRead(ctx, nk, req.UserID, &settings, true)
	if err != nil {
		logger.WithFields(map[string]interface{}{
			"user_id": req.UserID,
			"error":   err,
		}).Error("Failed to read matchmaking settings")
		return "", runtime.NewError("Failed to read matchmaking settings", StatusInternalError)
	}

	// Update static rating fields
	if req.Enable {
		settings.StaticRatingMu = req.Mu
		settings.StaticRatingSigma = req.Sigma
	} else {
		settings.StaticRatingMu = nil
		settings.StaticRatingSigma = nil
	}

	// Write back the updated settings
	if err := StorableWrite(ctx, nk, req.UserID, &settings); err != nil {
		logger.WithFields(map[string]interface{}{
			"user_id": req.UserID,
			"error":   err,
		}).Error("Failed to write matchmaking settings")
		return "", runtime.NewError("Failed to write matchmaking settings", StatusInternalError)
	}

	callerID, _ := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	logger.WithFields(map[string]interface{}{
		"caller_id": callerID,
		"user_id":   req.UserID,
		"enable":    req.Enable,
		"mu":        req.Mu,
		"sigma":     req.Sigma,
	}).Info("Static MMR setting updated by operator")

	meta := settings.StorageMeta()
	resp := SetStaticMMRResponse{
		Success: true,
		UserID:  req.UserID,
		Enable:  req.Enable,
		Mu:      settings.StaticRatingMu,
		Sigma:   settings.StaticRatingSigma,
		Version: meta.Version,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		return "", runtime.NewError("Failed to marshal response", StatusInternalError)
	}
	return string(data), nil
}
