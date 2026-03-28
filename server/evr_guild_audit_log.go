package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	guildAuditLogDatabaseName   = "nevr"
	guildAuditLogCollectionName = "guild_audit_log"
)

// GuildAuditEntry represents a single audit log entry for a guild.
type GuildAuditEntry struct {
	ID             string `json:"id" bson:"_id"`
	GroupID        string `json:"group_id" bson:"group_id"`
	Timestamp      string `json:"timestamp" bson:"timestamp"`
	ActorID        string `json:"actor_id,omitempty" bson:"actor_id,omitempty"`
	ActorUsername  string `json:"actor_username,omitempty" bson:"actor_username,omitempty"`
	Action         string `json:"action" bson:"action"`
	TargetID       string `json:"target_id,omitempty" bson:"target_id,omitempty"`
	TargetUsername string `json:"target_username,omitempty" bson:"target_username,omitempty"`
	Details        string `json:"details,omitempty" bson:"details,omitempty"`
}

// GuildAuditLogWrite persists an audit entry to MongoDB.
func GuildAuditLogWrite(ctx context.Context, mongoClient *mongo.Client, entry *GuildAuditEntry) error {
	if mongoClient == nil {
		return nil // MongoDB not configured; silently skip.
	}
	if entry.ID == "" {
		entry.ID = uuid.Must(uuid.NewV4()).String()
	}
	if entry.Timestamp == "" {
		entry.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}

	collection := mongoClient.Database(guildAuditLogDatabaseName).Collection(guildAuditLogCollectionName)
	_, err := collection.InsertOne(ctx, entry)
	return err
}

// EnsureGuildAuditLogIndexes creates indexes on the guild_audit_log collection.
func EnsureGuildAuditLogIndexes(ctx context.Context, mongoClient *mongo.Client) error {
	if mongoClient == nil {
		return nil
	}
	collection := mongoClient.Database(guildAuditLogDatabaseName).Collection(guildAuditLogCollectionName)
	_, err := collection.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys: bson.D{{Key: "group_id", Value: 1}, {Key: "timestamp", Value: -1}},
		},
	})
	return err
}

// GuildAuditLogListRequest is the request payload for the audit log RPC.
type GuildAuditLogListRequest struct {
	GroupID string `json:"group_id"`
	Limit   int    `json:"limit"`
	Cursor  string `json:"cursor"`
}

// GuildAuditLogListResponse is the response payload for the audit log RPC.
type GuildAuditLogListResponse struct {
	Entries []*GuildAuditEntry `json:"entries"`
	Cursor  string             `json:"cursor,omitempty"`
}

// GuildGroupAuditLogRPC lists audit log entries for a guild group.
func GuildGroupAuditLogRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("unauthenticated", 16)
	}

	var req GuildAuditLogListRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", runtime.NewError("invalid request payload", 3)
	}
	if req.GroupID == "" {
		return "", runtime.NewError("group_id is required", 3)
	}

	// Verify user has permission to view audit logs (owner, enforcer, auditor, or global op).
	gg, err := GuildGroupLoad(ctx, nk, req.GroupID)
	if err != nil {
		return "", runtime.NewError("guild group not found", 5)
	}

	if !gg.IsOwner(userID) {
		isGlobalOp, checkErr := isGlobalOperator(ctx, nk, userID)
		if checkErr != nil || !isGlobalOp {
			return "", runtime.NewError("permission denied: only guild owner can view the guild's audit logs", 7)
		}
	}

	mongoClient := globalMongoClient.Load()
	if mongoClient == nil {
		return "", runtime.NewError("audit log storage unavailable", 13)
	}

	limit := req.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	collection := mongoClient.Database(guildAuditLogDatabaseName).Collection(guildAuditLogCollectionName)

	filter := bson.M{"group_id": req.GroupID}

	// Use cursor as a timestamp-based pagination token (the timestamp of the last entry seen).
	if req.Cursor != "" {
		filter["timestamp"] = bson.M{"$lt": req.Cursor}
	}

	findOpts := options.Find().
		SetSort(bson.D{{Key: "timestamp", Value: -1}}).
		SetLimit(int64(limit + 1)) // fetch one extra to detect if there's a next page

	cur, err := collection.Find(ctx, filter, findOpts)
	if err != nil {
		return "", runtime.NewError("failed to query audit log", 13)
	}
	defer cur.Close(ctx)

	entries := make([]*GuildAuditEntry, 0, limit)
	for cur.Next(ctx) {
		var entry GuildAuditEntry
		if err := cur.Decode(&entry); err != nil {
			continue
		}
		entries = append(entries, &entry)
	}

	var nextCursor string
	if len(entries) > limit {
		// There are more results; set cursor to the timestamp of the last returned entry.
		nextCursor = entries[limit-1].Timestamp
		entries = entries[:limit]
	}

	resp := GuildAuditLogListResponse{
		Entries: entries,
		Cursor:  nextCursor,
	}

	b, err := json.Marshal(resp)
	if err != nil {
		return "", runtime.NewError("failed to marshal response", 13)
	}
	return string(b), nil
}
