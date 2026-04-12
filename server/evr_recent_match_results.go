package server

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// PlayerMatchResult records a single match outcome for a player.
type PlayerMatchResult struct {
	UserID    string    `bson:"user_id" json:"user_id"`
	MatchID   string    `bson:"match_id" json:"match_id"`
	Mode      string    `bson:"mode" json:"mode"`
	Won       bool      `bson:"won" json:"won"`
	CreatedAt time.Time `bson:"created_at" json:"created_at"`
}

// StorePlayerMatchResult inserts a match result document into MongoDB.
func StorePlayerMatchResult(ctx context.Context, mongoClient *mongo.Client, result *PlayerMatchResult) error {
	collection := mongoClient.Database(matchDataDatabaseName).Collection(playerMatchResultsCollectionName)
	_, err := collection.InsertOne(ctx, result)
	if err != nil {
		return fmt.Errorf("failed to insert player match result: %w", err)
	}
	return nil
}

// GetRecentWinRate queries the last N match results for a user+mode and computes the win rate.
// Returns (winRate, gamesCount, err). winRate is -1 if no games found.
func GetRecentWinRate(ctx context.Context, mongoClient *mongo.Client, userID string, mode string, limit int) (float64, int, error) {
	collection := mongoClient.Database(matchDataDatabaseName).Collection(playerMatchResultsCollectionName)

	filter := bson.M{"user_id": userID, "mode": mode}
	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetLimit(int64(limit)).
		SetProjection(bson.M{"won": 1})

	cursor, err := collection.Find(ctx, filter, opts)
	if err != nil {
		return -1, 0, fmt.Errorf("failed to query player match results: %w", err)
	}
	defer cursor.Close(ctx)

	var wins, total int
	for cursor.Next(ctx) {
		var result struct {
			Won bool `bson:"won"`
		}
		if err := cursor.Decode(&result); err != nil {
			continue
		}
		total++
		if result.Won {
			wins++
		}
	}

	if err := cursor.Err(); err != nil {
		return -1, 0, fmt.Errorf("cursor error reading player match results: %w", err)
	}

	if total == 0 {
		return -1, 0, nil
	}

	winRate := float64(wins) / float64(total) * 100
	return winRate, total, nil
}
