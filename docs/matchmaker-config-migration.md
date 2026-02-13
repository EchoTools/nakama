# Matchmaker Configuration Migration Guide

## Overview

The matchmaker configuration has been migrated from `ServiceSettingsData` to a dedicated `EVRMatchmakerConfig` structure stored in system storage. This provides better organization, validation, and separation of concerns.

## What Changed

### Before (Old System)
```go
// Accessing matchmaker config through ServiceSettings
settings := ServiceSettings()
timeout := settings.Matchmaking.MatchmakingTimeoutSecs
enableSBMM := settings.Matchmaking.EnableSBMM
skillDefaults := settings.SkillRating.Defaults
```

### After (New System)
```go
// Accessing matchmaker config through dedicated accessor
config := EVRMatchmakerConfigGet()
timeout := config.Timeout.MatchmakingTimeoutSecs
enableSBMM := config.SBMM.EnableSBMM
skillDefaults := config.SkillRating.Defaults
```

## Storage Location

- **Collection**: `SystemConfig`
- **Key**: `matchmaker`
- **User ID**: `00000000-0000-0000-0000-000000000000` (system)
- **Permissions**: Read=2 (public), Write=0 (system only)

## Configuration Structure

The new `EVRMatchmakerConfig` organizes 43 fields into 10 logical groups:

### 1. TimeoutConfig (3 fields)
- `MatchmakingTimeoutSecs` - Maximum time for matchmaking (default: 360s)
- `FailsafeTimeoutSecs` - Failsafe timeout (default: 300s)
- `FallbackTimeoutSecs` - Fallback timeout (default: 150s)

### 2. BackfillConfig (4 fields)
- `DisableArenaBackfill` - Disable arena match backfilling
- `ArenaBackfillMaxAgeSecs` - Max age for backfillable matches (default: 270s)
- `BackfillMinTimeSecs` - Minimum time before backfilling
- `EnablePostMatchmakerBackfill` - Enable post-matchmaker backfill

### 3. ReducingPrecisionConfig (2 fields)
- `IntervalSecs` - Interval for constraint relaxation (default: 30s)
- `MaxCycles` - Maximum relaxation cycles (default: 5)

### 4. SkillBasedMatchmakingConfig (12 fields)
- `EnableSBMM` - Enable skill-based matchmaking
- `MinPlayerCount` - Minimum players for SBMM (default: 24)
- `EnableOrdinalRange` - Enable ordinal range matching
- `RatingRange` - Rating range for matching
- `RatingRangeExpansionPerMinute` - Range expansion rate (default: 0.5)
- `MaxRatingRangeExpansion` - Maximum expansion (default: 5.0)
- `MatchmakingTicketsUseMu` - Use Mu instead of Ordinal for tickets
- `BackfillQueriesUseMu` - Use Mu for backfill queries
- `MatchmakerUseMu` - Use Mu for matchmaker values
- `PartySkillBoostPercent` - Party coordination boost (default: 0.10)
- `EnableRosterVariants` - Generate multiple roster options
- `UseSnakeDraftTeamFormation` - Use snake draft for teams

### 5. DivisionConfig (2 fields)
- `EnableDivisions` - Enable division system
- `GreenDivisionMaxAccountAgeDays` - Max age for green division (default: 30)

### 6. EarlyQuitConfig (5 fields)
- `EnableEarlyQuitPenalty` - Enable penalty system
- `SilentEarlyQuitSystem` - Disable Discord notifications
- `EarlyQuitTier1Threshold` - Tier 1 threshold (default: -3)
- `EarlyQuitTier2Threshold` - Tier 2 threshold (default: -10)
- `EarlyQuitLossThreshold` - Loss ratio threshold (default: 0.2)

### 7. ServerSelectionConfig (4 fields)
- `MaxServerRTT` - Maximum server RTT (default: 180ms)
- `ServerRatings` - Server quality ratings
- `ExcludeServerList` - Excluded server IDs
- `RTTDeltaOverrides` - RTT delta overrides per server

### 8. QueryAddons (6 fields)
- `Backfill` - Backfill query addons
- `LobbyBuilder` - Server allocation query addons
- `Create` - Lobby creation query addons
- `Allocate` - Generic allocation query addons
- `Matchmaking` - Matchmaking ticket query addons
- `RPCAllocate` - RPC allocation query addons

### 9. DebugConfig (2 fields)
- `EnableMatchmakerStateCapture` - Enable state capture
- `MatchmakerStateCaptureDir` - State capture directory (default: "/tmp/matchmaker_replay")

### 10. PriorityConfig (2 fields)
- `WaitTimePriorityThresholdSecs` - Wait time priority threshold (default: 120s)
- `MaxMatchmakingTickets` - Maximum concurrent tickets (default: 24)

### 11. SkillRatingConfig (4 fields + nested)
- `Defaults` - Default rating values
  - `Z` - Standard deviations for ordinal (default: 3)
  - `Mu` - Initial mean skill (default: 10.0)
  - `Sigma` - Initial uncertainty (calculated: Mu/Z)
  - `Tau` - Dynamics factor (default: 0.3)
- `TeamStatMultipliers` - Team stat weights (map[string]float64)
- `PlayerStatMultipliers` - Player stat weights (map[string]float64)
- `WinningTeamBonus` - Winning team bonus (default: 4.0)

## Validation Rules

The new config includes comprehensive validation:

1. All timeout values must be ≥ 0
2. Percentage values must be between 0.0 and 1.0
3. `MinPlayerCount` must be > 0
4. Rating ranges must be ≥ 0
5. `MaxServerRTT` must be > 0
6. Early quit thresholds must be ordered: Tier1 < Tier2
7. `RatingRangeExpansionPerMinute` must be ≥ 0
8. `MaxRatingRangeExpansion` must be ≥ 0
9. `WaitTimePriorityThresholdSecs` must be ≥ 0
10. `MaxMatchmakingTickets` must be ≥ 0
11. `GreenDivisionMaxAccountAgeDays` must be ≥ 0
12. `MaxCycles` must be > 0 when `IntervalSecs` > 0

## Migration Path

### For Code

Replace all occurrences of:
- `ServiceSettings().Matchmaking.*` → `EVRMatchmakerConfigGet().*`
- `ServiceSettings().SkillRating.*` → `EVRMatchmakerConfigGet().SkillRating.*`

### For Stored Configurations

No action required. The system maintains backward compatibility:
- Old `Matchmaking` and `SkillRating` fields remain in `ServiceSettingsData`
- Existing stored configs will continue to work
- New config is loaded independently from system storage
- First load creates new config with defaults if not present

## API Changes

### Loading Configuration

```go
// Automatic loading during server startup
ServiceSettingsLoad(ctx, logger, nk)

// Manual loading
config, err := LoadEVRMatchmakerConfig(ctx, nk, setDefaults)
if err != nil {
    return err
}
EVRMatchmakerConfigSet(config)
```

### Saving Configuration

```go
config := EVRMatchmakerConfigGet()
// Modify config...
config.SBMM.EnableSBMM = true

// Validate before saving
if err := config.Validate(); err != nil {
    return err
}

// Save to storage
if err := SaveEVRMatchmakerConfig(ctx, nk, config); err != nil {
    return err
}
```

### Accessing Configuration

```go
// Thread-safe read access
config := EVRMatchmakerConfigGet()
if config == nil {
    // Config not loaded yet
    return
}

// Use config values
timeout := config.Timeout.MatchmakingTimeoutSecs
enableSBMM := config.SBMM.EnableSBMM
```

## RPC Endpoints

The existing RPC endpoints continue to work:

### Get Configuration
```
RPC: matchmaker/config
Payload: {} (empty)
Returns: Full matchmaker configuration as JSON
```

### Update Configuration
```
RPC: matchmaker/config
Payload: { "config": { /* partial or full config */ } }
Returns: Updated configuration
```

The RPC response structure matches the new `EVRMatchmakerConfig` organization.

## Breaking Changes

**None** - This migration maintains full backward compatibility:
- Old struct fields remain in `ServiceSettingsData`
- Existing stored configs continue to load
- All code migrated to use new accessors
- RPC endpoints maintain same interface

## Testing

Unit tests verify:
- All 43 default values match specifications
- 14 validation rules correctly enforce constraints
- JSON serialization/deserialization works correctly
- Atomic pointer access is thread-safe
- Storage operations succeed

Run tests:
```bash
go test -short -vet=off ./server -run TestEVRMatchmakerConfig
```

## Rollback Plan

If issues arise:
1. Revert to previous commit
2. Old code paths remain functional
3. No data loss - old fields still in storage

## Future Cleanup

After sufficient migration period:
1. Remove `GlobalMatchmakingSettings` struct
2. Remove `SkillRatingSettings` struct  
3. Remove fields from `ServiceSettingsData`:
   - `Matchmaking GlobalMatchmakingSettings`
   - `SkillRating SkillRatingSettings`
4. Remove backward compatibility code in `FixDefaultServiceSettings`

Estimated timeline: 1-2 releases after this change deploys.

## Support

For questions or issues:
- Check existing code examples in migrated files
- Review test cases in `evr_matchmaker_config_test.go`
- Consult field mapping audit in `.sisyphus/plans/matchmaker-field-mapping.md`
