package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

// EarlyQuitHistoryRequest is the request payload for the early quit history RPC
type EarlyQuitHistoryRequest struct {
	UserID          string `json:"user_id,omitempty"`          // Target user ID (optional, defaults to caller)
	IncludeForgiven bool   `json:"include_forgiven,omitempty"` // Include forgiven quits in results
	DaysBack        int    `json:"days_back,omitempty"`        // Only show quits from last N days (0 = all)
}

// EarlyQuitHistoryResponse contains detailed quit history and statistics
type EarlyQuitHistoryResponse struct {
	UserID  string       `json:"user_id"`
	Summary QuitSummary  `json:"summary"`
	Records []QuitRecord `json:"records"`
}

// QuitSummary provides aggregate statistics
type QuitSummary struct {
	TotalQuits          int     `json:"total_quits"`
	UnforgivenQuits     int     `json:"unforgiven_quits"`
	ForgivenQuits       int     `json:"forgiven_quits"`
	EarlyQuits          int     `json:"early_quits"`
	PregameQuits        int     `json:"pregame_quits"`
	CurrentPenaltyLevel int32   `json:"current_penalty_level"`
	CompletedMatches    int32   `json:"completed_matches"`
	QuitRate            float64 `json:"quit_rate"`
	MatchmakingTier     int32   `json:"matchmaking_tier"`

	// Recent statistics (last 7 days)
	RecentQuits       int     `json:"recent_quits_7d"`
	RecentCompletions int32   `json:"recent_completions_7d"`
	RecentQuitRate    float64 `json:"recent_quit_rate_7d"`
}

// EarlyQuitHistoryRPC returns detailed early quit history for a player
func EarlyQuitHistoryRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	var request EarlyQuitHistoryRequest
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return "", runtime.NewError("Invalid request payload", StatusInvalidArgument)
	}

	// Get the caller's user ID from context
	callerUserID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || callerUserID == "" {
		return "", runtime.NewError("User ID not found in context", StatusUnauthenticated)
	}

	// Determine target user ID
	targetUserID := request.UserID
	if targetUserID == "" {
		targetUserID = callerUserID
	}

	// Check if caller has permission to view other users' history
	if targetUserID != callerUserID {
		// Check if caller is a global operator
		isGlobalOperator, err := CheckSystemGroupMembership(ctx, db, callerUserID, GroupGlobalOperators)
		if err != nil {
			logger.Error("Failed to check global operator status", zap.Error(err))
			return "", runtime.NewError("Failed to check permissions", StatusInternalError)
		}
		if !isGlobalOperator {
			return "", runtime.NewError("Only global operators can view other users' quit history", StatusPermissionDenied)
		}
	}

	// Load early quit config for summary stats
	eqconfig := NewEarlyQuitConfig()
	if err := StorableRead(ctx, nk, targetUserID, eqconfig, false); err != nil {
		logger.Debug("Failed to load early quit config, using defaults", zap.Error(err))
	}

	// Load detailed quit history
	history := NewEarlyQuitHistory(targetUserID)
	if err := StorableRead(ctx, nk, targetUserID, history, false); err != nil {
		logger.Debug("No early quit history found, returning empty", zap.Error(err))
		// Return empty history rather than error
		history = NewEarlyQuitHistory(targetUserID)
	}

	// Filter records if needed
	records := history.Records
	if request.DaysBack > 0 {
		cutoff := time.Now().Add(-time.Duration(request.DaysBack) * 24 * time.Hour)
		filtered := make([]QuitRecord, 0)
		for _, record := range records {
			if record.QuitTime.After(cutoff) {
				filtered = append(filtered, record)
			}
		}
		records = filtered
	}

	if !request.IncludeForgiven {
		filtered := make([]QuitRecord, 0)
		for _, record := range records {
			if !record.Forgiven {
				filtered = append(filtered, record)
			}
		}
		records = filtered
	}

	// Calculate summary statistics
	totalQuits := len(history.Records)
	unforgivenQuits := history.CountUnforgivenQuits()
	forgivenQuits := totalQuits - unforgivenQuits
	earlyQuits, pregameQuits := history.CountQuitsByType(false)

	quitRate := history.GetQuitRate(eqconfig.TotalCompletedMatches)

	// Calculate recent statistics (last 7 days)
	recentQuits := history.GetRecentQuits(7 * 24 * time.Hour)
	recentQuitCount := 0
	for _, record := range recentQuits {
		if !record.Forgiven {
			recentQuitCount++
		}
	}

	// Estimate recent completions (this is approximate since we don't track completion timestamps)
	recentCompletions := int32(0)
	if eqconfig.TotalCompletedMatches > 0 {
		// Rough estimate: assume completions are distributed evenly over time
		totalDays := 90.0 // We keep 90 days of history
		recentDays := 7.0
		recentCompletions = int32(float64(eqconfig.TotalCompletedMatches) * (recentDays / totalDays))
	}

	recentTotal := recentQuitCount + int(recentCompletions)
	recentQuitRate := 0.0
	if recentTotal > 0 {
		recentQuitRate = float64(recentQuitCount) / float64(recentTotal)
	}

	summary := QuitSummary{
		TotalQuits:          totalQuits,
		UnforgivenQuits:     unforgivenQuits,
		ForgivenQuits:       forgivenQuits,
		EarlyQuits:          earlyQuits,
		PregameQuits:        pregameQuits,
		CurrentPenaltyLevel: eqconfig.EarlyQuitPenaltyLevel,
		CompletedMatches:    eqconfig.TotalCompletedMatches,
		QuitRate:            quitRate,
		MatchmakingTier:     eqconfig.MatchmakingTier,
		RecentQuits:         recentQuitCount,
		RecentCompletions:   recentCompletions,
		RecentQuitRate:      recentQuitRate,
	}

	response := EarlyQuitHistoryResponse{
		UserID:  targetUserID,
		Summary: summary,
		Records: records,
	}

	responseData, err := json.Marshal(response)
	if err != nil {
		logger.Error("Failed to marshal response", zap.Error(err))
		return "", runtime.NewError("Failed to create response", StatusInternalError)
	}

	return string(responseData), nil
}
