package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

var _ = Event(&EventMatchDataJournal{})

type EventMatchDataJournal struct {
	MatchID string                 `json:"match_id"`
	Entry   *MatchDataJournalEntry `json:"entry"`
	Journal *MatchDataJournal      `json:"journal,omitempty"` // Full journal for persistence
}

func NewEventMatchDataJournal(matchID string, entry *MatchDataJournalEntry) *EventMatchDataJournal {
	return &EventMatchDataJournal{
		MatchID: matchID,
		Entry:   entry,
	}
}

func NewEventMatchDataJournalPersist(journal *MatchDataJournal) *EventMatchDataJournal {
	return &EventMatchDataJournal{
		MatchID: journal.MatchID,
		Journal: journal,
	}
}

func (e *EventMatchDataJournal) Process(ctx context.Context, logger runtime.Logger, dispatcher *EventDispatcher) error {
	logger = logger.WithFields(map[string]any{
		"match_id": e.MatchID,
		"event":    "match_data_journal",
	})

	// If this is a journal persistence event, handle it differently
	if e.Journal != nil {
		return e.persistJournal(ctx, logger, dispatcher)
	}

	// This is a new entry event, add to the journal
	return e.addToJournal(ctx, logger, dispatcher)
}

func (e *EventMatchDataJournal) addToJournal(ctx context.Context, logger runtime.Logger, dispatcher *EventDispatcher) error {
	dispatcher.Lock()
	defer dispatcher.Unlock()

	matchID, err := MatchIDFromString(e.MatchID)
	if err != nil {
		return fmt.Errorf("failed to parse match ID: %w", err)
	}

	j, ok := dispatcher.matchJournals[matchID]
	if !ok {
		dispatcher.matchJournals[matchID] = NewMatchDataJournal(matchID)
		j = dispatcher.matchJournals[matchID]
		logger.Debug("Created new match data journal")
	}

	j.Events = append(j.Events, e.Entry)
	j.UpdatedAt = time.Now().UTC()

	logger.WithField("total_events", len(j.Events)).Debug("Added entry to match data journal")

	// Queue to Redis if we have too many events or the journal is getting old
	if len(j.Events) >= matchDataJournalEventThreshold || time.Since(j.CreatedAt) > matchDataJournalMaxAge {
		if err := e.queueToRedis(ctx, logger, dispatcher, j); err != nil {
			logger.WithField("error", err).Warn("Failed to queue journal to Redis")
		} else {
			// Clear the journal after successful queuing
			delete(dispatcher.matchJournals, matchID)
			logger.Debug("Queued journal to Redis and cleared from memory")
		}
	}

	return nil
}

func (e *EventMatchDataJournal) persistJournal(ctx context.Context, logger runtime.Logger, dispatcher *EventDispatcher) error {
	if dispatcher.mongo == nil {
		logger.Warn("MongoDB client not available, skipping journal persistence")
		return nil
	}

	collection := dispatcher.mongo.Database(matchDataDatabaseName).Collection(matchDataCollectionName)

	if _, err := collection.InsertOne(ctx, e.Journal); err != nil {
		return fmt.Errorf("failed to insert match journal to MongoDB: %w", err)
	}

	logger.WithField("events_count", len(e.Journal.Events)).Info("Successfully persisted match journal to MongoDB")
	return nil
}

func (e *EventMatchDataJournal) queueToRedis(ctx context.Context, logger runtime.Logger, dispatcher *EventDispatcher, journal *MatchDataJournal) error {
	// Get Redis client from dispatcher if available
	redisClient := dispatcher.getRedisClient()
	if redisClient == nil {
		logger.Warn("Redis client not available, falling back to direct MongoDB insertion")
		return e.directMongoPersist(ctx, logger, dispatcher, journal)
	}

	// Serialize the journal
	journalBytes, err := json.Marshal(journal)
	if err != nil {
		return fmt.Errorf("failed to marshal journal for Redis queue: %w", err)
	}

	// Push to Redis queue
	queueKey := redisMatchDataJournalQueueKey
	if err := redisClient.LPush(queueKey, string(journalBytes)).Err(); err != nil {
		logger.WithField("error", err).Warn("Failed to push to Redis queue, falling back to direct MongoDB")
		return e.directMongoPersist(ctx, logger, dispatcher, journal)
	}

	logger.Debug("Successfully queued journal to Redis")
	return nil
}

func (e *EventMatchDataJournal) directMongoPersist(ctx context.Context, logger runtime.Logger, dispatcher *EventDispatcher, journal *MatchDataJournal) error {
	if dispatcher.mongo == nil {
		return fmt.Errorf("neither Redis nor MongoDB available for persistence")
	}

	collection := dispatcher.mongo.Database(matchDataDatabaseName).Collection(matchDataCollectionName)

	if _, err := collection.InsertOne(ctx, journal); err != nil {
		return fmt.Errorf("failed to insert journal directly to MongoDB: %w", err)
	}

	logger.Debug("Successfully persisted journal directly to MongoDB")
	return nil
}
