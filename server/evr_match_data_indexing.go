package server

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// EnsureMatchDataIndexes creates necessary indexes for the match data collection
func EnsureMatchDataIndexes(ctx context.Context, mongoClient *mongo.Client) error {
	if mongoClient == nil {
		return nil // Skip if MongoDB is not configured
	}

	collection := mongoClient.Database(matchDataDatabaseName).Collection(matchDataCollectionName)

	// Create index on match_id for efficient queries
	matchIDIndex := mongo.IndexModel{
		Keys:    bson.D{{Key: "match_id", Value: 1}},
		Options: options.Index().SetBackground(true).SetName("match_id_1"),
	}

	// Create compound index on match_id and created_at for temporal queries
	matchIDCreatedAtIndex := mongo.IndexModel{
		Keys: bson.D{
			{Key: "match_id", Value: 1},
			{Key: "created_at", Value: -1},
		},
		Options: options.Index().SetBackground(true).SetName("match_id_created_at_-1"),
	}

	// Create index on updated_at for efficient cleanup queries
	updatedAtIndex := mongo.IndexModel{
		Keys:    bson.D{{Key: "updated_at", Value: -1}},
		Options: options.Index().SetBackground(true).SetName("updated_at_-1"),
	}

	// Create TTL index for automatic cleanup of old data (optional, 30 days retention)
	ttlIndex := mongo.IndexModel{
		Keys: bson.D{{Key: "created_at", Value: 1}},
		Options: options.Index().
			SetBackground(true).
			SetName("created_at_ttl").
			SetExpireAfterSeconds(int32(30 * 24 * time.Hour.Seconds())), // 30 days
	}

	indexes := []mongo.IndexModel{
		matchIDIndex,
		matchIDCreatedAtIndex,
		updatedAtIndex,
		ttlIndex,
	}

	_, err := collection.Indexes().CreateMany(ctx, indexes)
	return err
}
