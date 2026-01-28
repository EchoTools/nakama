# Matchmaker State Capture and Replay System

## Overview

This document describes the matchmaker state capture and replay system that enables debugging and testing of matchmaking decisions.

## Purpose

The matchmaker state capture system was created to address issues with players experiencing long wait times, particularly those with very high skill ratings (mu). By capturing the complete state of each matchmaking cycle, we can:

1. **Debug matchmaking decisions** - Understand why certain matches were made or not made
2. **Analyze wait time patterns** - Identify which players wait the longest and why
3. **Test new algorithms** - Replay captured states to validate algorithm changes
4. **Reproduce issues** - Capture problematic matchmaking scenarios for analysis

## Configuration

State capture is controlled by two settings in the global matchmaking configuration:

```json
{
  "matchmaking": {
    "enable_matchmaker_state_capture": true,
    "matchmaker_state_capture_dir": "/tmp/matchmaker_replay"
  }
}
```

- `enable_matchmaker_state_capture` (default: `false`) - Enable/disable state capture
- `matchmaker_state_capture_dir` (default: `"/tmp/matchmaker_replay"`) - Directory to save state files

## Captured State

Each matchmaking cycle captures:

### Basic Information
- **Timestamp** - When the matchmaking cycle ran
- **Mode** - Game mode (e.g., "echo_arena_public")
- **Group ID** - Matchmaking group identifier
- **Processing Time** - How long the matchmaker took to process (milliseconds)

### Candidates and Matches
- **Candidates** - All potential match combinations considered
- **Matches** - Final selected matches
- **Filter Counts** - Number of candidates filtered out by each filter (e.g., max_rtt)

### Player Statistics
- **Total Players** - Number of unique players in the queue
- **Total Tickets** - Number of matchmaking tickets
- **Matched Players** - Number of players successfully matched
- **Unmatched Players** - Number of players not matched

### Detailed Entry Data
For each matchmaker entry (player/party):
- **Ticket ID** - Unique matchmaking ticket identifier
- **Presence** - User ID, session ID, username
- **Properties** - All ticket properties including:
  - `rating_mu` / `rating_sigma` - Skill rating
  - `submission_time` - When ticket was created (Unix timestamp)
  - `divisions` - Player divisions
  - `max_rtt` - Maximum acceptable server latency
  - RTT properties - Latency to each server region
- **Party ID** - Party identifier if in a group

### Prediction Details (Optional)
When available, includes details about each predicted match:
- **Size** - Number of players in the match
- **Division Count** - Number of different skill divisions
- **Oldest Ticket Timestamp** - Age of the oldest ticket in the match
- **Draw Probability** - Predicted match balance (0-1, higher = more balanced)
- **Selected** - Whether this prediction was chosen
- **Ticket Summaries** - Per-ticket information:
  - Player count
  - Rating (mu/sigma)
  - Wait time in seconds
  - Divisions

## File Format

State files are saved as JSON with the naming pattern:
```
matchmaker_state_YYYYMMDD_HHMMSS_<mode>.json
```

Example filename: `matchmaker_state_20260122_153045_echo_arena_public.json`

## Usage Examples

### Enable State Capture

Update the global settings (via RPC or database):

```json
{
  "matchmaking": {
    "enable_matchmaker_state_capture": true
  }
}
```

### Load and Analyze State

```go
import "github.com/heroiclabs/nakama/v3/server"

// Load a captured state
state, err := server.LoadMatchmakerState("/tmp/matchmaker_replay/matchmaker_state_20260122_153045_echo_arena_public.json")
if err != nil {
    log.Fatal(err)
}

// Analyze high-skill players waiting
for _, detail := range state.PredictionDetails {
    for _, ticket := range detail.Tickets {
        if ticket.RatingMu >= 25.0 && ticket.WaitTimeSeconds > 120 {
            fmt.Printf("High-skill player waiting: mu=%.2f, wait=%.0fs, selected=%v\n",
                ticket.RatingMu, ticket.WaitTimeSeconds, detail.Selected)
        }
    }
}
```

### Analyze Wait Times by Skill Level

```go
// Group players by skill range
skillBuckets := make(map[string][]float64)
for _, candidate := range state.Candidates {
    for _, entry := range candidate.Entries {
        mu := entry.NumericProperties["rating_mu"]
        submissionTime := entry.NumericProperties["submission_time"]
        waitTime := float64(state.Timestamp.Unix()) - submissionTime
        
        bucket := fmt.Sprintf("%.0f-%.0f", math.Floor(mu/5)*5, math.Ceil(mu/5)*5)
        skillBuckets[bucket] = append(skillBuckets[bucket], waitTime)
    }
}

// Print average wait times per skill range
for bucket, times := range skillBuckets {
    avg := sum(times) / float64(len(times))
    fmt.Printf("Skill range %s: avg wait %.0fs (%d players)\n", bucket, avg, len(times))
}
```

## Enhanced Logging

In addition to state capture, the matchmaker now logs additional information:

- **max_wait_time_secs** - Longest wait time of any player in the queue
- **avg_wait_time_secs** - Average wait time across all players
- **high_skill_waiters** - Array of high-skill players (mu >= 25) waiting > 2 minutes, including:
  - User ID and username
  - Rating (mu)
  - Wait time
  - Whether they were matched

Example log output:
```json
{
  "mode": "echo_arena_public",
  "num_player_total": 24,
  "num_matches_made": 3,
  "max_wait_time_secs": 180.5,
  "avg_wait_time_secs": 45.2,
  "high_skill_waiters": [
    {
      "user_id": "abc123",
      "username": "ProPlayer",
      "rating_mu": 28.5,
      "wait_time_secs": 180.5,
      "matched": false
    }
  ]
}
```

## Troubleshooting

### State Files Not Being Created

1. Check that `enable_matchmaker_state_capture` is `true` in global settings
2. Verify the capture directory exists and is writable
3. Check server logs for "Failed to save matchmaker state" errors

### Large State Files

State files can be large (several MB) when there are many players queued. Consider:
- Only enabling capture during specific debugging periods
- Using log rotation or cleanup scripts for the capture directory
- Analyzing wait time patterns from enhanced logs instead of full state

### Performance Impact

State capture has minimal performance impact:
- Captures happen after matchmaking decisions are made
- File I/O is non-blocking (happens after matches are returned)
- Typical overhead: < 5ms per matchmaking cycle

## Related Issues

- Issue: "Players with very high skill rating (mu) are having extensive matchmaking times"
- Root causes identified through state capture:
  1. Static skill range doesn't expand based on wait time
  2. Match size priority beats wait time priority
  3. No dynamic relaxation of rating_mu range
  4. Asymmetric player pools at extreme skill levels

## Future Enhancements

Potential improvements to the state capture system:

1. **Replay Testing Framework** - Automated testing of algorithm changes against captured states
2. **State Comparison Tool** - Diff two states to see how algorithm changes affect outcomes
3. **Aggregated Analytics** - Process multiple state files to identify patterns
4. **Real-time Dashboard** - Live view of matchmaking statistics
5. **Selective Capture** - Only capture states meeting certain criteria (e.g., long wait times)
