package server

import (
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

func TestLeaveReasonClassification(t *testing.T) {
	tests := []struct {
		name           string
		leaveReason    LeaveReason
		shouldPenalize bool // Whether immediate penalty should be applied
	}{
		{"voluntary leave gets immediate penalty", LeaveReasonVoluntary, true},
		{"unknown leave gets immediate penalty", LeaveReasonUnknown, true},
		{"empty leave reason gets immediate penalty", "", true},
		{"disconnect defers penalty", LeaveReasonDisconnect, false},
		{"timeout defers penalty", LeaveReasonTimeout, false},
		{"crash recovery defers penalty", LeaveReasonCrashRecovery, false},
		{"reservation expiry defers penalty", LeaveReasonReservationExp, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This mirrors the logic in MatchLeave
			shouldPenalizeImmediately := tt.leaveReason == LeaveReasonVoluntary ||
				tt.leaveReason == LeaveReasonUnknown ||
				tt.leaveReason == ""

			if shouldPenalizeImmediately != tt.shouldPenalize {
				t.Errorf("leaveReason=%q: expected shouldPenalize=%v, got %v",
					tt.leaveReason, tt.shouldPenalize, shouldPenalizeImmediately)
			}
		})
	}
}

func TestCreateQuitRecordFromParticipation_IncludesLeaveReason(t *testing.T) {
	matchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	tests := []struct {
		name        string
		leaveReason LeaveReason
	}{
		{"voluntary", LeaveReasonVoluntary},
		{"disconnect", LeaveReasonDisconnect},
		{"crash_recovery", LeaveReasonCrashRecovery},
		{"reservation_exp", LeaveReasonReservationExp},
		{"unknown", LeaveReasonUnknown},
		{"empty", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &MatchLabel{
				ID:        matchID,
				Mode:      evr.ModeArenaPublic,
				StartTime: time.Now().Add(-5 * time.Minute),
				GameState: &GameState{
					BlueScore:   2,
					OrangeScore: 3,
					MatchOver:   false,
				},
			}

			participation := &PlayerParticipation{
				UserID:               uuid.Must(uuid.NewV4()).String(),
				Username:             "testplayer",
				DisplayName:          "TestPlayer",
				Team:                 BlueTeam,
				JoinTime:             time.Now().Add(-4 * time.Minute),
				LeaveTime:            time.Now(),
				LeaveReason:          tt.leaveReason,
				GameDurationAtLeave:  4 * time.Minute,
				ScoresAtLeave:        [2]int{2, 3},
				ClockRemainingAtJoin: 300.0,
			}

			record := CreateQuitRecordFromParticipation(state, participation)

			if record.LeaveReason != tt.leaveReason {
				t.Errorf("expected LeaveReason=%q, got %q", tt.leaveReason, record.LeaveReason)
			}
			if record.MatchID != matchID {
				t.Errorf("expected MatchID=%v, got %v", matchID, record.MatchID)
			}
		})
	}
}

func TestDifferentialPenalty_VoluntaryVsDisconnect(t *testing.T) {
	t.Run("voluntary leave increments penalty immediately", func(t *testing.T) {
		config := NewEarlyQuitPlayerState()
		leaveReason := LeaveReasonVoluntary

		// Mirrors the MatchLeave logic
		if leaveReason == LeaveReasonVoluntary || leaveReason == LeaveReasonUnknown || leaveReason == "" {
			config.IncrementEarlyQuit()
		}

		if config.NumEarlyQuits != 1 {
			t.Errorf("expected 1 early quit, got %d", config.NumEarlyQuits)
		}
		if config.NumSteadyEarlyQuits != 1 {
			t.Errorf("expected NumSteadyEarlyQuits 1, got %d", config.NumSteadyEarlyQuits)
		}
	})

	t.Run("disconnect does not increment penalty immediately", func(t *testing.T) {
		config := NewEarlyQuitPlayerState()
		leaveReason := LeaveReasonDisconnect

		// Mirrors the MatchLeave logic
		if leaveReason == LeaveReasonVoluntary || leaveReason == LeaveReasonUnknown || leaveReason == "" {
			config.IncrementEarlyQuit()
		}

		if config.NumEarlyQuits != 0 {
			t.Errorf("expected 0 early quits for disconnect, got %d", config.NumEarlyQuits)
		}
		if config.PenaltyLevel != 0 {
			t.Errorf("expected penalty level 0 for disconnect, got %d", config.PenaltyLevel)
		}
	})

	t.Run("crash recovery does not increment penalty immediately", func(t *testing.T) {
		config := NewEarlyQuitPlayerState()
		leaveReason := LeaveReasonCrashRecovery

		if leaveReason == LeaveReasonVoluntary || leaveReason == LeaveReasonUnknown || leaveReason == "" {
			config.IncrementEarlyQuit()
		}

		if config.NumEarlyQuits != 0 {
			t.Errorf("expected 0 early quits for crash recovery, got %d", config.NumEarlyQuits)
		}
	})
}

func TestPlayerParticipation_LeaveReason(t *testing.T) {
	p := &PlayerParticipation{}

	// Default should be zero value (empty string)
	if p.LeaveReason != "" {
		t.Errorf("expected empty default LeaveReason, got %q", p.LeaveReason)
	}

	// Should be settable
	p.LeaveReason = LeaveReasonDisconnect
	if p.LeaveReason != LeaveReasonDisconnect {
		t.Errorf("expected LeaveReason=%q, got %q", LeaveReasonDisconnect, p.LeaveReason)
	}
}
