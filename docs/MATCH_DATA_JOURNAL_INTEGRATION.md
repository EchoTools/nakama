# MatchDataJournal EventDispatcher Integration

## Overview

The MatchDataJournal has been refactored to integrate with the EventDispatcher system as a distinct event type. This provides better scalability, reliability, and separation of concerns for match data persistence.

## Architecture

### Event-Driven Processing
- `EventMatchDataJournal` is now a first-class event type in the EventDispatcher system
- Match data entries are processed asynchronously through the event queue
- Redis queue provides reliability buffer before MongoDB persistence
- Automatic journal management with configurable thresholds

### Data Flow

1. **Match Events**: Game events call `MatchDataEvent()` with match data
2. **Event Creation**: Creates `EventMatchDataJournal` with new entry
3. **Journal Management**: Entries are accumulated in memory journals
4. **Queue Processing**: Large or old journals are queued to Redis
5. **Persistence**: Redis queue is processed periodically to persist to MongoDB
6. **Indexing**: MongoDB collections have optimized indexes for match ID queries

### Redis Queue Integration

The system uses Redis as a reliability buffer:
- **Queue Key**: `match_data_journal_queue`
- **Trigger Conditions**: 
  - Journal has 100+ events
  - Journal is older than 5 minutes
- **Fallback**: Direct MongoDB insertion if Redis unavailable
- **Batch Processing**: Up to 10 items processed per 30-second cycle

### MongoDB Persistence

#### Collection Structure
- **Database**: `nevr`
- **Collection**: `match_data`
- **Document Format**:
  ```json
  {
    "match_id": "string",
    "events": [
      {
        "created_at": "timestamp",
        "data": "any"
      }
    ],
    "created_at": "timestamp",
    "updated_at": "timestamp"
  }
  ```

#### Indexes
- `match_id_1`: Primary index for match ID queries
- `match_id_created_at_-1`: Compound index for temporal queries
- `updated_at_-1`: Index for cleanup operations
- `created_at_ttl`: TTL index for automatic cleanup (30 days)

## Remote Log Integration

Remote log events with valid match SessionIDs are automatically captured:
- `EventRemoteLogSet` processes remote logs
- Logs with valid session UUIDs are converted to `MatchDataRemoteLogSet`
- These are sent through the same `MatchDataEvent` pipeline
- Ensures all match-related data is captured and stored

## Configuration

### EventDispatcher Initialization
```go
eventDispatch, err := NewEventDispatch(
    ctx, logger, db, nk, initializer, 
    mongoClient,  // MongoDB client (optional)
    redisClient,  // Redis client (optional)
    dg, statisticsQueue, vrmlScanQueue
)
```

### MongoDB Indexing
Call `EnsureMatchDataIndexes(ctx, mongoClient)` during initialization to create required indexes.

## Querying Match Data

### By Match ID
```javascript
// MongoDB query
db.match_data.find({"match_id": "your-match-id"})
```

### By Time Range
```javascript
// Recent matches for a specific match ID
db.match_data.find({
  "match_id": "your-match-id",
  "created_at": {"$gte": ISODate("2024-01-01")}
}).sort({"created_at": -1})
```

## Error Handling

- **Redis Unavailable**: Falls back to direct MongoDB insertion
- **MongoDB Unavailable**: Logs error, data may be lost
- **Queue Processing Errors**: Failed items are returned to queue for retry
- **Malformed Data**: Logged and skipped, doesn't block other processing

## Performance Considerations

- **Memory Usage**: Journals are kept in memory until threshold reached
- **Batch Processing**: Redis queue processed in batches of 10
- **Background Operations**: All persistence is asynchronous
- **Index Performance**: Queries by match_id are O(log n) with btree index
- **TTL Cleanup**: Automatic cleanup prevents unbounded growth

## Migration Notes

- **Backward Compatibility**: Existing match data event handling continues to work
- **Gradual Migration**: Old `handleMatchEvent` removed, new system handles all events
- **No Breaking Changes**: Public API (`MatchDataEvent`) remains unchanged