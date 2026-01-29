# Learnings: Tier Degradation Logic Fix

## Bug: Inverted Tier Comparison
Fixed critical bug in tier degradation detection where the comparison logic was inverted.

### Root Cause
The code used `oldTier > newTier` to detect degradation, but the tier system uses:
- Tier1 = 1 (good standing)
- Tier2 = 2 (penalty tier)

When a player degrades, they move from Tier1 (1) → Tier2 (2), so `newTier (2) > oldTier (1)` is the correct comparison.

### Locations Fixed
1. **server/evr_match.go:743** - SendTierChangeNotification call
2. **server/evr_earlyquit.go:310** - SendTierChangeNotification call

### Pattern Changed
```go
// BEFORE (inverted):
messageTrigger.SendTierChangeNotification(ctx, ..., oldTier > newTier)

// AFTER (correct):
messageTrigger.SendTierChangeNotification(ctx, ..., newTier > oldTier)
```

### Verification Steps Completed
✅ Grep confirmed old pattern removed from both SendTierChangeNotification calls
✅ Grep confirmed new pattern present in both locations
✅ Go build ./server/... succeeds with no errors

### Notes
- The Discord notification logic in evr_match.go:752 still correctly uses `oldTier > newTier` for a different purpose (determining message text), which is correct
- Tier degradation now properly triggers when players move to penalty tiers
- Tier restoration properly triggers when players recover to good standing

## Fixed Penalty Expiry Notification Spam

**Problem**: The `checkAndNotifyExpiredPenalties` scheduler sent PenaltyExpired notifications repeatedly every 30 seconds because it had no memory of which players were already notified.

**Solution**: Added tracking field to prevent duplicate notifications:
1. Added `LastExpiryNotificationSent time.Time` to `EarlyQuitConfig` struct
2. Updated `checkAndNotifyExpiredPenalties` to check if notification was already sent
3. Logic: Only send if `LastExpiryNotificationSent` is zero OR before `LastEarlyQuitTime` (indicates new penalty)
4. After sending, update `LastExpiryNotificationSent` to current time and save config

**Files Changed**:
- `server/evr_earlyquit.go`: Added field with json tag `last_expiry_notification_sent`
- `server/evr_early_quit_message_trigger.go`: Added deduplication logic at lines 361-373

**Pattern**: Use timestamp tracking on penalty state to prevent scheduler spam - check field before sending notification, update field after sending.


## Login Early Quit Features Population (2026-01-28)

Fixed bug where `clientProfile.EarlyQuitFeatures` was always zeroed on login despite active penalties.

**Root Cause**: Test stub was removed in PR #277 but no replacement logic was added.

**Solution**: In `server/evr_pipeline_login.go` `loggedInUserProfileRequest()` function (line ~916):
- Populate `EarlyQuitFeatures` from `params.earlyQuitConfig` atomic.Pointer
- Calculate penalty end time: `LastEarlyQuitTime + GetLockoutDuration(level)`
- Only set fields if penalty is still active (current time < penalty end time)

**Key Details**:
- `params.earlyQuitConfig` is `*atomic.Pointer[EarlyQuitConfig]` - must call `.Load()`
- Server struct: `server/evr_earlyquit.go` EarlyQuitConfig (storage)
- Client struct: `server/evr/core_account.go` EarlyQuitFeatures (protocol)
- Field mapping: `PenaltyLevel` (int), `PenaltyTimestamp` (int64 unix)

**Pattern**: When client profile is constructed from server data, must explicitly populate all derived fields.


## SpawnLock Service Setting Check (2026-01-28)

**Problem**: SpawnLock enforcement in match join was only gated by the feature flag `DefaultEarlyQuitFeatureFlags().EnableSpawnLock`, but not the service setting `ServiceSettings().Matchmaking.EnableEarlyQuitPenalty`. This could block players from joining matches even when the early quit penalty system is disabled in configuration.

**Root Cause**: QueueBlocking check in `evr_lobby_find.go` correctly gated by both feature flag AND service setting, but SpawnLock in `evr_match.go` only checked the feature flag.

**Solution**: Added service setting check to SpawnLock enforcement in `server/evr_match.go` around line 259:

```go
// Get service settings
serviceSettings := ServiceSettings()
enableEarlyQuitPenalty := true
if serviceSettings != nil {
    enableEarlyQuitPenalty = serviceSettings.Matchmaking.EnableEarlyQuitPenalty
}

// Gate SpawnLock by BOTH feature flag AND service setting
if featureFlags != nil && featureFlags.EnableSpawnLock && enableEarlyQuitPenalty && !meta.Presence.IsSpectator() {
    // ... existing SpawnLock enforcement logic
}
```

**Key Details**:
- Default to `enableEarlyQuitPenalty = true` if ServiceSettings is nil (safe default)
- Check both conditions in the if statement: `featureFlags.EnableSpawnLock && enableEarlyQuitPenalty`
- Matches pattern used in QueueBlocking check (same file, different function)

**Verification**:
- ✅ Grep confirmed both checks present in condition
- ✅ Go build ./server/... succeeds with no errors
- ✅ Pattern consistent with QueueBlocking implementation in evr_lobby_find.go

**Pattern**: Service settings must gate feature flags for extensibility - allows ops to disable early quit system entirely without feature flag changes.


## Lockout Duration Comments & Helper Consolidation (2026-01-28)

**Problem**: Comments didn't match actual lockout duration values.
- Comment in `server/evr_match.go:730` incorrectly stated "(5min, 15min, 30min, 60min)"
- Actual durations in `EarlyQuitLockoutDurations` map are: 0s, 2m, 5m, 15m for penalty levels 0-3

**Good News**: No duplicate definitions found - all code already uses shared helpers!
- `GetLockoutDuration()` and `GetLockoutDurationSeconds()` consistently used across codebase
- Found in: evr_early_quit_message_trigger.go, evr_lobby_find.go, evr_match.go, evr_pipeline_login.go
- Locations all call the shared helper functions, no local maps

**Solution**: Updated misleading comment in `server/evr_match.go:730`:
```go
// BEFORE:
// Default lockout durations in seconds (5min, 15min, 30min, 60min)
lockoutDuration := GetLockoutDurationSeconds(int(penaltyLevel))

// AFTER:
// Get lockout duration for current penalty level (0s, 2m, 5m, 15m)
lockoutDuration := GetLockoutDurationSeconds(int(penaltyLevel))
```

**Source of Truth** (server/evr_earlyquit.go):
```go
var EarlyQuitLockoutDurations = map[int]time.Duration{
    0: 0 * time.Second,   // No lockout
    1: 120 * time.Second, // 2 minutes
    2: 300 * time.Second, // 5 minutes
    3: 900 * time.Second, // 15 minutes
}
```

**Verification Completed**:
✅ `grep -n "lockoutDurations :=" server/*.go` → No matches (no duplicate definitions)
✅ `grep -n "5min, 15min, 30min, 60min" server/*.go` → No matches (incorrect comment removed)
✅ `go build ./server/...` → Succeeds with no errors

**Pattern**: Comments should document actual behavior. When durations are defined in shared constants, comment values in calling code should match the map/array exactly. Use helper functions to lookup, don't hardcode duration values.

