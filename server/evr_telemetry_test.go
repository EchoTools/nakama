package server

import (
	"context"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

// telemetryTestLogger is a simple logger for testing
type telemetryTestLogger struct {
	messages []string
}

func (l *telemetryTestLogger) Debug(format string, v ...interface{}) {
	l.messages = append(l.messages, "DEBUG")
}

func (l *telemetryTestLogger) Info(format string, v ...interface{}) {
	l.messages = append(l.messages, "INFO")
}

func (l *telemetryTestLogger) Warn(format string, v ...interface{}) {
	l.messages = append(l.messages, "WARN")
}

func (l *telemetryTestLogger) Error(format string, v ...interface{}) {
	l.messages = append(l.messages, "ERROR")
}

func (l *telemetryTestLogger) Fields() map[string]interface{} {
	return make(map[string]interface{})
}

func (l *telemetryTestLogger) WithField(key string, value interface{}) runtime.Logger {
	return l
}

func (l *telemetryTestLogger) WithFields(map[string]interface{}) runtime.Logger {
	return l
}

func TestEventJournal_Journal(t *testing.T) {
	// Test that event journaling works without Redis (graceful degradation)
	logger := &telemetryTestLogger{}

	eventJournal := NewEventJournal(nil, logger)

	event := &JournalEvent{
		Type:      "test",
		Timestamp: time.Now(),
		UserID:    "test-user",
		SessionID: "test-session",
		Data:      map[string]interface{}{"action": "test"},
	}

	err := eventJournal.Journal(context.Background(), "test_events", event)
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	// Check that debug message was logged
	if len(logger.messages) == 0 {
		t.Error("Expected debug message to be logged")
	}
}

func TestMatchSummary_Creation(t *testing.T) {
	summary := &MatchSummary{
		MatchID:         "test-match-123",
		Players:         []string{"player1", "player2"},
		DurationSeconds: 300,
		MinPing:         10,
		MaxPing:         50,
		AvgPing:         25.5,
		FinalRoundScores: map[string]int{
			"player1": 100,
			"player2": 90,
		},
		MatchLabel: "arena-public",
		CreatedAt:  time.Now(),
	}

	if summary.MatchID != "test-match-123" {
		t.Errorf("Expected MatchID to be 'test-match-123', got %s", summary.MatchID)
	}
	if len(summary.Players) != 2 {
		t.Errorf("Expected 2 players, got %d", len(summary.Players))
	}
	if summary.DurationSeconds != 300 {
		t.Errorf("Expected duration 300, got %d", summary.DurationSeconds)
	}
	if summary.AvgPing != 25.5 {
		t.Errorf("Expected avg ping 25.5, got %f", summary.AvgPing)
	}
	if _, exists := summary.FinalRoundScores["player1"]; !exists {
		t.Error("Expected player1 to exist in final scores")
	}
}

func TestStreamModeLobbySessionTelemetry(t *testing.T) {
	// Test that the new stream mode constant is set to the expected value (0x16)
	const expectedValue = 0x16
	if StreamModeLobbySessionTelemetry != expectedValue {
		t.Errorf("Expected StreamModeLobbySessionTelemetry to be %d (0x16), got %d", expectedValue, StreamModeLobbySessionTelemetry)
	}
	if StreamModeLobbySessionTelemetry <= StreamModeMatchmaker {
		t.Error("StreamModeLobbySessionTelemetry should be greater than StreamModeMatchmaker")
	}
}

func TestTelemetrySubscription_Creation(t *testing.T) {
	sessionID := uuid.Must(uuid.NewV4())
	userID := uuid.Must(uuid.NewV4())
	lobbyID := uuid.Must(uuid.NewV4())

	subscription := &TelemetrySubscription{
		SessionID: sessionID,
		UserID:    userID,
		LobbyID:   lobbyID,
		Active:    true,
	}

	if !subscription.Active {
		t.Error("Expected subscription to be active")
	}
	if subscription.SessionID == uuid.Nil {
		t.Error("Expected non-nil session ID")
	}
	if subscription.UserID == uuid.Nil {
		t.Error("Expected non-nil user ID")
	}
	if subscription.LobbyID == uuid.Nil {
		t.Error("Expected non-nil lobby ID")
	}
}
