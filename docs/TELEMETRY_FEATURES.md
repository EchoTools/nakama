# Redis Streams Event Journaling, MongoDB Summarization, and StreamModeLobbySessionTelemetry

This document describes the new rtapi and event journaling features implemented for the Nakama EVR module.

## Overview

The implementation adds three main features:

1. **Redis Streams Event Journaling** - Durable event storage using Redis Streams
2. **MongoDB Match Summarization** - Long-term match data storage and analytics
3. **StreamModeLobbySessionTelemetry** - Real-time lobby rtapi subscription system

## Configuration

### Environment Variables

- `MONGODB_URI` - MongoDB connection string for match summaries (optional)
- `JOURNAL_REDIS_URI` - Redis connection for event journaling (optional, falls back to VRML_REDIS_URI)
- `VRML_REDIS_URI` - Existing Redis connection that can be reused for journaling

### Example Configuration

```bash
# MongoDB for match summaries
MONGODB_URI=mongodb://localhost:27017/nakama

# Redis for event journaling
JOURNAL_REDIS_URI=redis://localhost:6379/1

# Or reuse existing Redis
VRML_REDIS_URI=redis://localhost:6379/0
```

## Features

### 1. Redis Streams Event Journaling

Provides durable storage for events using Redis Streams with consumer group support.

#### Event Types

- `events:player_actions` - Player game actions
- `events:purchases` - Purchase transactions
- `events:rtapi` - Lobby and match rtapi data

#### Usage

```go
// Journal a player action
err := eventJournal.JournalPlayerAction(ctx, userID, sessionID, matchID, "goal_scored", map[string]interface{}{
    "score": 10,
    "team": "blue",
    "timestamp": time.Now(),
})

// Journal rtapi data
err := eventJournal.JournalTelemetry(ctx, userID, sessionID, lobbyID, telemetryData)

// Journal a purchase
err := eventJournal.JournalPurchase(ctx, userID, sessionID, purchaseData)
```

#### Consumer Groups

```go
consumer := NewEventConsumer(redisClient, logger, "analytics-group", "consumer-1")

// Process events from a stream
err := consumer.ConsumeEvents(ctx, "events:player_actions", 10, func(messageID string, data map[string]interface{}) error {
    // Process the event
    return nil
})
```

### 2. MongoDB Match Summarization

Stores complete match summaries in MongoDB for analytics and reporting.

#### MatchSummary Structure

```go
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
```

#### Usage

```go
// Store a match summary
summary := &MatchSummary{
    MatchID:         "match-123",
    Players:         []string{"player1", "player2"},
    DurationSeconds: 300,
    // ... other fields
}

err := matchSummaryStore.Store(ctx, summary)

// Retrieve a match summary
summary, err := matchSummaryStore.Get(ctx, "match-123")
```

### 3. StreamModeLobbySessionTelemetry API

Enables sessions to subscribe to real-time lobby rtapi streams.

#### Stream Mode

- **Constant**: `StreamModeLobbySessionTelemetry = 0x16`
- **Purpose**: Dedicated stream mode for lobby session rtapi
- **Usage**: Sessions can subscribe to receive rtapi data from specific lobbies

#### API Endpoints

##### Subscribe to Lobby Telemetry

```http
POST /rtapi/subscribe
Headers:
  X-Session-ID: session-uuid
  X-User-ID: user-uuid
  Content-Type: application/json

{
  "lobby_id": "lobby-uuid"
}
```

Response:
```json
{
  "success": true,
  "message": "Successfully subscribed to lobby rtapi",
  "lobby_id": "lobby-uuid",
  "session_id": "session-uuid"
}
```

##### Unsubscribe from Lobby Telemetry

```http
POST /rtapi/unsubscribe
Headers:
  X-Session-ID: session-uuid
  X-User-ID: user-uuid
  Content-Type: application/json

{
  "lobby_id": "lobby-uuid"
}
```

##### Get Active Subscriptions

```http
GET /rtapi/subscriptions?lobby_id=lobby-uuid
```

Response:
```json
{
  "lobby_id": "lobby-uuid",
  "subscriptions": [
    {
      "session_id": "session-uuid-1",
      "user_id": "user-uuid-1",
      "lobby_id": "lobby-uuid",
      "active": true
    }
  ],
  "count": 1
}
```

#### Telemetry Broadcasting

```go
// Broadcast rtapi to all subscribed sessions
telemetryData := map[string]interface{}{
    "event_type": "player_movement",
    "player_id": "player-123",
    "position": map[string]float64{"x": 10.5, "y": 20.3, "z": 5.1},
}

err := telemetryManager.BroadcastTelemetry(ctx, lobbyID, telemetryData)
```

## Integration with Existing EVR Systems

### Example Match Handler Integration

```go
func (m *EvrMatch) MatchSignal(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state interface{}, data string) (interface{}, string) {
    // ... existing match logic
    
    // Handle rtapi events
    if telemetryIntegration != nil {
        switch opcode {
        case OpCodeEVRPacketData:
            if isTelemetryPacket(packet) {
                telemetryData := extractTelemetryData(packet)
                telemetryIntegration.HandleSNSTelemetryEvent(ctx, sessionID, userID, lobbyID, telemetryData)
            }
        }
    }
    
    return state, ""
}
```

### Example Match End Integration

```go
func (m *EvrMatch) MatchTerminate(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state interface{}, graceSeconds int) interface{} {
    matchState := state.(*EvrMatchState)
    
    // Create match summary
    if telemetryIntegration != nil {
        telemetryIntegration.HandleMatchEnd(ctx, matchState)
    }
    
    return state
}
```

## Data Flow

1. **Event Generation**: Game events are generated during gameplay
2. **Journaling**: Events are stored in Redis Streams for durability
3. **Real-time Distribution**: Telemetry is broadcast to subscribed sessions
4. **Summarization**: Match end events trigger MongoDB summary creation
5. **Analytics**: Consumer groups process events for analytics and reporting

## Error Handling and Graceful Degradation

- All systems gracefully handle missing dependencies
- Redis unavailable: Journaling is skipped, logs debug message
- MongoDB unavailable: Match summaries are skipped, logs warning
- Network issues: Operations retry with exponential backoff

## Monitoring and Observability

- All operations are logged with appropriate levels
- Metrics can be added for monitoring event throughput
- Consumer group lag can be monitored for processing delays

## Security Considerations

- API endpoints require session authentication (X-Session-ID, X-User-ID headers)
- Redis Streams support ACLs for access control
- MongoDB connection should use authentication in production

## Performance Considerations

- Redis Streams are optimized for high throughput
- Consumer groups provide automatic load balancing
- MongoDB queries should use appropriate indexes
- Telemetry broadcasting is asynchronous to avoid blocking game logic