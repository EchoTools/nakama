package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-redis/redis"
	"github.com/heroiclabs/nakama-common/runtime"
)

// EventJournal handles durable event journaling using Redis Streams
type EventJournal struct {
	redisClient *redis.Client
	logger      runtime.Logger
}

// JournalEvent represents an event to be journaled
type JournalEvent struct {
	Type      string                 `json:"type"`
	Timestamp time.Time              `json:"timestamp"`
	UserID    string                 `json:"user_id,omitempty"`
	SessionID string                 `json:"session_id,omitempty"`
	MatchID   string                 `json:"match_id,omitempty"`
	Data      map[string]interface{} `json:"data"`
}

// NewEventJournal creates a new EventJournal instance
func NewEventJournal(redisClient *redis.Client, logger runtime.Logger) *EventJournal {
	return &EventJournal{
		redisClient: redisClient,
		logger:      logger,
	}
}

// Journal adds an event to a Redis Stream for durable storage
func (ej *EventJournal) Journal(ctx context.Context, eventType string, event *JournalEvent) error {
	if ej.redisClient == nil {
		ej.logger.Debug("Redis client not available, skipping journaling: event_type=%s", eventType)
		return nil
	}

	streamKey := fmt.Sprintf("events:%s", eventType)

	// Serialize the event
	eventData, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	// Add to Redis Stream
	args := &redis.XAddArgs{
		Stream: streamKey,
		Values: map[string]interface{}{
			"event_type": eventType,
			"data":       string(eventData),
			"timestamp":  event.Timestamp.Unix(),
		},
	}

	_, err = ej.redisClient.XAdd(args).Result()
	if err != nil {
		ej.logger.Error("Failed to add event to Redis Stream: stream=%s, event_type=%s, error=%v", streamKey, eventType, err)
		return fmt.Errorf("failed to add event to Redis Stream: %w", err)
	}

	ej.logger.Debug("Event journaled successfully: stream=%s, event_type=%s", streamKey, eventType)

	return nil
}

// JournalPlayerAction journals a player action event
func (ej *EventJournal) JournalPlayerAction(ctx context.Context, userID, sessionID, matchID string, action string, data map[string]interface{}) error {
	event := &JournalEvent{
		Type:      "player_action",
		Timestamp: time.Now(),
		UserID:    userID,
		SessionID: sessionID,
		MatchID:   matchID,
		Data: map[string]interface{}{
			"action":  action,
			"details": data,
		},
	}

	return ej.Journal(ctx, "player_actions", event)
}

// JournalPurchase journals a purchase event
func (ej *EventJournal) JournalPurchase(ctx context.Context, userID, sessionID string, purchaseData map[string]interface{}) error {
	event := &JournalEvent{
		Type:      "purchase",
		Timestamp: time.Now(),
		UserID:    userID,
		SessionID: sessionID,
		Data:      purchaseData,
	}

	return ej.Journal(ctx, "purchases", event)
}

// JournalTelemetry journals telemetry data
func (ej *EventJournal) JournalTelemetry(ctx context.Context, userID, sessionID, lobbyID string, telemetryData map[string]interface{}) error {
	event := &JournalEvent{
		Type:      "telemetry",
		Timestamp: time.Now(),
		UserID:    userID,
		SessionID: sessionID,
		Data: map[string]interface{}{
			"lobby_id":  lobbyID,
			"telemetry": telemetryData,
		},
	}

	return ej.Journal(ctx, "telemetry", event)
}

// EventConsumer handles consuming events from Redis Streams
type EventConsumer struct {
	redisClient  *redis.Client
	logger       runtime.Logger
	groupName    string
	consumerName string
}

// NewEventConsumer creates a new EventConsumer instance
func NewEventConsumer(redisClient *redis.Client, logger runtime.Logger, groupName, consumerName string) *EventConsumer {
	return &EventConsumer{
		redisClient:  redisClient,
		logger:       logger,
		groupName:    groupName,
		consumerName: consumerName,
	}
}

// CreateConsumerGroup creates a consumer group for a stream
func (ec *EventConsumer) CreateConsumerGroup(ctx context.Context, streamKey string) error {
	// Try to create the consumer group (ignore error if it already exists)
	err := ec.redisClient.XGroupCreate(streamKey, ec.groupName, "0").Err()
	if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
		return fmt.Errorf("failed to create consumer group: %w", err)
	}
	return nil
}

// ConsumeEvents reads events from a Redis Stream using consumer groups
func (ec *EventConsumer) ConsumeEvents(ctx context.Context, streamKey string, batchSize int64, processFunc func(string, map[string]interface{}) error) error {
	// Ensure consumer group exists
	if err := ec.CreateConsumerGroup(ctx, streamKey); err != nil {
		return err
	}

	args := &redis.XReadGroupArgs{
		Group:    ec.groupName,
		Consumer: ec.consumerName,
		Streams:  []string{streamKey, ">"},
		Count:    batchSize,
		Block:    time.Second * 5,
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			result, err := ec.redisClient.XReadGroup(args).Result()
			if err == redis.Nil {
				// No new messages, continue
				continue
			}
			if err != nil {
				ec.logger.Error("Error reading from Redis Stream: stream=%s, error=%v", streamKey, err)
				time.Sleep(time.Second)
				continue
			}

			for _, stream := range result {
				for _, message := range stream.Messages {
					// Process the message
					if err := processFunc(message.ID, message.Values); err != nil {
						ec.logger.Error("Error processing message: stream=%s, message_id=%s, error=%v", streamKey, message.ID, err)
						continue
					}

					// Acknowledge the message
					if err := ec.redisClient.XAck(streamKey, ec.groupName, message.ID).Err(); err != nil {
						ec.logger.Error("Error acknowledging message: stream=%s, message_id=%s, error=%v", streamKey, message.ID, err)
					}
				}
			}
		}
	}
}
