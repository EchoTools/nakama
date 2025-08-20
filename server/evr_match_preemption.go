package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

// MatchPreemptionManager handles match purging and preemption logic
type MatchPreemptionManager struct {
	nk              runtime.NakamaModule
	logger          runtime.Logger
	reservationMgr  *ReservationManager
	gracePeriod     time.Duration
}

// NewMatchPreemptionManager creates a new match preemption manager
func NewMatchPreemptionManager(nk runtime.NakamaModule, logger runtime.Logger, reservationMgr *ReservationManager) *MatchPreemptionManager {
	return &MatchPreemptionManager{
		nk:              nk,
		logger:          logger,
		reservationMgr:  reservationMgr,
		gracePeriod:     time.Duration(PreemptionGracePeriodSeconds) * time.Second,
	}
}

// PreemptionRequest represents a request to preempt a match
type PreemptionRequest struct {
	RequestingUserID     string                `json:"requesting_user_id"`
	RequestingGroupID    uuid.UUID             `json:"requesting_group_id"`
	RequestingClassification SessionClassification `json:"requesting_classification"`
	RequiredServerCount  int                   `json:"required_server_count"`
	Force                bool                  `json:"force"` // Force preemption (for enforcer actions)
	Reason               string                `json:"reason,omitempty"`
}

// PreemptionCandidate represents a match that could be preempted
type PreemptionCandidate struct {
	MatchID        string                `json:"match_id"`
	Label          *MatchLabel           `json:"label"`
	Classification SessionClassification `json:"classification"`
	ReservationExpired bool              `json:"reservation_expired"`
	PlayerCount    int                   `json:"player_count"`
	Priority       int                   `json:"priority"` // Lower = higher priority for preemption
}

// PreemptionResult represents the result of a preemption attempt
type PreemptionResult struct {
	Success         bool                   `json:"success"`
	PreemptedMatches []string              `json:"preempted_matches"`
	NotificationsSent []string             `json:"notifications_sent"`
	Errors          []string               `json:"errors,omitempty"`
}

// FindPreemptionCandidates finds matches that can be preempted for the given request
func (pm *MatchPreemptionManager) FindPreemptionCandidates(ctx context.Context, req *PreemptionRequest) ([]*PreemptionCandidate, error) {
	// Get all active matches
	matches, err := pm.nk.MatchList(ctx, 100, true, "", nil, nil, "*")
	if err != nil {
		return nil, fmt.Errorf("failed to list matches: %w", err)
	}

	var candidates []*PreemptionCandidate
	
	for _, match := range matches {
		var label MatchLabel
		if err := json.Unmarshal([]byte(match.Label.Value), &label); err != nil {
			pm.logger.Warn("Failed to unmarshal match label for %s: %v", match.MatchId, err)
			continue
		}

		// Skip matches from the same group
		if label.GroupID != nil && *label.GroupID == req.RequestingGroupID {
			continue
		}

		// Check if this match can be preempted
		reservationExpired := pm.isReservationExpired(&label)
		
		if req.Force || req.RequestingClassification.CanPreempt(label.Classification, reservationExpired) {
			priority := pm.calculatePreemptionPriority(&label)
			candidates = append(candidates, &PreemptionCandidate{
				MatchID:            match.MatchId,
				Label:              &label,
				Classification:     label.Classification,
				ReservationExpired: reservationExpired,
				PlayerCount:        label.PlayerCount,
				Priority:           priority,
			})
		}
	}

	// Sort candidates by priority (lower number = higher priority for preemption)
	// This implements the purging rules specified in the requirements
	for i := 0; i < len(candidates)-1; i++ {
		for j := i + 1; j < len(candidates); j++ {
			if candidates[i].Priority > candidates[j].Priority {
				candidates[i], candidates[j] = candidates[j], candidates[i]
			}
		}
	}

	return candidates, nil
}

// PreemptMatches preempts the specified matches with grace period notifications
func (pm *MatchPreemptionManager) PreemptMatches(ctx context.Context, matchIDs []string, req *PreemptionRequest) (*PreemptionResult, error) {
	result := &PreemptionResult{
		Success:           true,
		PreemptedMatches:  make([]string, 0),
		NotificationsSent: make([]string, 0),
		Errors:           make([]string, 0),
	}

	for _, matchID := range matchIDs {
		if err := pm.preemptSingleMatch(ctx, matchID, req, result); err != nil {
			pm.logger.Error("Failed to preempt match %s: %v", matchID, err)
			result.Errors = append(result.Errors, fmt.Sprintf("match %s: %v", matchID, err))
			result.Success = false
		}
	}

	return result, nil
}

// preemptSingleMatch handles preempting a single match
func (pm *MatchPreemptionManager) preemptSingleMatch(ctx context.Context, matchID string, req *PreemptionRequest, result *PreemptionResult) error {
	// Get the match
	match, err := pm.nk.MatchGet(ctx, matchID)
	if err != nil {
		return fmt.Errorf("failed to get match: %w", err)
	}
	if match == nil {
		return fmt.Errorf("match not found")
	}

	var label MatchLabel
	if err := json.Unmarshal([]byte(match.Label.Value), &label); err != nil {
		return fmt.Errorf("failed to unmarshal match label: %w", err)
	}

	// Send DM notifications to spawner and owner
	notificationsSent := 0
	reason := req.Reason
	if reason == "" {
		reason = fmt.Sprintf("Match preempted by higher priority %s session", req.RequestingClassification.String())
	}

	// Notify spawner
	if label.SpawnedBy != "" {
		if err := pm.sendPreemptionNotification(ctx, label.SpawnedBy, matchID, reason); err != nil {
			pm.logger.Warn("Failed to notify spawner %s: %v", label.SpawnedBy, err)
		} else {
			notificationsSent++
			result.NotificationsSent = append(result.NotificationsSent, label.SpawnedBy)
		}
	}

	// Notify owner if different from spawner
	if label.Owner != "" && label.Owner != label.SpawnedBy {
		if err := pm.sendPreemptionNotification(ctx, label.Owner, matchID, reason); err != nil {
			pm.logger.Warn("Failed to notify owner %s: %v", label.Owner, err)
		} else {
			notificationsSent++
			result.NotificationsSent = append(result.NotificationsSent, label.Owner)
		}
	}

	// If notifications were sent, wait for grace period
	if notificationsSent > 0 && !req.Force {
		pm.logger.Info("Sent preemption notifications for match %s, waiting %v grace period", matchID, pm.gracePeriod)
		
		// Schedule the actual shutdown after grace period
		go func() {
			time.Sleep(pm.gracePeriod)
			if err := pm.shutdownMatch(ctx, matchID, reason); err != nil {
				pm.logger.Error("Failed to shutdown match %s after grace period: %v", matchID, err)
			} else {
				pm.logger.Info("Successfully preempted match %s after grace period", matchID)
			}
		}()
	} else {
		// No notifications sent or forced, shutdown immediately
		if err := pm.shutdownMatch(ctx, matchID, reason); err != nil {
			return fmt.Errorf("failed to shutdown match: %w", err)
		}
	}

	result.PreemptedMatches = append(result.PreemptedMatches, matchID)
	return nil
}

// sendPreemptionNotification sends a DM notification about match preemption
func (pm *MatchPreemptionManager) sendPreemptionNotification(ctx context.Context, userID, matchID, reason string) error {
	content := map[string]interface{}{
		"match_id": matchID,
		"reason":   reason,
		"message":  fmt.Sprintf("Your match %s has been preempted: %s. The match will be shut down in %d seconds.", matchID, reason, PreemptionGracePeriodSeconds),
	}

	return pm.nk.NotificationSend(ctx, userID, "Match Preempted", content, NotificationCodeMatchPreempted, "", true)
}

// shutdownMatch shuts down a match
func (pm *MatchPreemptionManager) shutdownMatch(ctx context.Context, matchID, reason string) error {
	// Send shutdown signal to the match
	data := map[string]interface{}{
		"action": "shutdown",
		"reason": reason,
	}
	
	dataBytes, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal shutdown data: %w", err)
	}

	_, err = pm.nk.MatchSignal(ctx, matchID, string(dataBytes))
	return err
}

// isReservationExpired checks if a match's reservation time has expired
func (pm *MatchPreemptionManager) isReservationExpired(label *MatchLabel) bool {
	// For now, consider a reservation expired if the match has been running for more than its expected duration
	// In a full implementation, this would check against actual reservation records
	
	if label.StartTime.IsZero() {
		return false
	}
	
	// If the match has been running for more than 2 hours without a specific end time, consider it expired
	maxRunTime := 2 * time.Hour
	return time.Now().After(label.StartTime.Add(maxRunTime))
}

// calculatePreemptionPriority calculates priority for preemption (lower = higher priority to be preempted)
func (pm *MatchPreemptionManager) calculatePreemptionPriority(label *MatchLabel) int {
	priority := int(label.Classification) * 100 // Base priority from classification
	
	// Add factors that make a match more likely to be preempted:
	// - Lower player count
	// - Longer running time
	// - No active players recently
	
	// Player count factor (fewer players = higher priority for preemption)
	if label.PlayerCount == 0 {
		priority += 50
	} else if label.PlayerCount < 4 {
		priority += 20
	}
	
	// Running time factor (longer running = higher priority for preemption)
	if !label.StartTime.IsZero() {
		runningMinutes := int(time.Since(label.StartTime).Minutes())
		if runningMinutes > 120 { // More than 2 hours
			priority += 30
		} else if runningMinutes > 60 { // More than 1 hour
			priority += 10
		}
	}
	
	return priority
}

// Notification codes for different types of notifications
const (
	NotificationCodeMatchPreempted = 100
)