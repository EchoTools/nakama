package server

import (
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
)

func TestSessionClassification_String(t *testing.T) {
	tests := []struct {
		classification SessionClassification
		expected       string
	}{
		{ClassificationLeague, "league"},
		{ClassificationScrimmage, "scrimmage"},
		{ClassificationMixed, "mixed"},
		{ClassificationPickup, "pickup"},
		{ClassificationNone, "none"},
		{SessionClassification(999), "unknown"},
	}

	for _, test := range tests {
		result := test.classification.String()
		if result != test.expected {
			t.Errorf("Expected %s, got %s", test.expected, result)
		}
	}
}

func TestParseSessionClassification(t *testing.T) {
	tests := []struct {
		input    string
		expected SessionClassification
	}{
		{"league", ClassificationLeague},
		{"scrimmage", ClassificationScrimmage},
		{"mixed", ClassificationMixed},
		{"pickup", ClassificationPickup},
		{"none", ClassificationNone},
		{"invalid", ClassificationNone},
		{"", ClassificationNone},
	}

	for _, test := range tests {
		result := ParseSessionClassification(test.input)
		if result != test.expected {
			t.Errorf("Input %s: expected %v, got %v", test.input, test.expected, result)
		}
	}
}

func TestSessionClassification_CanPreempt(t *testing.T) {
	tests := []struct {
		name                      string
		requesting                SessionClassification
		target                    SessionClassification
		targetReservationExpired  bool
		expected                  bool
	}{
		{
			name:                     "League cannot be preempted",
			requesting:               ClassificationLeague,
			target:                   ClassificationLeague,
			targetReservationExpired: true,
			expected:                 false,
		},
		{
			name:                     "Scrimmage cannot preempt league",
			requesting:               ClassificationScrimmage,
			target:                   ClassificationLeague,
			targetReservationExpired: true,
			expected:                 false,
		},
		{
			name:                     "League can preempt scrimmage",
			requesting:               ClassificationLeague,
			target:                   ClassificationScrimmage,
			targetReservationExpired: false,
			expected:                 true,
		},
		{
			name:                     "Same classification with expired reservation",
			requesting:               ClassificationScrimmage,
			target:                   ClassificationScrimmage,
			targetReservationExpired: true,
			expected:                 true,
		},
		{
			name:                     "Same classification without expired reservation",
			requesting:               ClassificationScrimmage,
			target:                   ClassificationScrimmage,
			targetReservationExpired: false,
			expected:                 false,
		},
		{
			name:                     "Any classification can preempt none",
			requesting:               ClassificationPickup,
			target:                   ClassificationNone,
			targetReservationExpired: false,
			expected:                 true,
		},
		{
			name:                     "None cannot preempt none",
			requesting:               ClassificationNone,
			target:                   ClassificationNone,
			targetReservationExpired: true,
			expected:                 false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := test.requesting.CanPreempt(test.target, test.targetReservationExpired)
			if result != test.expected {
				t.Errorf("Expected %v, got %v", test.expected, result)
			}
		})
	}
}

func TestMatchReservation_IsExpired(t *testing.T) {
	now := time.Now()
	
	tests := []struct {
		name        string
		reservation *MatchReservation
		expected    bool
	}{
		{
			name: "Explicitly expired state",
			reservation: &MatchReservation{
				State: ReservationStateExpired,
			},
			expected: true,
		},
		{
			name: "Reserved state, start time in future",
			reservation: &MatchReservation{
				State:     ReservationStateReserved,
				StartTime: now.Add(10 * time.Minute),
			},
			expected: false,
		},
		{
			name: "Reserved state, start time 3 minutes ago",
			reservation: &MatchReservation{
				State:     ReservationStateReserved,
				StartTime: now.Add(-3 * time.Minute),
			},
			expected: false,
		},
		{
			name: "Reserved state, start time 6 minutes ago",
			reservation: &MatchReservation{
				State:     ReservationStateReserved,
				StartTime: now.Add(-6 * time.Minute),
			},
			expected: true,
		},
		{
			name: "Activated state, start time past",
			reservation: &MatchReservation{
				State:     ReservationStateActivated,
				StartTime: now.Add(-10 * time.Minute),
			},
			expected: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := test.reservation.IsExpired()
			if result != test.expected {
				t.Errorf("Expected %v, got %v", test.expected, result)
			}
		})
	}
}

func TestMatchReservation_CanBePreempted(t *testing.T) {
	now := time.Now()
	
	tests := []struct {
		name                      string
		reservation               *MatchReservation
		preemptingClassification  SessionClassification
		expected                  bool
	}{
		{
			name: "League reservation cannot be preempted",
			reservation: &MatchReservation{
				Classification: ClassificationLeague,
				State:         ReservationStateActivated,
				StartTime:     now.Add(-30 * time.Minute),
				EndTime:       now.Add(30 * time.Minute),
			},
			preemptingClassification: ClassificationLeague,
			expected:                 false,
		},
		{
			name: "Scrimmage can preempt mixed",
			reservation: &MatchReservation{
				Classification: ClassificationMixed,
				State:         ReservationStateActivated,
				StartTime:     now.Add(-30 * time.Minute),
				EndTime:       now.Add(30 * time.Minute),
			},
			preemptingClassification: ClassificationScrimmage,
			expected:                 true,
		},
		{
			name: "Same classification with expired end time",
			reservation: &MatchReservation{
				Classification: ClassificationPickup,
				State:         ReservationStateActivated,
				StartTime:     now.Add(-90 * time.Minute),
				EndTime:       now.Add(-10 * time.Minute), // Ended 10 minutes ago
			},
			preemptingClassification: ClassificationPickup,
			expected:                 true,
		},
		{
			name: "Expired reservation",
			reservation: &MatchReservation{
				Classification: ClassificationPickup,
				State:         ReservationStateReserved,
				StartTime:     now.Add(-10 * time.Minute), // Started 10 minutes ago but never activated
				EndTime:       now.Add(50 * time.Minute),
			},
			preemptingClassification: ClassificationPickup,
			expected:                 true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := test.reservation.CanBePreempted(test.preemptingClassification)
			if result != test.expected {
				t.Errorf("Expected %v, got %v", test.expected, result)
			}
		})
	}
}

func TestMatchReservation_UpdateState(t *testing.T) {
	reservation := &MatchReservation{
		State:        ReservationStateReserved,
		StateHistory: []ReservationStateTransition{},
	}

	reservation.UpdateState(ReservationStateActivated, "match allocated")

	if reservation.State != ReservationStateActivated {
		t.Errorf("Expected state %v, got %v", ReservationStateActivated, reservation.State)
	}

	if len(reservation.StateHistory) != 1 {
		t.Errorf("Expected 1 state transition, got %d", len(reservation.StateHistory))
	}

	transition := reservation.StateHistory[0]
	if transition.FromState != ReservationStateReserved {
		t.Errorf("Expected from state %v, got %v", ReservationStateReserved, transition.FromState)
	}

	if transition.ToState != ReservationStateActivated {
		t.Errorf("Expected to state %v, got %v", ReservationStateActivated, transition.ToState)
	}

	if transition.Reason != "match allocated" {
		t.Errorf("Expected reason 'match allocated', got '%s'", transition.Reason)
	}
}

func TestReservationConflictDetection(t *testing.T) {
	groupID := uuid.Must(uuid.NewV4())
	baseTime := time.Now().Truncate(time.Minute)
	
	existing := &MatchReservation{
		ID:        "existing",
		GroupID:   groupID,
		StartTime: baseTime.Add(60 * time.Minute),
		EndTime:   baseTime.Add(120 * time.Minute),
		State:     ReservationStateActivated,
	}

	tests := []struct {
		name          string
		requestStart  time.Time
		requestEnd    time.Time
		shouldConflict bool
		conflictType   string
	}{
		{
			name:          "No conflict - before existing",
			requestStart:  baseTime,
			requestEnd:    baseTime.Add(30 * time.Minute),
			shouldConflict: false,
		},
		{
			name:          "No conflict - after existing",
			requestStart:  baseTime.Add(150 * time.Minute),
			requestEnd:    baseTime.Add(180 * time.Minute),
			shouldConflict: false,
		},
		{
			name:          "Overlap conflict - starts during existing",
			requestStart:  baseTime.Add(90 * time.Minute),
			requestEnd:    baseTime.Add(150 * time.Minute),
			shouldConflict: true,
			conflictType:   "overlap",
		},
		{
			name:          "Overlap conflict - ends during existing",
			requestStart:  baseTime.Add(30 * time.Minute),
			requestEnd:    baseTime.Add(90 * time.Minute),
			shouldConflict: true,
			conflictType:   "overlap",
		},
		{
			name:          "Complete overlap - encompasses existing",
			requestStart:  baseTime.Add(30 * time.Minute),
			requestEnd:    baseTime.Add(150 * time.Minute),
			shouldConflict: true,
			conflictType:   "overlap",
		},
		{
			name:          "Adjacent - ends when existing starts",
			requestStart:  baseTime.Add(30 * time.Minute),
			requestEnd:    baseTime.Add(60 * time.Minute),
			shouldConflict: true,
			conflictType:   "adjacent",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Simulate conflict detection logic
			hasConflict := test.requestStart.Before(existing.EndTime) && test.requestEnd.After(existing.StartTime)
			
			if hasConflict != test.shouldConflict {
				t.Errorf("Expected conflict: %v, got: %v", test.shouldConflict, hasConflict)
			}

			if hasConflict && test.conflictType != "" {
				// Check conflict type detection
				var conflictType string
				if test.requestStart.Equal(existing.EndTime) || test.requestEnd.Equal(existing.StartTime) {
					conflictType = "adjacent"
				} else {
					conflictType = "overlap"
				}

				if conflictType != test.conflictType {
					t.Errorf("Expected conflict type: %s, got: %s", test.conflictType, conflictType)
				}
			}
		})
	}
}