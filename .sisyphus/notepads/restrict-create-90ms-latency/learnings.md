# Learnings - restrict-create-90ms-latency

This file tracks conventions, patterns, and wisdom discovered during implementation.
# Latency Filter Implementation - Learnings

## Implementation Summary
Successfully added 90ms maximum latency restriction to `/create` Discord command in `server/evr_discord_appbot_handlers.go`.

### Key Implementation Details

1. **Data Structure**: `extIPs` is `map[string]int` where key is IP address string and value is RTT in milliseconds.

2. **Filter Pattern**: Standard Go idiom for filtering maps:
   - Iterate through original map
   - Track minimum value for error messaging
   - Build new filtered map with qualifying entries
   - Pass filtered map to allocation function

3. **Error Handling**: When no servers qualify, returns user-friendly error message with best available latency:
   ```go
   "no servers within 90ms of your location. Your best server has %dms latency"
   ```

4. **Filter Placement**: Code inserted after line 772 (`extIPs := latencyHistory.AverageRTTs(true)`), before `MatchSettings` initialization. This ensures early validation without unnecessary data structure creation.

5. **Latency History Integration**: The `AverageRTTs(true)` method already returns rounded RTT values (to nearest 10ms), so no additional rounding needed in the filter.

### Code Pattern
```go
const maxLatencyMs = 90
filteredIPs := make(map[string]int)
minLatencyMs := -1

// Single-pass iteration: find min and filter
for ip, latency := range extIPs {
    if minLatencyMs == -1 || latency < minLatencyMs {
        minLatencyMs = latency
    }
    if latency <= maxLatencyMs {
        filteredIPs[ip] = latency
    }
}

// Guard: only error if we have data but nothing qualifies
if len(filteredIPs) == 0 && minLatencyMs > maxLatencyMs {
    return nil, 0, fmt.Errorf("...")
}
```

### Testing Notes
- File passes LSP diagnostics with no errors
- Pre-existing build/test failures in evr package unrelated to changes
- Implementation is isolated to handleCreateMatch function, no side effects on other code paths
