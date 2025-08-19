package server

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// MatchSummary represents a summarized match journal for MongoDB
type MatchSummary struct {
	MatchID           string                 `bson:"match_id"`
	Players           []string               `bson:"players"`
	PerRoundStats     map[string][]RoundStat `bson:"per_round_stats"`
	DurationSeconds   int                    `bson:"duration_seconds"`
	MinPing           int                    `bson:"min_ping"`
	MaxPing           int                    `bson:"max_ping"`
	AvgPing           float64                `bson:"avg_ping"`
	FinalRoundScores  map[string]int         `bson:"final_round_scores"`
	EvrMatchPresences interface{}            `bson:"evr_match_presences"`
	MatchLabel        string                 `bson:"match_label"`
	CreatedAt         time.Time              `bson:"created_at"`
	UpdatedAt         time.Time              `bson:"updated_at"`
}

// RoundStat represents statistics for a single round for a player
type RoundStat struct {
	Round int `bson:"round"`
	Score int `bson:"score"`
	// Add additional stat fields as needed
}

// MatchSummaryStore provides methods for storing and retrieving match summaries
type MatchSummaryStore struct {
	client     *mongo.Client
	database   string
	collection string
}

// NewMatchSummaryStore creates a new MatchSummaryStore instance
func NewMatchSummaryStore(client *mongo.Client, database, collection string) *MatchSummaryStore {
	return &MatchSummaryStore{
		client:     client,
		database:   database,
		collection: collection,
	}
}

// Store saves a match summary to MongoDB
func (ms *MatchSummaryStore) Store(ctx context.Context, summary *MatchSummary) error {
	collection := ms.client.Database(ms.database).Collection(ms.collection)
	summary.UpdatedAt = time.Now()
	if summary.CreatedAt.IsZero() {
		summary.CreatedAt = summary.UpdatedAt
	}
	
	_, err := collection.InsertOne(ctx, summary)
	return err
}

// Update updates an existing match summary
func (ms *MatchSummaryStore) Update(ctx context.Context, matchID string, update bson.M) error {
	collection := ms.client.Database(ms.database).Collection(ms.collection)
	filter := bson.M{"match_id": matchID}
	update["updated_at"] = time.Now()
	
	_, err := collection.UpdateOne(ctx, filter, bson.M{"$set": update})
	return err
}

// Get retrieves a match summary by match ID
func (ms *MatchSummaryStore) Get(ctx context.Context, matchID string) (*MatchSummary, error) {
	collection := ms.client.Database(ms.database).Collection(ms.collection)
	filter := bson.M{"match_id": matchID}
	
	var summary MatchSummary
	err := collection.FindOne(ctx, filter).Decode(&summary)
	if err != nil {
		return nil, err
	}
	
	return &summary, nil
}