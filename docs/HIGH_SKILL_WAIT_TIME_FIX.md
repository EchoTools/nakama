# High-Skill Player Wait Time Fix

## Overview

This document describes the solution implemented to reduce matchmaking wait times for players with very high skill ratings (mu >= 25).

## Problem Statement

Players with very high skill ratings were experiencing excessive wait times (5+ minutes) due to:

1. **Match size priority over wait time** - The matchmaker prioritized larger matches over older tickets, meaning a new 6-player match would be selected over an older 4-player match, even if players in the smaller match had been waiting 5+ minutes.

2. **Static skill rating range** - The `MatchmakingRatingRange` (e.g., ±2.0) never expanded based on wait time. A high-skill player with mu=28 could only match with players in [26, 30], severely limiting the matchmaking pool.

3. **Asymmetric player distribution** - Few players exist at extreme skill levels, compounding the static range problem.

## Solution

### 1. Dynamic Priority System

The matchmaker now switches from "size-first" to "wait-time-first" priority after a configurable threshold.

**Before:**
```
1. Match Size (larger better)
2. Wait Time (older better)
3. Division Count (fewer better)
4. Draw Probability (higher better)
```

**After (when oldest ticket exceeds threshold):**
```
1. Wait Time (older better)          ← Promoted to #1
2. Match Size (larger better)        ← Demoted to #2
3. Division Count (fewer better)
4. Draw Probability (higher better)
```

**Configuration:**
```json
{
  "matchmaking": {
    "wait_time_priority_threshold_secs": 120
  }
}
```

Default: `120` seconds (2 minutes)

**Example Impact:**
- Player A has been waiting 180 seconds in a 4-player match
- Player B just joined a 6-player match (10 seconds old)
- **Before**: 6-player match selected (size priority)
- **After**: 4-player match selected (wait time priority kicks in at 120s)

### 2. Dynamic Rating Range Expansion

The matchmaking rating range now expands gradually based on wait time, allowing players to match with a wider skill range as they wait longer.

**Formula:**
```
expansion = min(waitMinutes * expansionPerMinute, maxExpansion)
effectiveRange = baseRange + expansion
```

**Configuration:**
```json
{
  "matchmaking": {
    "rating_range": 2.0,
    "rating_range_expansion_per_minute": 0.5,
    "max_rating_range_expansion": 5.0
  }
}
```

**Defaults:**
- `rating_range`: `2.0` (base range)
- `rating_range_expansion_per_minute`: `0.5`
- `max_rating_range_expansion`: `5.0`

**Example Progression (mu=28 player):**

| Wait Time | Expansion | Effective Range | Can Match With |
|-----------|-----------|-----------------|----------------|
| 0 min     | +0.0      | ±2.0            | [26.0, 30.0]   |
| 1 min     | +0.5      | ±2.5            | [25.5, 30.5]   |
| 2 min     | +1.0      | ±3.0            | [25.0, 31.0]   |
| 4 min     | +2.0      | ±4.0            | [24.0, 32.0]   |
| 6 min     | +3.0      | ±5.0            | [23.0, 33.0]   |
| 10 min    | +5.0 (cap)| ±7.0            | [21.0, 35.0]   |

### 3. Application Points

The dynamic rating range expansion is applied in two places:

1. **Backfill queries** (`buildBackfillQuery` in `evr_lobby_parameters.go`)
   - When searching for existing matches to join
   - Expands the label query exclusion ranges

2. **Matchmaking tickets** (`CreateMatchmakingTicket` in `evr_lobby_parameters.go`)
   - When creating new matchmaking tickets
   - Expands both the numeric properties and query exclusion ranges

## Expected Results

### For High-Skill Players (mu >= 25)

**Reduced Wait Times:**
- **0-2 minutes**: Behavior unchanged (looking for ideal skill match)
- **2+ minutes**: Priority switches to favor wait time over match size
- **2+ minutes**: Rating range begins expanding (±0.5 per minute)
- **4 minutes**: Can match with ±4 skill range instead of ±2
- **10+ minutes**: Maximum expansion reached (±7 total range)

**Maintained Match Quality:**
- Expansion is gradual (0.5 per minute), not instant
- Maximum expansion cap prevents extremely unbalanced matches
- Draw probability still considered in final tiebreaker
- High-skill players still match with high-skill players when possible

### For Average-Skill Players (mu 15-20)

**No Negative Impact:**
- Most players match within 1-2 minutes (expansion hasn't kicked in)
- Larger player pool at average skill levels means matches found quickly
- Size priority still matters for quick matches
- Only players waiting 2+ minutes see any changes

### System-Wide Metrics

**Expected Improvements:**
- High-skill player (mu >= 25) average wait time: **-40% to -60%**
- High-skill player (mu >= 25) max wait time: **< 5 minutes** (down from 10+ minutes)
- Overall player satisfaction: **Improved** (fewer complaints about wait times)
- Match quality (draw probability): **Minimal impact** (±2-3%)

## Configuration Recommendations

### Conservative (Prioritize Match Quality)
```json
{
  "wait_time_priority_threshold_secs": 180,
  "rating_range_expansion_per_minute": 0.3,
  "max_rating_range_expansion": 3.0
}
```
- Wait time priority after 3 minutes
- Slower expansion (0.3 per minute)
- Lower cap (±5 total at max)

### Balanced (Default)
```json
{
  "wait_time_priority_threshold_secs": 120,
  "rating_range_expansion_per_minute": 0.5,
  "max_rating_range_expansion": 5.0
}
```
- Wait time priority after 2 minutes
- Moderate expansion (0.5 per minute)
- Moderate cap (±7 total at max)

### Aggressive (Minimize Wait Times)
```json
{
  "wait_time_priority_threshold_secs": 60,
  "rating_range_expansion_per_minute": 0.8,
  "max_rating_range_expansion": 8.0
}
```
- Wait time priority after 1 minute
- Faster expansion (0.8 per minute)
- Higher cap (±10 total at max)

## Monitoring

### Key Metrics to Track

1. **Wait Time by Skill Bracket**
   ```
   Skill Range    Avg Wait    Max Wait    P95 Wait
   [15-20)        45s         2m          1m 30s
   [20-25)        1m 15s      3m          2m 30s
   [25-30)        2m 30s      5m          4m       ← Watch this
   [30+)          3m          6m          5m       ← Watch this
   ```

2. **Match Quality by Wait Time**
   ```
   Wait Time      Avg Draw Prob    Skill Spread
   < 2 min        0.48             ±2.1
   2-4 min        0.46             ±2.8
   4-6 min        0.44             ±3.5
   6+ min         0.42             ±4.2
   ```

3. **Priority Switch Events**
   ```
   - Number of matchmaking cycles where wait time priority activated
   - Average wait time when priority switches
   - Match sizes when priority is active vs. inactive
   ```

### Enhanced Logging

The matchmaker already logs:
- `max_wait_time_secs` - Longest wait in current queue
- `avg_wait_time_secs` - Average wait time
- `high_skill_waiters` - Array of high-skill players waiting > 2 minutes

Watch for patterns in `high_skill_waiters` - this array should shrink over time as the fix takes effect.

## Testing

### Manual Testing Steps

1. **Test Priority Switch**
   ```bash
   # Set threshold to 60 seconds for faster testing
   # Create 2 parties:
   # - Party A: 4 players, high skill (mu ~28), start first
   # - Party B: 6 players, mixed skill (mu ~20), start 90 seconds later
   # Expected: Party A matches first after 60 seconds (priority switches)
   ```

2. **Test Rating Range Expansion**
   ```bash
   # Set expansion to 1.0/min for faster testing
   # Create high-skill player (mu=28) with base range ±2
   # Wait and observe matchmaking queries:
   # - 0 min: Should query [26, 30]
   # - 1 min: Should query [25, 31]
   # - 2 min: Should query [24, 32]
   ```

3. **Test Maximum Expansion Cap**
   ```bash
   # Set max expansion to 3.0
   # Wait 10 minutes
   # Expected: Range stops at ±5.0 (base 2.0 + max 3.0), not ±7.0
   ```

### Automated Tests

See `server/evr_matchmaker_waittime_test.go`:
- `TestDynamicWaitTimePriority` - Verifies priority switching logic
- `TestRatingRangeExpansion` - Verifies expansion calculation
- `TestHighSkillPlayerScenario` - End-to-end high-skill player test

## Rollback Plan

If issues arise, the fix can be disabled by setting:

```json
{
  "wait_time_priority_threshold_secs": 999999,
  "rating_range_expansion_per_minute": 0.0
}
```

This effectively disables both features:
- Wait time priority will never activate (threshold unreachable)
- Rating range will never expand (0.0 expansion per minute)

## Future Enhancements

1. **Adaptive Expansion Rate** - Adjust expansion speed based on queue depth
2. **Skill-Based Thresholds** - Different thresholds for different skill levels
3. **Time-of-Day Awareness** - More aggressive expansion during off-peak hours
4. **Party Size Consideration** - Larger parties might need more aggressive expansion
5. **Regional Differences** - Different settings for regions with smaller player bases

## Related Documentation

- `MATCHMAKER_REPLAY.md` - State capture system for debugging
- `evr_matchmaker_process.go` - Priority sorting implementation
- `evr_lobby_parameters.go` - Rating range expansion implementation
- `evr_global_settings.go` - Configuration structure
