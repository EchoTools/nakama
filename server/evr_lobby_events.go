package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/echotools/nevr-common/v4/gen/go/rtapi"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	sessionEventDatabaseName   = "nakama"
	sessionEventCollectionName = "session_events"
)

// ctxMongoClientKey is the context key for the MongoDB client
type ctxMongoClientKey struct{}

// SessionEvent represents a simple session event object
type SessionEvent struct {
	MatchID MatchID                       `bson:"match_id" json:"match_id"`
	UserID  string                        `bson:"user_id,omitempty" json:"user_id,omitempty"`
	Data    *rtapi.LobbySessionStateFrame `bson:"data,omitempty" json:"data,omitempty"`
}

// StoreSessionEvent stores a session event to MongoDB
func StoreSessionEvent(ctx context.Context, mongoClient *mongo.Client, event *SessionEvent) error {
	if mongoClient == nil {
		return fmt.Errorf("mongo client is nil")
	}

	if !event.MatchID.IsValid() {
		return fmt.Errorf("match_id is invalid")
	}

	collection := mongoClient.Database(sessionEventDatabaseName).Collection(sessionEventCollectionName)

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	_, err := collection.InsertOne(ctx, event)
	if err != nil {
		return fmt.Errorf("failed to insert session event: %w", err)
	}

	return nil
}

// RetrieveSessionEventsByMatchID retrieves all session events for a given match ID from MongoDB
func RetrieveSessionEventsByMatchID(ctx context.Context, mongoClient *mongo.Client, matchID string) ([]*SessionEvent, error) {
	if mongoClient == nil {
		return nil, fmt.Errorf("mongo client is nil")
	}

	if matchID == "" {
		return nil, fmt.Errorf("match_id is required")
	}

	collection := mongoClient.Database(sessionEventDatabaseName).Collection(sessionEventCollectionName)

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Create filter for match_id
	filter := bson.M{"match_id": matchID}

	// Sort by timestamp ascending
	opts := options.Find().SetSort(bson.D{{Key: "timestamp", Value: 1}})

	cursor, err := collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to query session events: %w", err)
	}
	defer cursor.Close(ctx)

	var events []*SessionEvent
	if err := cursor.All(ctx, &events); err != nil {
		return nil, fmt.Errorf("failed to decode session events: %w", err)
	}

	return events, nil
}

// GetSessionEventsRPC is a Nakama RPC endpoint that retrieves session events by match_id
// Expected query parameter: match_id
func GetSessionEventsRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	// Parse the payload as JSON to extract match_id
	var request struct {
		MatchUUID string `json:"match_id"`
	}

	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return "", fmt.Errorf("invalid request payload: %w", err)
	}

	node := ctx.Value(runtime.RUNTIME_CTX_NODE).(string)

	matchID := MatchID{
		UUID: uuid.FromStringOrNil(request.MatchUUID),
		Node: node,
	}
	if !matchID.IsValid() {
		return "", fmt.Errorf("match_id is required in request payload")
	}

	// Get MongoDB client from the runtime module
	// Note: You'll need to pass the mongo client to this RPC function
	// For now, we'll assume it's available through the runtime context or you'll need to modify
	// the RPC registration to include it
	mongoClient, ok := ctx.Value(ctxMongoClientKey{}).(*mongo.Client)
	if !ok || mongoClient == nil {
		return "", fmt.Errorf("mongodb client not available in context")
	}

	// Retrieve events from MongoDB
	events, err := RetrieveSessionEventsByMatchID(ctx, mongoClient, request.MatchUUID)
	if err != nil {
		logger.Error("Failed to retrieve session events", "error", err, "match_id", request.MatchUUID)
		return "", fmt.Errorf("failed to retrieve session events: %w", err)
	}

	// Marshal response
	response := map[string]interface{}{
		"match_id": request.MatchUUID,
		"count":    len(events),
		"events":   events,
	}

	responseJSON, err := json.Marshal(response)
	if err != nil {
		return "", fmt.Errorf("failed to marshal response: %w", err)
	}

	return string(responseJSON), nil
}

// StoreSessionEventRPC is a Nakama RPC endpoint that stores a session event to MongoDB
// Expected payload: SessionEvent JSON object
func StoreSessionEventRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	// Parse the payload as SessionEvent

	msg := &rtapi.LobbySessionStateFrame{}

	if err := protojson.Unmarshal([]byte(payload), msg); err != nil {
		return "", fmt.Errorf("invalid request payload: %w", err)
	}

	node := ctx.Value(runtime.RUNTIME_CTX_NODE).(string)

	matchID := MatchID{
		UUID: uuid.FromStringOrNil(msg.GetSession().GetSessionId()),
		Node: node,
	}
	if !matchID.IsValid() {
		return "", fmt.Errorf("match_id is required in request payload")
	}

	userID := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	event := &SessionEvent{
		MatchID: matchID,
		UserID:  userID,
		Data:    msg,
	}

	// Get MongoDB client from the runtime module
	mongoClient, ok := ctx.Value(ctxMongoClientKey{}).(*mongo.Client)
	if !ok || mongoClient == nil {
		return "", fmt.Errorf("mongodb client not available in context")
	}

	// Store the event to MongoDB
	if err := StoreSessionEvent(ctx, mongoClient, event); err != nil {
		logger.Error("Failed to store session event", "error", err, "match_id", event.MatchID)
		return "", fmt.Errorf("failed to store session event: %w", err)
	}

	// Return success response
	response := map[string]interface{}{
		"success":  true,
		"match_id": event.MatchID,
	}

	responseJSON, err := json.Marshal(response)
	if err != nil {
		return "", fmt.Errorf("failed to marshal response: %w", err)
	}

	logger.Debug("Stored session event", "match_id", event.MatchID)
	return string(responseJSON), nil
}

// RegisterSessionEventRPCs registers the session event RPC endpoints
func RegisterSessionEventRPCs(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer, mongoClient *mongo.Client) error {
	// Create a wrapper function that injects the mongo client into the context
	getSessionEventsWrapper := func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
		// Inject mongo client into context
		ctx = context.WithValue(ctx, ctxMongoClientKey{}, mongoClient)
		return GetSessionEventsRPC(ctx, logger, db, nk, payload)
	}

	storeSessionEventWrapper := func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
		// Inject mongo client into context
		ctx = context.WithValue(ctx, ctxMongoClientKey{}, mongoClient)
		return StoreSessionEventRPC(ctx, logger, db, nk, payload)
	}

	if err := initializer.RegisterRpc("session_events/get", getSessionEventsWrapper); err != nil {
		return fmt.Errorf("failed to register session_events/get RPC: %w", err)
	}

	if err := initializer.RegisterRpc("session_events/store", storeSessionEventWrapper); err != nil {
		return fmt.Errorf("failed to register session_events/store RPC: %w", err)
	}

	logger.Info("Registered session event RPC endpoints")
	return nil
}
