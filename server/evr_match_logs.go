package server

import (
	"context"
	"fmt"
	"sync"

	"github.com/gofrs/uuid/v5"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.uber.org/zap"
)

const (
	matchLogDatabaseName   = "match_data"
	matchLogCollectionName = "log_entries"
)

type SessionRemoteLog interface {
	SessionUUID() uuid.UUID
}

type MatchLogEntry struct {
	MatchUUID string           `json:"match_uuid"`
	Message   SessionRemoteLog `json:"message"`
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
		ctx:      ctx,
		logger:   logger,
		entries:  make([]MatchLogEntry, 0),
		mongoURI: mongoURI,
	}
}

func (m *MatchLogManager) Start() {
	var err error
	m.client, err = mongo.Connect(m.ctx, options.Client().ApplyURI(m.mongoURI))
	if err != nil {
		m.logger.Error("Failed to connect to MongoDB", zap.Error(err))
		return
	}
	m.logger.Info("Connected to MongoDB")
	m.collection = m.client.Database(matchLogDatabaseName).Collection(matchLogCollectionName)
}

func (m *MatchLogManager) AddLog(message SessionRemoteLog) error {
	m.Lock()
	defer m.Unlock()

	if m.collection == nil {
		return fmt.Errorf("log manager not started")
	}

	_, err := m.collection.InsertOne(m.ctx, MatchLogEntry{
		MatchUUID: message.SessionUUID().String(),
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
