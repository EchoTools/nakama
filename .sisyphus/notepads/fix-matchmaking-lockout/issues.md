# Issues - Fix Matchmaking Lockout

## Problems & Gotchas
<!-- Subagents append findings here -->

## Task 5 - Expiry Scheduler Bug (FIXED)

### The Bug
**File:** `server/evr_early_quit_message_trigger.go` (lines 320-326, 356)

**Root Cause:** Expiry scheduler confused two different concepts:
- `MatchmakingTier` (1-4): Priority tier in matchmaking system (tier 1=good, 2=penalty, 3-4=reserved)
- `EarlyQuitPenaltyLevel` (0-3): Early quit penalty severity

**Incorrect Code Pattern:**
```go
lockoutDurations := map[int32]int32{
    1: 0,    // Tier 1 doesn't match penalty level 0
    2: 300,  // Tier 2 doesn't match penalty level 1
    3: 900,  // Tier 3 doesn't match penalty level 2
    4: 1800, // Tier 4 doesn't exist in penalty system!
}
lockoutDuration, ok := lockoutDurations[eqConfig.MatchmakingTier]  // WRONG KEY
```

**Why This Failed:**
- Tier 4 (never set in normal operation) would always hit the "unknown tier" branch
- Early quit notifications would never be sent properly
- Players stuck with unexpired penalties indefinitely

### Resolution Applied
✓ Removed entire tier-based map (lines 320-326)
✓ Changed line 348: `lockoutDuration := GetLockoutDurationSeconds(int(penaltyLevel))`
✓ Comment updated to clarify penalty-level-based lookup
✓ Cast to `int(penaltyLevel)` since helper expects `int` type parameter
