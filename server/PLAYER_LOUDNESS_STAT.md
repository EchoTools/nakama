# Player Loudness Leaderboard Stat

This document describes how to use the PlayerLoudness leaderboard statistic.

## Overview

The PlayerLoudness stat tracks player voice loudness measured in decibels (dB) from
VOIP_LOUDNESS remote logs. It accumulates data daily and provides:
- Daily average loudness per player
- Minimum and maximum loudness values
- Total sample count

## Data Storage

The leaderboard record stores:
- **Score/Subscore**: Total accumulated loudness (sum of all samples)
- **Metadata**:
  - `min_loudness`: Minimum loudness value seen (float)
  - `max_loudness`: Maximum loudness value seen (float)
  - `count`: Number of loudness samples (int)

## Calculating Daily Average Loudness

The daily average loudness is calculated as:

  average_loudness = total_loudness / session_time

Where:
- `total_loudness` is decoded from the leaderboard score/subscore
- `session_time` is obtained from the GameServerTime leaderboard (in seconds)

## Example Code

```go
func GetPlayerDailyAverageLoudness(ctx context.Context, nk runtime.NakamaModule, 
    userID, groupID string, mode evr.Symbol) (avgLoudness, minLoudness, maxLoudness float64, count int64, err error) {
    
    // Get loudness leaderboard record
    loudnessID := StatisticBoardID(groupID, mode, PlayerLoudnessStatisticID, evr.ResetScheduleDaily)
    _, records, _, _, err := nk.LeaderboardRecordsList(ctx, loudnessID, []string{userID}, 1, "", 0)
    if err != nil || len(records) == 0 {
        return 0, 0, 0, 0, fmt.Errorf("no loudness data found")
    }
    
    record := records[0]
    
    // Decode total loudness
    totalLoudness, err := ScoreToFloat64(record.Score, record.Subscore)
    if err != nil {
        return 0, 0, 0, 0, err
    }
    
    // Extract metadata
    metadata := record.Metadata.(map[string]interface{})
    minLoudness = metadata["min_loudness"].(float64)
    maxLoudness = metadata["max_loudness"].(float64)
    count = int64(metadata["count"].(float64))
    
    // Get session time
    sessionTimeID := StatisticBoardID(groupID, mode, GameServerTimeStatisticsID, evr.ResetScheduleDaily)
    _, timeRecords, _, _, err := nk.LeaderboardRecordsList(ctx, sessionTimeID, []string{userID}, 1, "", 0)
    if err != nil || len(timeRecords) == 0 {
        return 0, 0, 0, 0, fmt.Errorf("no session time data found")
    }
    
    sessionTime, err := ScoreToFloat64(timeRecords[0].Score, timeRecords[0].Subscore)
    if err != nil {
        return 0, 0, 0, 0, err
    }
    
    // Calculate average
    if sessionTime > 0 {
        avgLoudness = totalLoudness / sessionTime
    }
    
    return avgLoudness, minLoudness, maxLoudness, count, nil
}
```

## Leaderboard ID Format

The leaderboard ID follows the standard format:

  {groupID}:{mode}:PlayerLoudness:daily

Example: `my-guild-123:echo_arena_public:PlayerLoudness:daily`

## Reset Schedule

The loudness stat resets daily at 16:00 UTC, matching the GameServerTime reset schedule.
This allows consistent daily average calculations across all daily stats.

## Notes

- Loudness values are typically negative (dB), with higher (less negative) values being louder
- Example loudness range: -60 dB (quiet) to -10 dB (loud)
- The stat only tracks players on Blue/Orange teams, not spectators
- VOIP_LOUDNESS messages are sent periodically during gameplay
