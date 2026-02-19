package server

import (
	"time"

	"github.com/gofrs/uuid/v5"
)

// SessionClassification represents the classification/priority of a match session
type SessionClassification int

const (
	ClassificationNone      SessionClassification = 0 // Lowest priority and safe zero-value default
	ClassificationPickup    SessionClassification = 1 // Can be preempted by mixed/scrimmage/league, or same classification if reservation expired
	ClassificationMixed     SessionClassification = 2 // Can be preempted by scrimmage/league, or same classification if reservation expired
	ClassificationScrimmage SessionClassification = 3 // Can be preempted by league, or same classification if reservation expired
	ClassificationLeague    SessionClassification = 4 // Highest priority - cannot be automatically purged
)

// String returns the string representation of the classification
func (c SessionClassification) String() string {
	switch c {
	case ClassificationLeague:
		return "league"
	case ClassificationScrimmage:
		return "scrimmage"
	case ClassificationMixed:
		return "mixed"
	case ClassificationPickup:
		return "pickup"
	case ClassificationNone:
		return "none"
	default:
		return "unknown"
	}
}

// ParseSessionClassification parses a string into a SessionClassification
func ParseSessionClassification(s string) SessionClassification {
	switch s {
	case "league":
		return ClassificationLeague
	case "scrimmage":
		return ClassificationScrimmage
	case "mixed":
		return ClassificationMixed
	case "pickup":
		return ClassificationPickup
	case "none":
		return ClassificationNone
	default:
		return ClassificationNone
	}
}

// CanPreempt returns true if this classification can preempt the target classification
func (c SessionClassification) CanPreempt(target SessionClassification, targetReservationExpired bool) bool {
	// League sessions cannot be automatically purged
	if target == ClassificationLeague {
		return false
	}

	// None sessions can be preempted at any time by any other classification
	if target == ClassificationNone {
		return c != ClassificationNone
	}

	// Higher classifications (higher values) can preempt lower classifications
	if c > target {
		return true
	}

	// Same classification can preempt if the target's reservation has expired
	if c == target && targetReservationExpired {
		return true
	}

	return false
}

// ReservationState represents the lifecycle state of a reservation
type ReservationState string

const (
	ReservationStateReserved  ReservationState = "reserved"  // Reservation created, waiting for activation
	ReservationStateActivated ReservationState = "activated" // Match allocated and running
	ReservationStateIdle      ReservationState = "idle"      // Match running but low player count
	ReservationStateEnded     ReservationState = "ended"     // Match completed normally
	ReservationStatePreempted ReservationState = "preempted" // Match was preempted by higher priority
	ReservationStateExpired   ReservationState = "expired"   // Reservation expired without activation
)

// MatchReservation represents a match reservation
type MatchReservation struct {
	ID             string                `json:"id"`             // Unique reservation ID (same as match ID when allocated)
	MatchID        string                `json:"match_id"`       // The actual match ID when allocated
	GroupID        uuid.UUID             `json:"group_id"`       // Guild group ID
	Owner          string                `json:"owner"`          // User ID of the reservation owner
	Requester      string                `json:"requester"`      // User ID of who made the reservation
	StartTime      time.Time             `json:"start_time"`     // When the reservation starts
	EndTime        time.Time             `json:"end_time"`       // When the reservation ends
	Duration       time.Duration         `json:"duration"`       // Duration of the reservation
	Classification SessionClassification `json:"classification"` // Priority classification
	State          ReservationState      `json:"state"`          // Current state of the reservation
	CreatedAt      time.Time             `json:"created_at"`     // When the reservation was created
	UpdatedAt      time.Time             `json:"updated_at"`     // When the reservation was last updated
	MessageID      string                `json:"message_id"`     // Discord message ID for tracking
	ChannelID      string                `json:"channel_id"`     // Discord channel ID

	// Match settings for when the reservation is activated
	Mode             string         `json:"mode,omitempty"`
	Level            string         `json:"level,omitempty"`
	TeamSize         int            `json:"team_size,omitempty"`
	RequiredFeatures []string       `json:"required_features,omitempty"`
	TeamAlignments   map[string]int `json:"team_alignments,omitempty"`

	// State transition history
	StateHistory []ReservationStateTransition `json:"state_history,omitempty"`
}

// ReservationStateTransition tracks state changes
type ReservationStateTransition struct {
	FromState ReservationState `json:"from_state"`
	ToState   ReservationState `json:"to_state"`
	Timestamp time.Time        `json:"timestamp"`
	Reason    string           `json:"reason,omitempty"`
}

// IsActive returns true if the reservation is currently active (allocated match running)
func (r *MatchReservation) IsActive() bool {
	return r.State == ReservationStateActivated
}

// IsExpired returns true if the reservation has passed its start time without activation
func (r *MatchReservation) IsExpired() bool {
	if r.State == ReservationStateExpired {
		return true
	}
	// Auto-expire if start time passed >5 minutes without players
	return r.State == ReservationStateReserved && time.Now().After(r.StartTime.Add(5*time.Minute))
}

// CanBePreempted returns true if this reservation can be preempted by the given classification
func (r *MatchReservation) CanBePreempted(preemptingClassification SessionClassification) bool {
	// Check if reservation time has expired
	reservationExpired := r.IsExpired() || time.Now().After(r.EndTime)

	return preemptingClassification.CanPreempt(r.Classification, reservationExpired)
}

// UpdateState changes the reservation state and records the transition
func (r *MatchReservation) UpdateState(newState ReservationState, reason string) {
	oldState := r.State
	r.State = newState
	r.UpdatedAt = time.Now()

	// Record state transition
	transition := ReservationStateTransition{
		FromState: oldState,
		ToState:   newState,
		Timestamp: r.UpdatedAt,
		Reason:    reason,
	}
	r.StateHistory = append(r.StateHistory, transition)
}

// ReservationConflict represents a conflict between reservations
type ReservationConflict struct {
	ExistingReservation *MatchReservation `json:"existing_reservation"`
	RequestedStartTime  time.Time         `json:"requested_start_time"`
	RequestedEndTime    time.Time         `json:"requested_end_time"`
	ConflictType        string            `json:"conflict_type"` // "overlap", "adjacent", etc.
}

// Constants for reservation system
const (
	// Storage collection for reservations
	ReservationStorageCollection = "match_reservations"
	ReservationStorageKey        = "reservation"

	// Reservation constraints
	MaxAdvanceBookingHours    = 36  // Maximum hours in advance to book
	MinReservationMinutes     = 34  // Minimum reservation duration
	MaxReservationMinutes     = 130 // Maximum reservation duration
	ExtensionIncrementMinutes = 15  // Extension increment for active matches

	// Grace periods
	PreemptionGracePeriodSeconds = 45 // Grace period for DM notifications before shutdown
	ReservationExpiryMinutes     = 5  // Minutes after start time before auto-expiry

	// Monitoring thresholds
	LowPlayerCountThreshold         = 6 // Player count below which to notify
	LowPlayerDurationMinutes        = 5 // Minutes of low player count before notification
	UtilizationCheckIntervalMinutes = 5 // How often to check utilization
)
