package evr

import (
	"fmt"
	"time"
)

// SNSEarlyQuitUpdateNotification represents penalty status update notifications
// Server → Client: Sent when a penalty is applied, expires, or player tier changes
type SNSEarlyQuitUpdateNotification struct {
	EventType       UpdateNotificationType `json:"event_type"`         // Type of penalty event
	PenaltyLevel    int32                  `json:"penalty_level"`      // Current penalty tier
	DurationSeconds int32                  `json:"duration_seconds"`   // Lockout duration
	ExpiresAt       int64                  `json:"expires_at"`         // Unix timestamp when penalty expires
	Reason          string                 `json:"reason"`             // Human-readable reason
	PreviousTier    int32                  `json:"previous_tier"`      // For tier changes
	NewTier         int32                  `json:"new_tier"`           // For tier changes
	MatchID         string                 `json:"match_id,omitempty"` // Match that triggered the penalty
}

// UpdateNotificationType indicates what type of penalty event occurred
type UpdateNotificationType string

const (
	// UpdateNotificationPenaltyApplied: Player received a new penalty (early quit detected)
	UpdateNotificationPenaltyApplied UpdateNotificationType = "penalty_applied"
	// UpdateNotificationPenaltyExpired: Player's penalty has expired
	UpdateNotificationPenaltyExpired UpdateNotificationType = "penalty_expired"
	// UpdateNotificationTierDegraded: Player moved to worse tier
	UpdateNotificationTierDegraded UpdateNotificationType = "tier_degraded"
	// UpdateNotificationTierRestored: Player moved back to better tier
	UpdateNotificationTierRestored UpdateNotificationType = "tier_restored"
	// UpdateNotificationLockoutActive: Matchmaking lockout is active
	UpdateNotificationLockoutActive UpdateNotificationType = "lockout_active"
	// UpdateNotificationLockoutCleared: Matchmaking lockout has been cleared
	UpdateNotificationLockoutCleared UpdateNotificationType = "lockout_cleared"
)

func (m SNSEarlyQuitUpdateNotification) Token() string {
	return "SNSEarlyQuitUpdateNotification"
}

func (m *SNSEarlyQuitUpdateNotification) Symbol() Symbol {
	return ToSymbol(m.Token())
}

func (m *SNSEarlyQuitUpdateNotification) String() string {
	return fmt.Sprintf("%s(event=%s, penalty_level=%d, expires_in=%ds)",
		m.Token(), m.EventType, m.PenaltyLevel, m.RemainingSeconds())
}

func (m *SNSEarlyQuitUpdateNotification) Stream(s *EasyStream) error {
	return s.StreamJson(m, false, ZlibCompression)
}

// NewPenaltyAppliedNotification creates a penalty applied notification
func NewPenaltyAppliedNotification(penaltyLevel int32, durationSeconds int32, reason string) *SNSEarlyQuitUpdateNotification {
	expiresAt := time.Now().Add(time.Duration(durationSeconds) * time.Second).Unix()
	return &SNSEarlyQuitUpdateNotification{
		EventType:       UpdateNotificationPenaltyApplied,
		PenaltyLevel:    penaltyLevel,
		DurationSeconds: durationSeconds,
		ExpiresAt:       expiresAt,
		Reason:          reason,
	}
}

// NewPenaltyExpiredNotification creates a penalty expired notification
func NewPenaltyExpiredNotification() *SNSEarlyQuitUpdateNotification {
	return &SNSEarlyQuitUpdateNotification{
		EventType:       UpdateNotificationPenaltyExpired,
		DurationSeconds: 0,
		ExpiresAt:       time.Now().Unix(),
		Reason:          "Penalty period has expired",
	}
}

// NewTierDegradedNotification creates a tier degraded notification
func NewTierDegradedNotification(oldTier, newTier int32, reason string) *SNSEarlyQuitUpdateNotification {
	return &SNSEarlyQuitUpdateNotification{
		EventType:    UpdateNotificationTierDegraded,
		PreviousTier: oldTier,
		NewTier:      newTier,
		Reason:       reason,
	}
}

// NewTierRestoredNotification creates a tier restored notification
func NewTierRestoredNotification(oldTier, newTier int32, reason string) *SNSEarlyQuitUpdateNotification {
	return &SNSEarlyQuitUpdateNotification{
		EventType:    UpdateNotificationTierRestored,
		PreviousTier: oldTier,
		NewTier:      newTier,
		Reason:       reason,
	}
}

// NewLockoutActiveNotification creates a lockout active notification
func NewLockoutActiveNotification(penaltyLevel int32, durationSeconds int32) *SNSEarlyQuitUpdateNotification {
	expiresAt := time.Now().Add(time.Duration(durationSeconds) * time.Second).Unix()
	return &SNSEarlyQuitUpdateNotification{
		EventType:       UpdateNotificationLockoutActive,
		PenaltyLevel:    penaltyLevel,
		DurationSeconds: durationSeconds,
		ExpiresAt:       expiresAt,
		Reason:          "Matchmaking lockout is active",
	}
}

// NewLockoutClearedNotification creates a lockout cleared notification
func NewLockoutClearedNotification() *SNSEarlyQuitUpdateNotification {
	return &SNSEarlyQuitUpdateNotification{
		EventType:       UpdateNotificationLockoutCleared,
		DurationSeconds: 0,
		ExpiresAt:       time.Now().Unix(),
		Reason:          "Matchmaking lockout has been cleared",
	}
}

// ExpiresAtTime returns the expiry time as a time.Time object
func (n *SNSEarlyQuitUpdateNotification) ExpiresAtTime() time.Time {
	return time.Unix(n.ExpiresAt, 0)
}

// RemainingSeconds returns the seconds until the penalty expires (or 0 if already expired)
func (n *SNSEarlyQuitUpdateNotification) RemainingSeconds() int32 {
	remaining := time.Until(n.ExpiresAtTime()).Seconds()
	if remaining < 0 {
		return 0
	}
	return int32(remaining)
}
