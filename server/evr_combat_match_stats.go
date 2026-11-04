package server

import (
	"context"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// PlayerMatchStats is one player's end-of-match stats as the game sent them in
// POST_MATCH_MATCH_STATS. It is kept on the match's own record in
// match_summaries, under player_stats.<xpid>.
type PlayerMatchStats struct {
	XPID         string             `json:"xpid" bson:"xpid"`
	UserID       string             `json:"user_id,omitempty" bson:"user_id,omitempty"` // the player, when the match label names them
	SenderUserID string             `json:"sender_user_id" bson:"sender_user_id"`       // who sent the log: the player, or the game server's operator
	MatchType    string             `json:"match_type" bson:"match_type"`
	Stats        map[string]float64 `json:"stats" bson:"stats"` // the game's key names; a key the game did not send, or sent as 0, is absent
	ReceivedAt   time.Time          `json:"received_at" bson:"received_at"`
}

// playerMatchStatsStore keeps a player's end-of-match stats on the match's
// record. Store must not block the caller: it runs on the event dispatcher.
type playerMatchStatsStore interface {
	Store(logger runtime.Logger, matchID string, rec PlayerMatchStats)
}

// mongoPlayerMatchStatsStore writes into match_summaries, the collection
// EventMatchSummary upserts at the end of the match. Both upsert the same _id
// and $set disjoint fields, so either may arrive first.
type mongoPlayerMatchStatsStore struct {
	client *mongo.Client
}

func (m mongoPlayerMatchStatsStore) Store(logger runtime.Logger, matchID string, rec PlayerMatchStats) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		collection := m.client.Database(matchDataDatabaseName).Collection(matchSummaryCollectionName)
		if _, err := collection.UpdateOne(ctx, bson.M{"_id": matchID}, playerMatchStatsUpdate(rec), options.Update().SetUpsert(true)); err != nil {
			logger.WithFields(map[string]any{
				"error":       err,
				"mid":         matchID,
				"player_xpid": rec.XPID,
			}).Warn("Failed to store combat match stats on the match summary")
		}
	}()
}

// playerMatchStatsUpdate sets one player's entry and nothing else, so players
// reporting the same match do not overwrite each other and a repeated report
// replaces its own entry rather than adding one.
func playerMatchStatsUpdate(rec PlayerMatchStats) bson.M {
	return bson.M{"$set": bson.M{"player_stats." + rec.XPID: rec}}
}
