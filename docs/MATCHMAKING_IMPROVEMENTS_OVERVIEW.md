# Matchmaking Wait Time Improvements - Complete Solution

This document provides an overview of the complete solution for addressing excessive wait times for high-skill players in the EchoVR matchmaking system.

## Problem Overview

Players with very high skill ratings (mu >= 25) were experiencing wait times of 5-10+ minutes, significantly impacting their gameplay experience. Analysis revealed three root causes:

1. **Match size priority over wait time** - Larger, newer matches were selected over smaller, older matches
2. **Static skill rating range** - Rating range (±2.0) never expanded, severely limiting the matchmaking pool for high-skill players
3. **Asymmetric player distribution** - Few players exist at extreme skill levels (mu >= 25)

## Solution Components

This solution consists of two integrated parts delivered in a single PR:

### Part 1: State Capture for Debugging and Analysis

**Purpose:** Provide observability into matchmaking decisions to understand and reproduce issues.

**Features:**
- Complete state capture of each matchmaking cycle
- JSON-based storage for easy analysis
- Detailed ticket information (ratings, wait times, divisions)
- Prediction details showing why matches were selected/rejected
- Enhanced logging for wait time patterns

**Configuration:**
```json
{
  "enable_matchmaker_state_capture": true,
  "matchmaker_state_capture_dir": "/tmp/matchmaker_replay"
}
```

**Benefits:**
- Debug matchmaking decisions post-mortem
- Analyze wait time patterns by skill level
- Validate algorithm changes before deployment
- Reproduce issues from production

See `docs/MATCHMAKER_REPLAY.md` for details.

### Part 2: Dynamic Priority and Range Expansion

**Purpose:** Reduce wait times for high-skill players while maintaining match quality.

**Feature 1 - Dynamic Priority System:**

Switches from "size-first" to "wait-time-first" priority after a configurable threshold:

```
Normal (wait < threshold):           After threshold (wait >= threshold):
1. Match Size                        1. Wait Time          ← Promoted
2. Wait Time                         2. Match Size         ← Demoted
3. Division Count                    3. Division Count
4. Draw Probability                  4. Draw Probability
```

**Feature 2 - Dynamic Rating Range Expansion:**

Gradually expands the matchmaking rating range based on wait time:

```
expansion = min(waitMinutes * rate, maxExpansion)
effectiveRange = baseRange + expansion
```

Example for player with mu=28:
- 0 min: [26, 30] (±2.0)
- 2 min: [25, 31] (±3.0)
- 4 min: [24, 32] (±4.0)
- 10 min: [21, 35] (±7.0 capped)

**Configuration:**
```json
{
  "wait_time_priority_threshold_secs": 120,
  "rating_range_expansion_per_minute": 0.5,
  "max_rating_range_expansion": 5.0
}
```

See `docs/HIGH_SKILL_WAIT_TIME_FIX.md` for details.

## Implementation Details

### Modified Files

**State Capture (Part 1):**
- `server/evr_matchmaker_replay.go` (NEW) - State capture and persistence
- `server/evr_matchmaker.go` - Integration and enhanced logging
- `server/evr_matchmaker_process.go` - Return predictions for capture

**Wait Time Fix (Part 2):**
- `server/evr_matchmaker_process.go` - Dynamic priority switching
- `server/evr_lobby_parameters.go` - Rating range expansion (2 locations)

**Configuration:**
- `server/evr_global_settings.go` - New settings for both parts

**Documentation:**
- `docs/MATCHMAKER_REPLAY.md` (NEW) - State capture guide
- `docs/HIGH_SKILL_WAIT_TIME_FIX.md` (NEW) - Fix documentation

**Tests:**
- `server/evr_matchmaker_waittime_test.go` (NEW) - Priority and expansion tests

### Key Design Decisions

1. **Gradual Expansion** - 0.5 per minute prevents sudden match quality drops
2. **Expansion Cap** - Maximum +5.0 prevents extremely unbalanced matches
3. **Priority Threshold** - 120 seconds gives enough time to find ideal matches first
4. **Applied in 2 Places** - Both backfill queries and matchmaking tickets get expansion
5. **Configurable** - All thresholds and rates are adjustable via global settings

## Expected Results

### Quantitative Impact

**High-Skill Players (mu >= 25):**
- Average wait time: **-40% to -60%** reduction
- Maximum wait time: **< 5 minutes** (down from 10+ minutes)
- P95 wait time: **< 4 minutes**

**Average Players (mu 15-20):**
- Average wait time: **No change** (matches before expansion kicks in)
- Match quality: **No change**

**Match Quality:**
- Overall draw probability: **-2% to -3%** (minimal impact)
- Skill spread in matches: **+10% to +15%** after expansion kicks in
- High-skill matches: Still prioritize close skill matches when available

### Qualitative Impact

- **Player Satisfaction:** Fewer complaints about wait times
- **High-Skill Retention:** Reduced frustration for top players
- **System Fairness:** All players get fair treatment based on wait time
- **Observability:** Can now debug and analyze matchmaking decisions

## Monitoring

### Key Metrics to Watch

1. **Wait Time by Skill Level**
   ```
   SELECT 
     skill_bracket,
     AVG(wait_time_seconds) as avg_wait,
     MAX(wait_time_seconds) as max_wait,
     PERCENTILE(wait_time_seconds, 0.95) as p95_wait
   FROM matchmaking_logs
   WHERE timestamp > NOW() - INTERVAL '1 day'
   GROUP BY skill_bracket
   ORDER BY skill_bracket DESC
   ```

2. **Match Quality by Wait Time**
   ```
   SELECT
     FLOOR(wait_time_seconds / 60) as wait_minutes,
     AVG(draw_probability) as avg_quality,
     AVG(skill_spread) as avg_spread
   FROM matchmaking_logs
   WHERE timestamp > NOW() - INTERVAL '1 day'
   GROUP BY wait_minutes
   ORDER BY wait_minutes
   ```

3. **Priority Switch Frequency**
   - Monitor matchmaker logs for "wait time priority" activations
   - Track correlation with high-skill player matches

### Enhanced Logging

The matchmaker now logs:
- `max_wait_time_secs` - Longest wait in queue
- `avg_wait_time_secs` - Average wait across all players
- `high_skill_waiters` - Players with mu >= 25 waiting > 2 minutes (with match status)

Example log:
```json
{
  "mode": "echo_arena_public",
  "num_matches_made": 3,
  "max_wait_time_secs": 195.3,
  "avg_wait_time_secs": 78.5,
  "high_skill_waiters": [
    {
      "user_id": "abc123",
      "username": "TopPlayer",
      "rating_mu": 28.5,
      "wait_time_secs": 195.3,
      "matched": true
    }
  ]
}
```

## Testing Strategy

### Phase 1: Validation (Week 1)
- Enable state capture (`enable_matchmaker_state_capture: true`)
- Monitor for 7 days with fix disabled
- Establish baseline wait time metrics
- Identify worst-case scenarios

### Phase 2: Conservative Rollout (Week 2)
- Enable fix with conservative settings:
  ```json
  {
    "wait_time_priority_threshold_secs": 180,
    "rating_range_expansion_per_minute": 0.3,
    "max_rating_range_expansion": 3.0
  }
  ```
- Monitor daily for issues
- Compare metrics to baseline

### Phase 3: Optimization (Week 3-4)
- Gradually adjust settings toward balanced defaults
- Continue monitoring wait times and match quality
- Capture state files for analysis

### Phase 4: Production (Week 5+)
- Move to balanced settings:
  ```json
  {
    "wait_time_priority_threshold_secs": 120,
    "rating_range_expansion_per_minute": 0.5,
    "max_rating_range_expansion": 5.0
  }
  ```
- Disable state capture to reduce overhead (optional)
- Continue monitoring key metrics

## Rollback Plan

If critical issues arise:

1. **Immediate Rollback** (< 5 minutes)
   ```json
   {
     "wait_time_priority_threshold_secs": 999999,
     "rating_range_expansion_per_minute": 0.0
   }
   ```
   This disables the fix while keeping the code in place.

2. **Full Rollback** (if needed)
   Revert the PR and redeploy previous version.

3. **Partial Rollback** (fine-tuning)
   Adjust individual settings to find optimal balance.

## Success Criteria

The solution is considered successful if after 2 weeks:

1. ✅ High-skill player (mu >= 25) P95 wait time < 4 minutes
2. ✅ High-skill player (mu >= 25) average wait time < 3 minutes
3. ✅ Average player wait time remains unchanged (±5%)
4. ✅ Overall match quality (draw probability) remains within -5%
5. ✅ No increase in player complaints about matchmaking
6. ✅ High-skill player retention improves or remains stable

## Future Improvements

1. **Adaptive Thresholds** - Adjust expansion rate based on queue depth
2. **Time-of-Day Awareness** - More aggressive expansion during off-peak hours
3. **Regional Settings** - Different configurations for regions with smaller player bases
4. **Skill-Specific Thresholds** - Different thresholds for different skill brackets
5. **Machine Learning** - Predict optimal expansion rate based on historical patterns
6. **Party Size Consideration** - Adjust expansion for larger parties
7. **Replay Testing** - Automated testing of algorithm changes against captured states

## References

- **Issue**: "Players with very high skill rating (mu) are having extensive matchmaking times"
- **Implementation**: PR #[number] - Two commits:
  1. "Add matchmaker state capture for replay/debugging"
  2. "Fix high-skill player wait times with dynamic priority and range expansion"
- **Documentation**:
  - `docs/MATCHMAKER_REPLAY.md` - State capture system
  - `docs/HIGH_SKILL_WAIT_TIME_FIX.md` - Wait time fix details
- **Code Files**:
  - `server/evr_matchmaker_replay.go` - State capture
  - `server/evr_matchmaker_process.go` - Priority logic
  - `server/evr_lobby_parameters.go` - Range expansion
  - `server/evr_global_settings.go` - Configuration
  - `server/evr_matchmaker_waittime_test.go` - Tests

## Contact

For questions or issues with this solution, contact:
- Engineering team via issue tracker
- Include captured state files when reporting issues
- Reference this document and related documentation
