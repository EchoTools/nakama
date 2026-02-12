# Remote Log Storage Refactoring

## Overview

This document describes the refactoring of remote log storage from database-backed storage objects to file-based storage, addressing backup isolation and performance requirements.

## Problem Statement

Previously, remote logs were stored as regular storage objects in the PostgreSQL `storage` table using the `RemoteLogs` collection. This approach had several issues:

1. **Database Backup Bloat**: Remote logs are high-volume debugging data that made database backups unnecessarily large
2. **Backup Pollution**: Rarely-needed debugging data was mixed with primary application data in backups
3. **Performance Concerns**: High-volume writes to the database could impact performance

## Solution

The solution implements a file-based storage system that:

### 1. Storage Isolation
- Remote logs are now written to JSON Lines (`.jsonl`) files in a dedicated directory
- By default, logs are stored in `<data_dir>/remotelogs/`
- This directory can be excluded from database backups, significantly reducing backup size
- The file-based approach naturally separates operational data from debugging logs

### 2. Performance Optimization
- **Batching**: Logs are batched in memory and written periodically
- **Async Writes**: A background goroutine handles all disk I/O asynchronously
- **Efficient Format**: JSON Lines format enables efficient append operations
- **Flush Control**: Logs are flushed to disk every 10 seconds or on session close

### 3. Operational Features
- **Automatic Rotation**: Log files automatically rotate at 10MB or daily
- **Retention Management**: Old log files (>7 days) are automatically cleaned up
- **Debugging Access**: Read API available for retrieving logs by user ID and timestamp
- **Zero Downtime**: Background routines handle rotation and cleanup without interrupting service

## Implementation Details

### New Components

#### `RemoteLogFileWriter` (`evr_remotelog_file_writer.go`)
Main component responsible for writing logs to disk:
- Manages file rotation based on size and time
- Handles automatic cleanup of old files
- Provides read access for debugging
- Thread-safe operations with mutex protection

#### Updated `UserLogJournalRegistry` (`evr_remotelog_journal.go`)
Updated to use file-based storage instead of database storage:
- Removed dependency on `runtime.NakamaModule` for storage operations
- Uses `RemoteLogFileWriter` for disk operations
- Simplified data structure (removed timestamp maps, direct string arrays)
- Maintains same batching and session tracking behavior

### Configuration

The system uses the existing `data_dir` configuration. Remote logs are stored in:
```
<data_dir>/remotelogs/remotelog-YYYY-MM-DDTHH-MM-SS.jsonl
```

### Constants
```go
RemoteLogDirectory      = "remotelogs"          // Subdirectory name
RemoteLogMaxFileSize    = 10 * 1024 * 1024     // 10MB per file
RemoteLogRetentionDays  = 7                     // Keep logs for 7 days
RemoteLogFlushInterval  = 10 * time.Second      // Flush every 10 seconds
```

### File Format

Logs are stored in JSON Lines format (one JSON object per line):
```json
{"user_id":"uuid","session_id":"uuid","timestamp":"2026-01-13T00:00:00Z","message":"{\"original\":\"log\"}"}
{"user_id":"uuid","session_id":"uuid","timestamp":"2026-01-13T00:00:01Z","message":"{\"original\":\"log\"}"}
```

## Migration Considerations

### For Existing Deployments

1. **No Data Migration Required**: The system starts fresh with file-based storage
2. **Old Storage Objects**: Existing logs in the `RemoteLogs` collection remain in the database but are no longer accessed
3. **Cleanup**: Operators can optionally clean up old `RemoteLogs` storage objects:
   ```sql
   DELETE FROM storage WHERE collection = 'RemoteLogs';
   ```

### Backup Strategy

1. **Database Backups**: Can now exclude remote log data (not in database anymore)
2. **Remote Log Backups**: Optionally back up `<data_dir>/remotelogs/` separately if needed
3. **Retention**: Most deployments won't need to back up remote logs at all due to their debugging-only nature

## Performance Benefits

1. **Reduced Database Load**: Eliminates high-volume storage writes from database
2. **Smaller Backups**: Database backups are significantly smaller without remote logs
3. **Faster Restores**: Database restores are faster without remote log data
4. **Better Scalability**: File-based storage scales better for high-volume log writes

## Monitoring and Operations

### Log File Location
```
<data_dir>/remotelogs/
├── remotelog-2026-01-13T00-00-00.jsonl
├── remotelog-2026-01-13T12-30-15.jsonl
└── remotelog-2026-01-14T00-00-00.jsonl
```

### Disk Space Management
- Automatic cleanup removes files older than 7 days
- File rotation occurs at 10MB or daily, whichever comes first
- Monitor disk space in `<data_dir>/remotelogs/` directory

### Debugging Access
Remote logs can be accessed programmatically via the `UserLogJournalRegistry.Read()` method for debugging purposes.

## Testing

Comprehensive tests are provided in `evr_remotelog_file_writer_test.go`:
- Write and read operations
- File rotation
- Automatic cleanup
- JSON Lines format validation
- Directory creation
- Timestamp filtering

Run tests with:
```bash
go test -v -run TestRemoteLogFileWriter ./server/
```

## Future Enhancements

Potential improvements for future consideration:
- Compression of rotated log files
- Integration with external log aggregation systems (e.g., ELK, Splunk)
- Cloud storage backend option (S3, GCS)
- Configurable retention periods
- Log streaming API for real-time monitoring
