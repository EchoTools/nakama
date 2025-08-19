package server

import (
	"context"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// testLogger is a simple logger for testing
type testLogger struct {
	t *testing.T
}

func (l *testLogger) Debug(msg string, keysAndValues ...interface{}) {
	l.t.Log("DEBUG:", msg, keysAndValues)
}

func (l *testLogger) Info(msg string, keysAndValues ...interface{}) {
	l.t.Log("INFO:", msg, keysAndValues)
}

func (l *testLogger) Warn(msg string, keysAndValues ...interface{}) {
	l.t.Log("WARN:", msg, keysAndValues)
}

func (l *testLogger) Error(msg string, keysAndValues ...interface{}) {
	l.t.Log("ERROR:", msg, keysAndValues)
}

func (l *testLogger) WithField(key string, value interface{}) runtime.Logger {
	return l
}

func (l *testLogger) WithFields(fields map[string]interface{}) runtime.Logger {
	return l
}

func (l *testLogger) Fields() map[string]interface{} {
	return nil
}

func TestSessionsChannelManager_Basic(t *testing.T) {
	// Test that the sessions manager can be created without panicking
	ctx := context.Background()
	
	// Create a mock sessions manager (without Discord)
	manager := &SessionsChannelManager{
		ctx:            ctx,
		logger:         &testLogger{t: t},
		activeMessages: make(map[string]*SessionMessageTracker),
		stopCh:         make(chan struct{}),
	}
	
	// Test adding and removing a session tracker manually (without using RemoveSessionMessage)
	sessionID := uuid.Must(uuid.NewV4()).String()
	tracker := &SessionMessageTracker{
		SessionID:   sessionID,
		MessageID:   "test_message_123",
		ChannelID:   "test_channel_456",
		GuildID:     "test_guild_789",
		LastUpdated: time.Now(),
		CreatedAt:   time.Now(),
	}
	
	manager.activeMessages[sessionID] = tracker
	
	if len(manager.activeMessages) != 1 {
		t.Errorf("Expected 1 active message, got %d", len(manager.activeMessages))
	}
	
	// Manually remove instead of using RemoveSessionMessage to avoid storage calls
	delete(manager.activeMessages, sessionID)
	
	if len(manager.activeMessages) != 0 {
		t.Errorf("Expected 0 active messages after removal, got %d", len(manager.activeMessages))
	}
}

func TestSessionsChannelManager_EmbedCreation(t *testing.T) {
	// Test that session embeds can be created without panicking
	ctx := context.Background()
	
	manager := &SessionsChannelManager{
		ctx:            ctx,
		logger:         &testLogger{t: t},
		activeMessages: make(map[string]*SessionMessageTracker),
		stopCh:         make(chan struct{}),
	}
	
	// Create a mock match label
	matchID := MatchID{UUID: uuid.Must(uuid.NewV4())}
	label := &MatchLabel{
		ID:        matchID,
		StartTime: time.Now(),
		Mode:      evr.ModeArenaPublic,
		Players:   []PlayerInfo{},
		GameServer: &GameServerPresence{
			Endpoint: evr.Endpoint{},
		},
	}
	
	guildGroup := &GuildGroup{
		GroupMetadata: GroupMetadata{
			GuildID:           "test_guild",
			SessionsChannelID: "test_sessions_channel",
		},
	}
	
	// This should not panic
	embeds, components := manager.createSessionEmbed(label, guildGroup)
	
	if len(embeds) == 0 {
		t.Error("Expected at least one embed to be created")
	}
	
	if len(components) == 0 {
		t.Error("Expected at least one component to be created")
	}
	
	// Check that the first embed has the expected fields
	mainEmbed := embeds[0]
	if mainEmbed.Title == "" {
		t.Error("Expected embed title to be set")
	}
	
	if len(mainEmbed.Fields) == 0 {
		t.Error("Expected embed to have fields")
	}
}