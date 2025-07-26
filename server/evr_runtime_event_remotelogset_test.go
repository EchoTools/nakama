package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// Mock implementations for testing

type MockLogger struct {
	mock.Mock
	fields map[string]interface{}
}

func NewMockLogger() *MockLogger {
	return &MockLogger{
		fields: make(map[string]interface{}),
	}
}

func (m *MockLogger) Info(msg string, keysAndValues ...interface{}) {
	args := []interface{}{msg}
	args = append(args, keysAndValues...)
	m.Called(args...)
}

func (m *MockLogger) Warn(msg string, keysAndValues ...interface{}) {
	args := []interface{}{msg}
	args = append(args, keysAndValues...)
	m.Called(args...)
}

func (m *MockLogger) Error(msg string, keysAndValues ...interface{}) {
	args := []interface{}{msg}
	args = append(args, keysAndValues...)
	m.Called(args...)
}

func (m *MockLogger) Debug(msg string, keysAndValues ...interface{}) {
	args := []interface{}{msg}
	args = append(args, keysAndValues...)
	m.Called(args...)
}

func (m *MockLogger) WithField(key string, value interface{}) runtime.Logger {
	newFields := make(map[string]interface{})
	for k, v := range m.fields {
		newFields[k] = v
	}
	newFields[key] = value
	m.Called(key, value)
	return &MockLogger{fields: newFields}
}

func (m *MockLogger) WithFields(fields map[string]interface{}) runtime.Logger {
	newFields := make(map[string]interface{})
	for k, v := range m.fields {
		newFields[k] = v
	}
	for k, v := range fields {
		newFields[k] = v
	}
	m.Called(fields)
	return &MockLogger{fields: newFields}
}

func (m *MockLogger) Fields() map[string]interface{} {
	return m.fields
}

type TestNakamaModule struct {
	runtime.NakamaModule
	mock.Mock
}

func (m *TestNakamaModule) LeaderboardRecordWrite(ctx context.Context, id, userID, username string, score, subscore int64, metadata map[string]interface{}, override *int) (*api.LeaderboardRecord, error) {
	args := m.Called(ctx, id, userID, username, score, subscore, metadata, override)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	// Return a basic mock leaderboard record
	return &api.LeaderboardRecord{
		LeaderboardId: id,
		OwnerId:      userID,
		Username:     &wrapperspb.StringValue{Value: username},
		Score:        score,
		Subscore:     subscore,
	}, args.Error(1)
}

func (m *TestNakamaModule) LeaderboardCreate(ctx context.Context, id string, authoritative bool, sortOrder, operator, resetSchedule string, metadata map[string]interface{}, enableRanks bool) error {
	args := m.Called(ctx, id, authoritative, sortOrder, operator, resetSchedule, metadata, enableRanks)
	return args.Error(0)
}

type MockSessionRegistry struct {
	mock.Mock
}

func (m *MockSessionRegistry) Get(sessionID uuid.UUID) Session {
	args := m.Called(sessionID)
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(Session)
}

type MockSession struct {
	mock.Mock
	userID    uuid.UUID
	sessionID uuid.UUID
	username  string
	ctx       context.Context
}

func (m *MockSession) UserID() uuid.UUID {
	return m.userID
}

func (m *MockSession) SessionID() uuid.UUID {
	return m.sessionID
}

func (m *MockSession) Username() string {
	return m.username
}

func (m *MockSession) Context() context.Context {
	return m.ctx
}

type TestMatchRegistry struct {
	MatchRegistry
	mock.Mock
}

func (m *TestMatchRegistry) SendData(matchID uuid.UUID, node string, userID, sessionID uuid.UUID, username, fromNode string, opCode int64, data []byte, reliable bool, receiveTime int64) bool {
	args := m.Called(matchID, node, userID, sessionID, username, fromNode, opCode, data, reliable, receiveTime)
	return args.Bool(0)
}

// Test data helpers

func createTestMatchTypeStats() evr.MatchTypeStats {
	return evr.MatchTypeStats{
		ArenaWins:                    5,
		ArenaLosses:                  2,
		Goals:                        10,
		Saves:                        8,
		Assists:                      12,
		Points:                       45,
		Stuns:                        3,
		Passes:                       25,
		Catches:                      20,
		Steals:                       4,
		Blocks:                       6,
		Interceptions:                7,
		ShotsOnGoal:                  15,
		PossessionTime:               120.5,
		HighestPoints:                15,
		XP:                           1250.0,
		AveragePointsPerGame:         9.0,
		GoalsPerGame:                 2.0,
		SavesPerGame:                 1.6,
		AssistsPerGame:               2.4,
		StunsPerGame:                 0.6,
		ArenaWinPercentage:           71.4,
		GoalScorePercentage:          66.7,
		GoalSavePercentage:           53.3,
		BlockPercentage:              40.0,
	}
}

func createTestRemoteLogPostMatchTypeStats() *evr.RemoteLogPostMatchTypeStats {
	sessionUUID := uuid.Must(uuid.NewV4())
	stats := createTestMatchTypeStats()
	
	return &evr.RemoteLogPostMatchTypeStats{
		GenericRemoteLog: evr.GenericRemoteLog{
			MessageData: "POST_MATCH_MATCH_TYPE_STATS",
			Type:        "POST_MATCH_MATCH_TYPE_STATS",
			XPID:        "user123_xpid",
		},
		SessionUUIDStr: sessionUUID.String(),
		MatchType:      "echo_arena",
		Stats:          stats,
	}
}

func createTestMatchLabel(sessionUUID uuid.UUID) *MatchLabel {
	groupID := uuid.Must(uuid.NewV4())
	userID := uuid.Must(uuid.NewV4())
	sessionID := uuid.Must(uuid.NewV4())
	evrID := evr.EvrId{PlatformCode: 1, AccountId: 12345}
	
	return &MatchLabel{
		ID:         MatchID{UUID: sessionUUID, Node: "test-node"},
		Open:       false,
		LobbyType:  UnassignedLobby,
		Mode:       evr.ModeArenaPublic,
		Level:      evr.ToSymbol("mpl_arena_a"),
		StartTime:  time.Now(),
		Size:       4,
		MaxSize:    8,
		GroupID:    &groupID,
		TeamSize:   4,
		Players: []PlayerInfo{
			{
				UserID:      userID.String(),
				SessionID:   sessionID.String(),
				DisplayName: "TestPlayer",
				Team:        BlueTeam,
				EvrID:       evrID,
				DiscordID:   "discord123",
			},
		},
	}
}

func createTestEventRemoteLogSet() *EventRemoteLogSet {
	sessionUUID := uuid.Must(uuid.NewV4())
	evrID := evr.EvrId{PlatformCode: 1, AccountId: 12345}
	
	// Create JSON representation of the RemoteLogPostMatchTypeStats
	statsMsg := createTestRemoteLogPostMatchTypeStats()
	statsMsg.SessionUUIDStr = sessionUUID.String()
	statsJSON, _ := json.Marshal(statsMsg)
	
	remoteLogSet := &evr.RemoteLogSet{
		EvrID:    evrID,
		LogLevel: evr.Info,
		Logs:     []string{string(statsJSON)},
	}
	
	return &EventRemoteLogSet{
		Node:         "test-node",
		UserID:       "user123",
		SessionID:    uuid.Must(uuid.NewV4()).String(),
		XPID:         evrID,
		Username:     "TestUser",
		RemoteLogSet: remoteLogSet,
	}
}

func TestTypeStatsToScoreMap_AllFieldsSet(t *testing.T) {

	// ArenaStatistics fields must match dummyStats for this test to work
	// so we use evr.MatchTypeStats as dummyStats for the actual code
	// but here we test the logic with a similar struct

	// Use evr.ArenaStatistics for real test
	arenaStats := evr.ArenaStatistics{
		ArenaWins:   &evr.StatisticValue{Value: 10},
		ArenaLosses: &evr.StatisticValue{Value: 1},
		Goals:       &evr.StatisticValue{Value: 5},
		Saves:       &evr.StatisticValue{Value: 2},
		Level:       &evr.StatisticValue{Value: 4},
	}

	wantedEntries := []*StatisticsQueueEntry{
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "ArenaLosses",
				Operator:      "set",
				ResetSchedule: "daily",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       1000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "ArenaLosses",
				Operator:      "set",
				ResetSchedule: "weekly",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       1000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "ArenaLosses",
				Operator:      "set",
				ResetSchedule: "alltime",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       1000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "ArenaWins",
				Operator:      "set",
				ResetSchedule: "daily",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       10000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "ArenaWins",
				Operator:      "set",
				ResetSchedule: "weekly",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       10000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "ArenaWins",
				Operator:      "set",
				ResetSchedule: "alltime",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       10000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "Goals",
				Operator:      "set",
				ResetSchedule: "daily",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       5000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "Goals",
				Operator:      "set",
				ResetSchedule: "weekly",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       5000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "Goals",
				Operator:      "set",
				ResetSchedule: "alltime",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       5000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "Saves",
				Operator:      "set",
				ResetSchedule: "daily",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       2000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "Saves",
				Operator:      "set",
				ResetSchedule: "weekly",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       2000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "Saves",
				Operator:      "set",
				ResetSchedule: "alltime",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       2000000000,
			Subscore:    0,
			Metadata:    nil,
		},
	}

	matchTypeStats := evr.MatchTypeStats{
		ArenaWins:   int64(arenaStats.ArenaWins.Value),
		ArenaLosses: int64(arenaStats.ArenaLosses.Value),
		Goals:       int64(arenaStats.Goals.Value),
		Saves:       int64(arenaStats.Saves.Value),
	}

	userID := "user123"
	displayName := "TestUser"
	groupID := "group456"
	mode := evr.ModeArenaPublic

	entries, err := typeStatsToScoreMap(userID, displayName, groupID, mode, matchTypeStats)
	assert.NoError(t, err)
	assert.NotEmpty(t, entries)

	assert.Equal(t, len(entries), 12, "There should be 12 entries for all stats and reset schedules")
	// Each non-zero stat should produce 3 entries (daily, weekly, alltime)

	for i, entry := range entries {
		assert.Equal(t, wantedEntries[i].BoardMeta, entry.BoardMeta, "BoardMeta mismatch at entry %d", i)
		assert.Equal(t, wantedEntries[i].UserID, entry.UserID, "UserID mismatch at entry %d", i)
		assert.Equal(t, wantedEntries[i].DisplayName, entry.DisplayName, "DisplayName mismatch at entry %d", i)
		assert.Equal(t, wantedEntries[i].Score, entry.Score, "Score mismatch at entry %d", i)
		assert.Equal(t, wantedEntries[i].Subscore, entry.Subscore, "Subscore mismatch at entry %d", i)
		assert.Equal(t, wantedEntries[i].Metadata, entry.Metadata, "Metadata mismatch at entry %d", i)
	}

}

// Tests for NewStatisticsQueue
func TestNewStatisticsQueue(t *testing.T) {
	t.Run("creates queue with proper configuration", func(t *testing.T) {
		zapLogger := NewConsoleLogger(nil, false)
		logger := NewRuntimeGoLogger(zapLogger)
		mockNK := &TestNakamaModule{}
		
		queue := NewStatisticsQueue(logger, &sql.DB{}, mockNK)
		
		assert.NotNil(t, queue)
		assert.NotNil(t, queue.ch)
		assert.Equal(t, logger, queue.logger)
		
		// Verify channel buffer size (8*3*100 = 2400)
		assert.Equal(t, cap(queue.ch), 8*3*100)
	})
}

// Tests for StatisticsQueue.Add
func TestStatisticsQueue_Add(t *testing.T) {
	t.Run("successfully adds entries to queue", func(t *testing.T) {
		// Use the real logger to avoid mocking complexity
		zapLogger := NewConsoleLogger(nil, false)
		logger := NewRuntimeGoLogger(zapLogger)
		
		queue := &StatisticsQueue{
			logger: logger,
			ch:     make(chan []*StatisticsQueueEntry, 10),
		}
		
		entries := []*StatisticsQueueEntry{
			{
				BoardMeta: LeaderboardMeta{
					GroupID:       "group123",
					Mode:          evr.ModeArenaPublic,
					StatName:      "Goals",
					Operator:      OperatorSet,
					ResetSchedule: evr.ResetScheduleDaily,
				},
				UserID:      "user123",
				DisplayName: "TestUser",
				Score:       5000000000,
			},
			{
				BoardMeta: LeaderboardMeta{
					GroupID:       "group123",
					Mode:          evr.ModeArenaPublic,
					StatName:      "Saves",
					Operator:      OperatorSet,
					ResetSchedule: evr.ResetScheduleDaily,
				},
				UserID:      "user123",
				DisplayName: "TestUser",
				Score:       3000000000,
			},
		}
		
		err := queue.Add(entries)
		
		assert.NoError(t, err)
		
		// Verify entries were added to channel
		select {
		case receivedEntries := <-queue.ch:
			assert.Equal(t, entries, receivedEntries)
		case <-time.After(100 * time.Millisecond):
			t.Fatal("Expected entries to be in channel")
		}
	})
	
	t.Run("returns error when queue is full", func(t *testing.T) {
		zapLogger := NewConsoleLogger(nil, false)
		logger := NewRuntimeGoLogger(zapLogger)
		
		queue := &StatisticsQueue{
			logger: logger,
			ch:     make(chan []*StatisticsQueueEntry, 0), // Buffer size 0 to simulate full queue
		}
		
		entries := []*StatisticsQueueEntry{{}}
		
		err := queue.Add(entries)
		
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "queue full")
	})
	
	t.Run("handles empty entries array", func(t *testing.T) {
		zapLogger := NewConsoleLogger(nil, false)
		logger := NewRuntimeGoLogger(zapLogger)
		
		queue := &StatisticsQueue{
			logger: logger,
			ch:     make(chan []*StatisticsQueueEntry, 10),
		}
		
		err := queue.Add([]*StatisticsQueueEntry{})
		
		assert.NoError(t, err)
	})
}

// Enhanced tests for typeStatsToScoreMap
func TestTypeStatsToScoreMap_Comprehensive(t *testing.T) {
	t.Run("handles zero values correctly", func(t *testing.T) {
		stats := evr.MatchTypeStats{
			ArenaWins:   0, // Should be skipped
			ArenaLosses: 5, // Should be included
			Goals:       0, // Should be skipped
		}
		
		entries, err := typeStatsToScoreMap("user123", "TestUser", "group123", evr.ModeArenaPublic, stats)
		
		assert.NoError(t, err)
		// Only ArenaLosses should produce entries (3 entries for 3 reset schedules)
		assert.Len(t, entries, 3)
		
		for _, entry := range entries {
			assert.Equal(t, "ArenaLosses", entry.BoardMeta.StatName)
			assert.Equal(t, int64(5000000000), entry.Score) // 5 * LeaderboardScoreScalingFactor
		}
	})
	
	t.Run("handles different stat operators", func(t *testing.T) {
		// This test checks that the operator mapping works correctly
		// Note: The actual operator assignment happens based on struct tags in the MatchTypeStats
		stats := evr.MatchTypeStats{
			ArenaWins:   1, // Typically "set" operator
			Goals:       2, // Typically "set" operator  
			XP:          1000.5, // Typically "set" operator for float values
		}
		
		entries, err := typeStatsToScoreMap("user123", "TestUser", "group123", evr.ModeArenaPublic, stats)
		
		assert.NoError(t, err)
		assert.NotEmpty(t, entries)
		
		// Verify that all entries have valid operators
		for _, entry := range entries {
			assert.Contains(t, []LeaderboardOperator{OperatorBest, OperatorSet, OperatorIncrement, OperatorDecrement}, entry.BoardMeta.Operator)
		}
	})
	
	t.Run("handles float values correctly", func(t *testing.T) {
		stats := evr.MatchTypeStats{
			XP:                       1250.75,
			AveragePointsPerGame:     9.5,
			PossessionTime:           120.25,
			ArenaWinPercentage:       75.5,
		}
		
		entries, err := typeStatsToScoreMap("user123", "TestUser", "group123", evr.ModeArenaPublic, stats)
		
		assert.NoError(t, err)
		assert.NotEmpty(t, entries)
		
		// Find XP entries and verify float conversion
		xpEntries := make([]*StatisticsQueueEntry, 0)
		for _, entry := range entries {
			if entry.BoardMeta.StatName == "XP" {
				xpEntries = append(xpEntries, entry)
			}
		}
		assert.Len(t, xpEntries, 3) // One for each reset schedule
		
		expectedScore := int64(1250.75 * LeaderboardScoreScalingFactor)
		for _, entry := range xpEntries {
			assert.Equal(t, expectedScore, entry.Score)
		}
	})
	
	t.Run("generates correct reset schedules", func(t *testing.T) {
		stats := evr.MatchTypeStats{
			Goals: 5,
		}
		
		entries, err := typeStatsToScoreMap("user123", "TestUser", "group123", evr.ModeArenaPublic, stats)
		
		assert.NoError(t, err)
		assert.Len(t, entries, 3) // daily, weekly, alltime
		
		schedules := make([]evr.ResetSchedule, 0, 3)
		for _, entry := range entries {
			schedules = append(schedules, entry.BoardMeta.ResetSchedule)
		}
		
		assert.Contains(t, schedules, evr.ResetScheduleDaily)
		assert.Contains(t, schedules, evr.ResetScheduleWeekly)
		assert.Contains(t, schedules, evr.ResetScheduleAllTime)
	})
}

// Test for basic pipeline functionality
func TestTypeStatsToScoreMap_PipelineIntegrity(t *testing.T) {
	t.Run("validates complete pipeline from stats to queue entries", func(t *testing.T) {
		// Create realistic test data
		stats := createTestMatchTypeStats()
		
		userID := "user123"
		displayName := "TestUser"
		groupID := "group456"
		mode := evr.ModeArenaPublic
		
		// Convert stats to queue entries
		entries, err := typeStatsToScoreMap(userID, displayName, groupID, mode, stats)
		
		require.NoError(t, err)
		require.NotEmpty(t, entries)
		
		// Validate that each entry has correct structure and data
		statCounts := make(map[string]int)
		for _, entry := range entries {
			// Verify basic structure
			assert.Equal(t, userID, entry.UserID)
			assert.Equal(t, displayName, entry.DisplayName)
			assert.Equal(t, groupID, entry.BoardMeta.GroupID)
			assert.Equal(t, mode, entry.BoardMeta.Mode)
			
			// Verify score is positive for non-zero stats
			assert.Greater(t, entry.Score, int64(0))
			
			// Verify operator is valid
			assert.Contains(t, []LeaderboardOperator{OperatorBest, OperatorSet, OperatorIncrement, OperatorDecrement}, entry.BoardMeta.Operator)
			
			// Verify reset schedule is valid
			assert.Contains(t, []evr.ResetSchedule{evr.ResetScheduleDaily, evr.ResetScheduleWeekly, evr.ResetScheduleAllTime}, entry.BoardMeta.ResetSchedule)
			
			// Count stats by name
			statCounts[entry.BoardMeta.StatName]++
		}
		
		// Verify that each non-zero stat appears exactly 3 times (for each reset schedule)
		expectedStats := []string{"ArenaWins", "ArenaLosses", "Goals", "Saves", "Assists", "Points", "Stuns", "Passes", "Catches", "Steals", "Blocks", "Interceptions", "ShotsOnGoal", "PossessionTime", "HighestPoints", "XP", "AveragePointsPerGame", "GoalsPerGame", "SavesPerGame", "AssistsPerGame", "StunsPerGame", "ArenaWinPercentage", "GoalScorePercentage", "GoalSavePercentage", "BlockPercentage"}
		
		for _, statName := range expectedStats {
			count, exists := statCounts[statName]
			if exists {
				assert.Equal(t, 3, count, "Stat %s should appear exactly 3 times (once for each reset schedule)", statName)
			}
		}
		
		// Test adding to statistics queue
		zapLogger := NewConsoleLogger(nil, false)
		logger := NewRuntimeGoLogger(zapLogger)
		
		queue := &StatisticsQueue{
			logger: logger,
			ch:     make(chan []*StatisticsQueueEntry, 100),
		}
		
		err = queue.Add(entries)
		assert.NoError(t, err)
		
		// Verify entries are in the queue
		select {
		case receivedEntries := <-queue.ch:
			assert.Equal(t, entries, receivedEntries)
		case <-time.After(100 * time.Millisecond):
			t.Fatal("Expected entries to be in channel")
		}
	})
}

// Test for remote log event processing
func TestEventRemoteLogSet_ProcessStatsMessage(t *testing.T) {
	t.Run("processes POST_MATCH_MATCH_TYPE_STATS message correctly", func(t *testing.T) {
		// Create a realistic remote log set containing stats message
		event := createTestEventRemoteLogSet()
		
		// Parse the logs to verify they contain the expected message type
		zapLogger := NewConsoleLogger(nil, false)
		logger := NewRuntimeGoLogger(zapLogger)
		
		logs, err := event.unmarshalLogs(logger, event.RemoteLogSet.Logs)
		
		require.NoError(t, err)
		require.Len(t, logs, 1)
		
		// Verify the parsed log is the correct type
		statsMsg, ok := logs[0].(*evr.RemoteLogPostMatchTypeStats)
		require.True(t, ok, "Expected RemoteLogPostMatchTypeStats message")
		
		// Verify the message contains expected data
		assert.Equal(t, "POST_MATCH_MATCH_TYPE_STATS", statsMsg.MessageType())
		assert.NotNil(t, statsMsg.Stats)
		assert.False(t, statsMsg.SessionUUID().IsNil())
		
		// Verify stats contain expected values
		assert.Equal(t, int64(5), statsMsg.Stats.ArenaWins)
		assert.Equal(t, int64(2), statsMsg.Stats.ArenaLosses)
		assert.Equal(t, int64(10), statsMsg.Stats.Goals)
		assert.Equal(t, int64(8), statsMsg.Stats.Saves)
		
		// Test that IsWinner works correctly (should be true since ArenaWins > 0)
		assert.True(t, statsMsg.IsWinner())
	})
}
