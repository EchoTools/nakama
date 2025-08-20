package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

// ReservationManager handles all reservation-related operations
type ReservationManager struct {
	nk     runtime.NakamaModule
	logger runtime.Logger
}

// NewReservationManager creates a new reservation manager
func NewReservationManager(nk runtime.NakamaModule, logger runtime.Logger) *ReservationManager {
	return &ReservationManager{
		nk:     nk,
		logger: logger,
	}
}

// CreateReservation creates a new match reservation
func (rm *ReservationManager) CreateReservation(ctx context.Context, req *CreateReservationRequest) (*MatchReservation, error) {
	// Validate the request
	if err := rm.validateReservationRequest(req); err != nil {
		return nil, err
	}

	// Check for conflicts with existing reservations
	conflicts, err := rm.checkReservationConflicts(ctx, req.GroupID, req.StartTime, req.EndTime, "")
	if err != nil {
		return nil, fmt.Errorf("failed to check conflicts: %w", err)
	}

	if len(conflicts) > 0 && !req.Force {
		return nil, &ReservationConflictError{Conflicts: conflicts}
	}

	// Create the reservation
	reservation := &MatchReservation{
		ID:             uuid.Must(uuid.NewV4()).String(),
		GroupID:        req.GroupID,
		Owner:          req.Owner,
		Requester:      req.Requester,
		StartTime:      req.StartTime,
		EndTime:        req.EndTime,
		Duration:       req.EndTime.Sub(req.StartTime),
		Classification: req.Classification,
		State:          ReservationStateReserved,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
		Mode:           req.Mode,
		Level:          req.Level,
		TeamSize:       req.TeamSize,
		RequiredFeatures: req.RequiredFeatures,
		TeamAlignments: req.TeamAlignments,
		StateHistory: []ReservationStateTransition{{
			FromState: "",
			ToState:   ReservationStateReserved,
			Timestamp: time.Now(),
			Reason:    "reservation created",
		}},
	}

	// Store the reservation
	if err := rm.StoreReservation(ctx, reservation); err != nil {
		return nil, fmt.Errorf("failed to store reservation: %w", err)
	}

	rm.logger.Info("Created reservation %s for group %s from %v to %v", 
		reservation.ID, reservation.GroupID.String(), reservation.StartTime, reservation.EndTime)

	return reservation, nil
}

// StoreReservation stores a reservation in the storage system
func (rm *ReservationManager) StoreReservation(ctx context.Context, reservation *MatchReservation) error {
	data, err := json.Marshal(reservation)
	if err != nil {
		return fmt.Errorf("failed to marshal reservation: %w", err)
	}

	// Store under system user with the reservation ID as key
	_, err = rm.nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      ReservationStorageCollection,
		Key:             reservation.ID,
		Value:           string(data),
		PermissionRead:  runtime.STORAGE_PERMISSION_OWNER_READ,
		PermissionWrite: runtime.STORAGE_PERMISSION_OWNER_WRITE,
		Version:         "",
	}})

	return err
}

// GetReservation retrieves a reservation by ID
func (rm *ReservationManager) GetReservation(ctx context.Context, reservationID string) (*MatchReservation, error) {
	objects, err := rm.nk.StorageRead(ctx, []*runtime.StorageRead{{
		Collection: ReservationStorageCollection,
		Key:        reservationID,
		UserID:     SystemUserID,
	}})

	if err != nil {
		return nil, fmt.Errorf("failed to read reservation: %w", err)
	}

	if len(objects) == 0 {
		return nil, ErrReservationNotFound
	}

	var reservation MatchReservation
	if err := json.Unmarshal([]byte(objects[0].Value), &reservation); err != nil {
		return nil, fmt.Errorf("failed to unmarshal reservation: %w", err)
	}

	return &reservation, nil
}

// ListReservations retrieves reservations for a group within a time range
func (rm *ReservationManager) ListReservations(ctx context.Context, groupID uuid.UUID, startTime, endTime time.Time) ([]*MatchReservation, error) {
	// Query storage for reservations
	// Note: In a production system, you'd want to use an index for efficient querying
	// For now, we'll list all reservations and filter
	objects, _, err := rm.nk.StorageList(ctx, SystemUserID, ReservationStorageCollection, 100, "")
	if err != nil {
		return nil, fmt.Errorf("failed to list reservations: %w", err)
	}

	var reservations []*MatchReservation
	for _, obj := range objects {
		var reservation MatchReservation
		if err := json.Unmarshal([]byte(obj.Value), &reservation); err != nil {
			rm.logger.Warn("Failed to unmarshal reservation %s: %v", obj.Key, err)
			continue
		}

		// Filter by group and time range
		if reservation.GroupID == groupID &&
			reservation.StartTime.Before(endTime) &&
			reservation.EndTime.After(startTime) {
			reservations = append(reservations, &reservation)
		}
	}

	return reservations, nil
}

// UpdateReservationState updates the state of a reservation
func (rm *ReservationManager) UpdateReservationState(ctx context.Context, reservationID string, newState ReservationState, reason string) error {
	reservation, err := rm.GetReservation(ctx, reservationID)
	if err != nil {
		return err
	}

	reservation.UpdateState(newState, reason)
	return rm.StoreReservation(ctx, reservation)
}

// DeleteReservation removes a reservation
func (rm *ReservationManager) DeleteReservation(ctx context.Context, reservationID string) error {
	return rm.nk.StorageDelete(ctx, []*runtime.StorageDelete{{
		Collection: ReservationStorageCollection,
		Key:        reservationID,
		UserID:     SystemUserID,
	}})
}

// checkReservationConflicts checks for scheduling conflicts
func (rm *ReservationManager) checkReservationConflicts(ctx context.Context, groupID uuid.UUID, startTime, endTime time.Time, excludeReservationID string) ([]*ReservationConflict, error) {
	// Get all reservations that might conflict
	existingReservations, err := rm.ListReservations(ctx, groupID, startTime.Add(-time.Hour), endTime.Add(time.Hour))
	if err != nil {
		return nil, err
	}

	var conflicts []*ReservationConflict
	for _, existing := range existingReservations {
		// Skip the reservation we're updating
		if existing.ID == excludeReservationID {
			continue
		}

		// Skip expired or ended reservations
		if existing.State == ReservationStateEnded || existing.State == ReservationStateExpired {
			continue
		}

		// Check for overlap
		if startTime.Before(existing.EndTime) && endTime.After(existing.StartTime) {
			conflictType := "overlap"
			
			// Check if it's just adjacent (within 5 minutes)
			if startTime.Equal(existing.EndTime) || endTime.Equal(existing.StartTime) {
				conflictType = "adjacent"
			} else if startTime.Before(existing.EndTime.Add(5*time.Minute)) && startTime.After(existing.EndTime.Add(-5*time.Minute)) ||
				endTime.Before(existing.StartTime.Add(5*time.Minute)) && endTime.After(existing.StartTime.Add(-5*time.Minute)) {
				conflictType = "near"
			}

			conflicts = append(conflicts, &ReservationConflict{
				ExistingReservation: existing,
				RequestedStartTime:  startTime,
				RequestedEndTime:    endTime,
				ConflictType:        conflictType,
			})
		}
	}

	return conflicts, nil
}

// validateReservationRequest validates a reservation request
func (rm *ReservationManager) validateReservationRequest(req *CreateReservationRequest) error {
	now := time.Now()

	// Check if start time is not too far in the future
	if req.StartTime.After(now.Add(time.Duration(MaxAdvanceBookingHours) * time.Hour)) {
		return fmt.Errorf("reservation cannot be more than %d hours in advance", MaxAdvanceBookingHours)
	}

	// Check if start time is not in the past
	if req.StartTime.Before(now.Add(-time.Minute)) {
		return fmt.Errorf("reservation start time cannot be in the past")
	}

	// Check duration limits
	duration := req.EndTime.Sub(req.StartTime)
	if duration < time.Duration(MinReservationMinutes)*time.Minute {
		return fmt.Errorf("reservation duration must be at least %d minutes", MinReservationMinutes)
	}
	if duration > time.Duration(MaxReservationMinutes)*time.Minute {
		return fmt.Errorf("reservation duration cannot exceed %d minutes", MaxReservationMinutes)
	}

	return nil
}

// CreateReservationRequest represents a request to create a reservation
type CreateReservationRequest struct {
	GroupID          uuid.UUID             `json:"group_id"`
	Owner            string                `json:"owner"`
	Requester        string                `json:"requester"`
	StartTime        time.Time             `json:"start_time"`
	EndTime          time.Time             `json:"end_time"`
	Classification   SessionClassification `json:"classification"`
	Mode             string                `json:"mode,omitempty"`
	Level            string                `json:"level,omitempty"`
	TeamSize         int                   `json:"team_size,omitempty"`
	RequiredFeatures []string              `json:"required_features,omitempty"`
	TeamAlignments   map[string]int        `json:"team_alignments,omitempty"`
	Force            bool                  `json:"force"` // Force creation even if conflicts exist
}

// Custom errors
var (
	ErrReservationNotFound = fmt.Errorf("reservation not found")
)

// ReservationConflictError represents a reservation scheduling conflict
type ReservationConflictError struct {
	Conflicts []*ReservationConflict
}

func (e *ReservationConflictError) Error() string {
	return fmt.Sprintf("reservation conflicts detected: %d conflicts", len(e.Conflicts))
}