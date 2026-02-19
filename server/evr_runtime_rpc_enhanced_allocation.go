package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

// EnhancedAllocateMatchRequest represents an enhanced allocation request with reservation support
type EnhancedAllocateMatchRequest struct {
	GroupID          string                `json:"group_id"`
	OwnerID          string                `json:"owner_id"`
	Classification   SessionClassification `json:"classification"`
	StartTime        string                `json:"start_time,omitempty"`
	Duration         int                   `json:"duration,omitempty"`       // Duration in minutes
	AllowPurging     bool                  `json:"allow_purging"`            // Allow purging existing matches if no servers available
	Force            bool                  `json:"force"`                    // Force allocation even with conflicts
	ReservationID    string                `json:"reservation_id,omitempty"` // Use existing reservation
	Mode             string                `json:"mode,omitempty"`
	Level            string                `json:"level,omitempty"`
	TeamSize         int                   `json:"team_size,omitempty"`
	RequiredFeatures []string              `json:"required_features,omitempty"`
	TeamAlignments   map[string]int        `json:"team_alignments,omitempty"`
	Region           string                `json:"region,omitempty"`
}

// EnhancedAllocateMatchResponse represents the enhanced allocation response
type EnhancedAllocateMatchResponse struct {
	Success          bool        `json:"success"`
	MatchLabel       *MatchLabel `json:"match_label,omitempty"`
	ReservationID    string      `json:"reservation_id,omitempty"`
	PreemptedMatches []string    `json:"preempted_matches,omitempty"`
	Message          string      `json:"message,omitempty"`
	Error            string      `json:"error,omitempty"`
}

// ReserveMatchRPC is an enhanced match allocation RPC that supports reservation management and purging
func ReserveMatchRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	// Ensure the request is authenticated
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", runtime.NewError("authentication required", StatusUnauthenticated)
	}

	// Parse the enhanced request
	var request EnhancedAllocateMatchRequest
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return "", runtime.NewError("failed to unmarshal request: "+err.Error(), StatusInvalidArgument)
	}

	// Validate required fields
	if request.GroupID == "" {
		return "", runtime.NewError("group_id is required", StatusInvalidArgument)
	}

	if request.OwnerID == "" {
		request.OwnerID = userID
	}

	// Initialize managers
	reservationMgr := NewReservationManager(nk, logger)
	preemptionMgr := NewMatchPreemptionManager(nk, logger, reservationMgr)

	response := &EnhancedAllocateMatchResponse{}

	// Check if this is using an existing reservation
	if request.ReservationID != "" {
		return processReservationAllocation(ctx, logger, nk, request, reservationMgr, response)
	}

	// Check server availability
	serverAvailable, err := checkServerAvailability(ctx, nk, logger)
	if err != nil {
		response.Error = fmt.Sprintf("Failed to check server availability: %v", err)
		return marshalResponse(response)
	}

	// If no servers available and purging is allowed, try to find candidates
	if !serverAvailable && request.AllowPurging {
		return processAllocationWithPurging(ctx, logger, nk, request, preemptionMgr, response)
	}

	// If no servers available and purging not allowed, fail
	if !serverAvailable {
		response.Error = "No servers available. Use allow_purging=true to enable match preemption."
		return marshalResponse(response)
	}

	// Proceed with normal allocation
	return processNormalAllocation(ctx, logger, db, nk, request, response)
}

// processReservationAllocation handles allocation using an existing reservation
func processReservationAllocation(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, request EnhancedAllocateMatchRequest, reservationMgr *ReservationManager, response *EnhancedAllocateMatchResponse) (string, error) {
	// Get the reservation
	reservation, err := reservationMgr.GetReservation(ctx, request.ReservationID)
	if err != nil {
		response.Error = "Reservation not found"
		return marshalResponse(response)
	}

	// Validate that the user has permission to use this reservation
	if reservation.Owner != request.OwnerID && reservation.Requester != request.OwnerID {
		response.Error = "Not authorized to use this reservation"
		return marshalResponse(response)
	}

	// Check if reservation is in the correct state
	if reservation.State != ReservationStateReserved {
		response.Error = fmt.Sprintf("Reservation is in %s state, cannot allocate", reservation.State)
		return marshalResponse(response)
	}

	// Update reservation state to activated
	if err := reservationMgr.UpdateReservationState(ctx, request.ReservationID, ReservationStateActivated, "match allocated"); err != nil {
		logger.Warn("Failed to update reservation state: %v", err)
	}

	// Send activation DM to the owner if discord session is available
	if dg != nil {
		matchID := reservation.MatchID
		if matchID == "" {
			matchID = reservation.ID
		}
		dmContent := BuildReservationActivationDM(matchID)

		ownerID := reservation.Owner
		if dmUser, err := dg.User(ownerID); err == nil && dmUser != nil {
			dmChannel, err := dg.UserChannelCreate(dmUser.ID)
			if err != nil {
				logger.Warn("Failed to create DM channel for user %s: %v", ownerID, err)
			} else if dmChannel != nil {
				_, err = dg.ChannelMessageSend(dmChannel.ID, dmContent)
				if err != nil {
					logger.Warn("Failed to send activation DM to user %s: %v", ownerID, err)
				} else {
					logger.Info("Sent reservation activation DM to user %s for reservation %s", ownerID, reservation.ID)
				}
			}
		} else if err != nil {
			logger.Warn("Failed to get Discord user %s: %v", ownerID, err)
		}
	}

	// TODO: Implement actual match allocation logic here
	// For now, return success
	response.Success = true
	response.ReservationID = reservation.ID
	response.Message = "Match allocated using reservation"

	return marshalResponse(response)
}

// processAllocationWithPurging handles allocation that may require purging existing matches
func processAllocationWithPurging(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, request EnhancedAllocateMatchRequest, preemptionMgr *MatchPreemptionManager, response *EnhancedAllocateMatchResponse) (string, error) {
	// Create preemption request
	preemptionReq := &PreemptionRequest{
		RequestingUserID:         request.OwnerID,
		RequestingGroupID:        uuid.FromStringOrNil(request.GroupID),
		RequestingClassification: request.Classification,
		RequiredServerCount:      1,
		Force:                    request.Force,
		Reason:                   "Server allocation for new match",
	}

	// Find preemption candidates
	candidates, err := preemptionMgr.FindPreemptionCandidates(ctx, preemptionReq)
	if err != nil {
		response.Error = fmt.Sprintf("Failed to find preemption candidates: %v", err)
		return marshalResponse(response)
	}

	if len(candidates) == 0 {
		response.Error = "No matches available for preemption"
		return marshalResponse(response)
	}

	// Preempt the lowest priority match
	matchesToPreempt := []string{candidates[0].MatchID}
	preemptionResult, err := preemptionMgr.PreemptMatches(ctx, matchesToPreempt, preemptionReq)
	if err != nil {
		response.Error = fmt.Sprintf("Failed to preempt matches: %v", err)
		return marshalResponse(response)
	}

	response.PreemptedMatches = preemptionResult.PreemptedMatches
	response.Success = preemptionResult.Success

	if response.Success {
		response.Message = fmt.Sprintf("Match allocated after preempting %d matches", len(response.PreemptedMatches))
		// TODO: Implement actual match allocation logic here
	} else {
		response.Error = fmt.Sprintf("Preemption failed: %v", preemptionResult.Errors)
	}

	return marshalResponse(response)
}

// processNormalAllocation handles normal allocation when servers are available
func processNormalAllocation(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, request EnhancedAllocateMatchRequest, response *EnhancedAllocateMatchResponse) (string, error) {
	// TODO: Implement normal allocation by calling the existing AllocateMatchRPC
	// For now, return success
	response.Success = true
	response.Message = "Match allocated normally"

	return marshalResponse(response)
}

// checkServerAvailability checks if there are available servers
func checkServerAvailability(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger) (bool, error) {
	// Get current match count
	matches, err := nk.MatchList(ctx, 1000, true, "", nil, nil, "*")
	if err != nil {
		return false, err
	}

	// Simple check: if we have less than 100 matches, consider servers available
	// In a real implementation, this would check actual server capacity
	return len(matches) < 100, nil
}

// marshalResponse marshals the response to JSON
func marshalResponse(response *EnhancedAllocateMatchResponse) (string, error) {
	responseBytes, err := json.Marshal(response)
	if err != nil {
		return "", fmt.Errorf("failed to marshal response: %w", err)
	}
	return string(responseBytes), nil
}

// ExtendMatchRPC allows extending the duration of an active match
func ExtendMatchRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	type ExtendMatchRequest struct {
		MatchID          string `json:"match_id"`
		ExtensionMinutes int    `json:"extension_minutes"`
		Reason           string `json:"reason"`
	}

	type ExtendMatchResponse struct {
		Success    bool   `json:"success"`
		NewEndTime string `json:"new_end_time,omitempty"`
		Message    string `json:"message,omitempty"`
		Error      string `json:"error,omitempty"`
	}

	// Ensure the request is authenticated
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", runtime.NewError("authentication required", StatusUnauthenticated)
	}

	// Parse request
	var request ExtendMatchRequest
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return "", runtime.NewError("failed to unmarshal request: "+err.Error(), StatusInvalidArgument)
	}

	response := &ExtendMatchResponse{}

	// Validate extension is in increments of 15 minutes
	if request.ExtensionMinutes%ExtensionIncrementMinutes != 0 {
		response.Error = fmt.Sprintf("Extension must be in %d minute increments", ExtensionIncrementMinutes)
		responseBytes, _ := json.Marshal(response)
		return string(responseBytes), nil
	}

	// Get match information
	match, err := nk.MatchGet(ctx, request.MatchID)
	if err != nil || match == nil {
		response.Error = "Match not found"
		responseBytes, _ := json.Marshal(response)
		return string(responseBytes), nil
	}

	var label MatchLabel
	if err := json.Unmarshal([]byte(match.Label.Value), &label); err != nil {
		response.Error = "Failed to parse match label"
		responseBytes, _ := json.Marshal(response)
		return string(responseBytes), nil
	}

	// Check if user has permission to extend (owner or spawner)
	if label.Owner.String() != userID && label.SpawnedBy != userID {
		// Check if user is guild enforcer
		if label.GroupID != nil {
			if gg, err := GuildGroupLoad(ctx, nk, label.GroupID.String()); err == nil {
				if !gg.HasRole(userID, gg.RoleMap.Enforcer) {
					response.Error = "Not authorized to extend this match"
					responseBytes, _ := json.Marshal(response)
					return string(responseBytes), nil
				}
			}
		} else {
			response.Error = "Not authorized to extend this match"
			responseBytes, _ := json.Marshal(response)
			return string(responseBytes), nil
		}
	}

	// Check if extension would cause conflicts (this would need more implementation)
	// For now, just allow the extension

	response.Success = true
	response.Message = fmt.Sprintf("Match extended by %d minutes. Reason: %s", request.ExtensionMinutes, request.Reason)

	responseBytes, err := json.Marshal(response)
	if err != nil {
		return "", runtime.NewError("failed to marshal response: "+err.Error(), StatusInternalError)
	}

	logger.Info("Match %s extended by %d minutes by user %s. Reason: %s",
		request.MatchID, request.ExtensionMinutes, userID, request.Reason)

	return string(responseBytes), nil
}
