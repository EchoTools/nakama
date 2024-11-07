package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.uber.org/zap"
)

const (
	matchLogDatabaseName   = "match_data"
	matchLogCollectionName = "log_entries"
)

type MatchLogEntry struct {
	MatchID   MatchID         `json:"match_id"`
	Timestamp time.Time       `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
}

type MatchLogManager struct {
	sync.Mutex

	ctx    context.Context
	logger *zap.Logger

	entries    []MatchLogEntry
	client     *mongo.Client
	mongoURI   string
	collection *mongo.Collection
}

func NewMatchLogManager(ctx context.Context, logger *zap.Logger, mongoURI string) *MatchLogManager {

	return &MatchLogManager{
		ctx:     ctx,
		logger:  logger,
		entries: make([]MatchLogEntry, 0),
	}
}

func (m *MatchLogManager) Start() error {
	var err error
	m.client, err = mongo.Connect(m.ctx, options.Client().ApplyURI(m.mongoURI))
	if err != nil {
		return fmt.Errorf("failed to connect to MongoDB: %v", err)
	}
	m.collection = m.client.Database(matchLogDatabaseName).Collection(matchLogCollectionName)

	return nil
}

func (m *MatchLogManager) AddLog(matchID MatchID, timestamp time.Time, message json.RawMessage) error {
	m.Lock()
	defer m.Unlock()

	if m.collection == nil {
		return fmt.Errorf("log manager not started")
	}

	_, err := m.collection.InsertOne(m.ctx, MatchLogEntry{
		MatchID:   matchID,
		Timestamp: timestamp,
		Message:   message,
	})
	if err != nil {
		return fmt.Errorf("failed to insert log entry: %v", err)
	}

	return nil
}

func (m *MatchLogManager) Close() error {
	return m.client.Disconnect(m.ctx)
}
