# Draft: Early Quit System Verification and Configuration

## User's Request
- Verify that the early quit system is working as intended
- Update documentation
- Add global settings toggle to disable entire early quit system (including lockout from matchmaking)

## Research Findings

### Early Quit System Architecture
**Key Files:**
- `server/evr_match.go` (lines 649-775) - Early quit detection in MatchLeave handler
- `server/evr_earlyquit.go` - Penalty levels, tiers, lockout durations
- `server/evr_lobby_find.go` (lines 129-173) - Matchmaking queue blocking enforcement
- `server/evr_global_settings.go` (lines 97-101) - Configuration options
- `docs/EARLY_QUIT.md` - Complete system documentation

**Current Configuration:**
- `EnableEarlyQuitPenalty` (bool) - Master toggle for entire system
- `SilentEarlyQuitSystem` (bool) - Disable Discord notifications
- `EarlyQuitTier1Threshold` (*int32) - Penalty threshold for good standing (default: 0)
- Feature flags: `EnableSpawnLock`, `EnableQueueBlocking`

**Current Behavior:**
- Detection: Player leaves public arena match before it ends
- Penalty: Level increases by 1 (max: 3), tiers updated if threshold crossed
- Lockouts: 
  - Level 1: 2 minutes
  - Level 2: 5 minutes  
  - Level 3: 15 minutes
- Queue Blocking: Checked in `evr_lobby_find.go` - prevents matchmaking during lockout
- Spawn Lock: Checked in `evr_match.go` - prevents rejoining matches during lockout
- Recovery: Complete a match to reduce penalty by 1, or logout completely within 5 min grace period

### Global Settings System Architecture
**Key Files:**
- `server/evr_global_settings.go` - `ServiceSettingsData` struct with all settings
- Settings stored in Nakama storage: collection "Global", key "settings"
- Thread-safe access via `ServiceSettings()` atomic pointer
- 30-second refresh cycle

**Adding New Toggle:**
- Location: `GlobalMatchmakingSettings` struct (nested in `ServiceSettingsData`)
- Existing master toggle: `EnableEarlyQuitPenalty` already exists!
- Access pattern: `ServiceSettings().Matchmaking.EnableEarlyQuitPenalty`

## Open Questions
- What specific behaviors should we verify?
- What documentation needs updating?
- When disabled, should existing penalties be cleared immediately or just stop new penalties?
