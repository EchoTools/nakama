# Fix Matchmaking Lockout Implementation

## TL;DR

> **Quick Summary**: Unify inconsistent lockout durations across 4 files (using 2/5/15 min for levels 1/2/3), fix the expiry scheduler's tier/penalty confusion, and implement the three unimplemented feature flags (SpawnLock, AutoReport, QueueBlocking).
> 
> **Deliverables**:
> - Single source of truth for lockout durations in `evr_earlyquit.go`
> - All 4 consumption points updated to use shared constants
> - Expiry scheduler fixed to use penalty level (0-3) instead of tier (1-4)
> - SpawnLock enforcement in `MatchJoinAttempt`
> - QueueBlocking enforcement in `lobbyFind`
> - AutoReport trigger at penalty level 3
> 
> **Estimated Effort**: Medium
> **Parallel Execution**: YES - 2 waves
> **Critical Path**: Task 1 → Tasks 2-5 → Task 6 → Task 7

---

## Context

### Original Request
Fix matchmaking lockout implementation inconsistencies in ~/src/nakama

### Interview Summary
**Key Discussions**:
- User chose custom lockout durations: Level 1=2min, Level 2=5min, Level 3=15min
- User wants feature flags (SpawnLock, AutoReport, QueueBlocking) implemented, not just fixed
- Expiry scheduler should be penalty-level based, not tier-based

**Research Findings**:
- 4 files define different lockout durations (60/120/240 vs 300/900/1800)
- Expiry scheduler at `evr_early_quit_message_trigger.go:320-326` uses tier-based map with non-existent Tiers 3-4
- Feature flags are sent to client (`EnableSpawnLock`, `EnableAutoReport`, `EnableQueueBlocking`) but never enforced server-side
- `MatchJoinAttempt` in `evr_match.go:222` is where spawn lock should be enforced
- No existing auto-report functionality for players (only server issue reporting exists)

### Metis Review
**Identified Gaps** (addressed):
- Need explicit user decision on durations → Resolved: 2/5/15 minutes
- Expiry scheduler tier vs penalty confusion → Will fix to penalty-level based
- Feature flags implementation scope → User chose to include implementation

---

## Work Objectives

### Core Objective
Create a consistent, single-source-of-truth implementation for matchmaking lockout durations and implement the three missing feature flag enforcements.

### Concrete Deliverables
- `server/evr_earlyquit.go` - New `EarlyQuitLockoutDurations` constant map
- `server/evr_lobby_find.go` - Updated to use shared constant + QueueBlocking
- `server/evr_match.go` - Updated notification durations + AutoReport trigger
- `server/evr/login_earlyquitconfig.go` - Updated `MMLockoutSec` values
- `server/evr_early_quit_message_trigger.go` - Fixed expiry scheduler (penalty-level based)
- `server/evr_match.go` - SpawnLock enforcement in `MatchJoinAttempt`

### Definition of Done
- [x] All lockout durations are: Level 0=0s, Level 1=120s, Level 2=300s, Level 3=900s
- [x] No hardcoded duration switch statements in evr_lobby_find.go
- [x] No hardcoded duration arrays like `[]int{0, 300, 900, 1800}`
- [x] Expiry scheduler uses penalty level (0-3), not tier (1-4)
- [x] SpawnLock prevents join when penalty > 0 and within lockout window
- [x] QueueBlocking rejects matchmaking request during lockout (not just delay)
- [x] AutoReport triggers at penalty level 3
- [x] `go test -short ./server/... -run ".*[Ee]arly[Qq]uit.*"` passes

### Must Have
- Single `EarlyQuitLockoutDurations` constant used everywhere
- Penalty-level based expiry scheduler
- SpawnLock enforcement in `MatchJoinAttempt`
- QueueBlocking enforcement in `lobbyFind`
- AutoReport at level 3

### Must NOT Have (Guardrails)
- Do NOT change penalty level incrementation/decrementation logic
- Do NOT modify tier assignment logic (Tier 1/Tier 2 thresholds)
- Do NOT add new tiers (Tier 3, Tier 4, etc.)
- Do NOT change the logout forgiveness mechanism
- Do NOT modify Discord notification templates (only durations in messages)
- Do NOT implement penalty decay (separate feature)

---

## Verification Strategy (MANDATORY)

### Test Decision
- **Infrastructure exists**: YES
- **User wants tests**: TDD
- **Framework**: `go test`

### Test Commands
```bash
# Run existing early quit tests
go test -short -v ./server/... -run ".*[Ee]arly[Qq]uit.*"

# Run full server tests (if time permits)
go test -short -v ./server/...
```

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 1 (Start Immediately):
└── Task 1: Create shared lockout duration constants

Wave 2 (After Wave 1):
├── Task 2: Update evr_lobby_find.go (delays + QueueBlocking)
├── Task 3: Update evr_match.go (notifications + AutoReport)
├── Task 4: Update login_earlyquitconfig.go (client config)
└── Task 5: Fix expiry scheduler (penalty-level based)

Wave 3 (After Wave 2):
└── Task 6: Implement SpawnLock in MatchJoinAttempt

Wave 4 (After Wave 3):
└── Task 7: Integration testing and verification
```

### Dependency Matrix

| Task | Depends On | Blocks | Can Parallelize With |
|------|------------|--------|---------------------|
| 1 | None | 2, 3, 4, 5 | None |
| 2 | 1 | 7 | 3, 4, 5 |
| 3 | 1 | 6, 7 | 2, 4, 5 |
| 4 | 1 | 7 | 2, 3, 5 |
| 5 | 1 | 7 | 2, 3, 4 |
| 6 | 3 | 7 | None |
| 7 | 2, 3, 4, 5, 6 | None | None |

---

## TODOs

- [x] 1. Create shared lockout duration constants in evr_earlyquit.go

  **What to do**:
  - Add new constant map `EarlyQuitLockoutDurations` with user's chosen values:
    - Level 0: 0 (no lockout)
    - Level 1: 2 minutes (120 seconds)
    - Level 2: 5 minutes (300 seconds)
    - Level 3: 15 minutes (900 seconds)
  - Add helper function `GetLockoutDuration(penaltyLevel int) time.Duration`
  - Add helper function `GetLockoutDurationSeconds(penaltyLevel int) int32`

  **Must NOT do**:
  - Do NOT modify existing penalty level constants (MinEarlyQuitPenaltyLevel, MaxEarlyQuitPenaltyLevel)
  - Do NOT change tier constants (MatchmakingTier1, MatchmakingTier2)

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Single file edit, adding constants and helper functions
  - **Skills**: `[]`
    - No special skills needed for constant definition

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 1 (solo)
  - **Blocks**: Tasks 2, 3, 4, 5
  - **Blocked By**: None

  **References**:
  - `server/evr_earlyquit.go:13-28` - Existing constants section (add new map after line 28)
  - `server/evr_earlyquit.go:45-51` - Pattern for helper function (follow CalculatePlayerReliabilityRating style)

  **Acceptance Criteria**:
  - [ ] `grep "EarlyQuitLockoutDurations" server/evr_earlyquit.go` returns the new constant
  - [ ] `grep "GetLockoutDuration" server/evr_earlyquit.go` returns both helper functions
  - [ ] `go build ./server/...` succeeds

  **Commit**: YES
  - Message: `feat(earlyquit): add shared lockout duration constants`
  - Files: `server/evr_earlyquit.go`
  - Pre-commit: `go build ./server/...`

---

- [x] 2. Update evr_lobby_find.go to use shared constants and implement QueueBlocking

  **What to do**:
  - Replace hardcoded switch statement (lines 136-143) with call to `GetLockoutDuration()`
  - Implement QueueBlocking: check if within lockout window, return error instead of delay
  - When blocked, return `NewLobbyError()` with remaining lockout time
  - Keep delay as fallback if QueueBlocking feature flag is disabled

  **Must NOT do**:
  - Do NOT change the mode check (ModeArenaPublic only)
  - Do NOT modify the Discord notification logic
  - Do NOT change the audit log message format

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Single file edit with straightforward logic change
  - **Skills**: `[]`

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Tasks 3, 4, 5)
  - **Blocks**: Task 7
  - **Blocked By**: Task 1

  **References**:
  - `server/evr_lobby_find.go:126-164` - Current hardcoded duration switch statement
  - `server/evr_lobby_find.go:130` - Mode check pattern to preserve
  - `server/evr/login_earlyquitfeatureflags.go:15` - `EnableQueueBlocking` flag definition
  - `server/evr_lobby_errors.go` - `NewLobbyError` pattern for blocking response

  **Acceptance Criteria**:
  - [ ] `grep -E "case [123]:" server/evr_lobby_find.go` returns no matches (no hardcoded cases)
  - [ ] `grep "GetLockoutDuration" server/evr_lobby_find.go` returns usage of shared function
  - [ ] `grep "EnableQueueBlocking" server/evr_lobby_find.go` returns feature flag check
  - [ ] `go build ./server/...` succeeds

  **Commit**: NO (groups with Task 7)

---

- [x] 3. Update evr_match.go notification durations and add AutoReport trigger

  **What to do**:
  - Replace hardcoded `lockoutDurations := []int{0, 300, 900, 1800}` (line 703) with call to `GetLockoutDurationSeconds()`
  - Add AutoReport trigger: when penalty level reaches 3, call auto-report function
  - Create placeholder auto-report function that logs for now (no existing report system for players)

  **Must NOT do**:
  - Do NOT change the early quit detection logic
  - Do NOT modify tier change logic
  - Do NOT change the logout forgiveness goroutine trigger

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Single file edit, straightforward logic addition
  - **Skills**: `[]`

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Tasks 2, 4, 5)
  - **Blocks**: Task 6, Task 7
  - **Blocked By**: Task 1

  **References**:
  - `server/evr_match.go:702-710` - Current hardcoded lockout durations array
  - `server/evr_match.go:694-746` - Full early quit handling block for context
  - `server/evr/login_earlyquitconfig.go:114` - AutoReport=1 at penalty level 3
  - `server/evr/login_earlyquitfeatureflags.go:13` - `EnableAutoReport` flag

  **Acceptance Criteria**:
  - [ ] `grep "lockoutDurations := \[\]int" server/evr_match.go` returns no matches
  - [ ] `grep "GetLockoutDurationSeconds" server/evr_match.go` returns usage
  - [ ] `grep "AutoReport\|autoReport" server/evr_match.go` returns trigger logic
  - [ ] `go build ./server/...` succeeds

  **Commit**: NO (groups with Task 7)

---

- [x] 4. Update login_earlyquitconfig.go client config values

  **What to do**:
  - Update `MMLockoutSec` values in `DefaultEarlyQuitServiceConfig()`:
    - Level 0: 0 (unchanged)
    - Level 1: 120 (was 300)
    - Level 2: 300 (was 900)
    - Level 3: 900 (was 1800)

  **Must NOT do**:
  - Do NOT change SpawnLock, AutoReport, or CNVPEReactivated values
  - Do NOT modify SteadyPlayerLevels config
  - Do NOT change MinEarlyQuits/MaxEarlyQuits thresholds

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Simple value changes in one file
  - **Skills**: `[]`

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Tasks 2, 3, 5)
  - **Blocks**: Task 7
  - **Blocked By**: Task 1

  **References**:
  - `server/evr/login_earlyquitconfig.go:78-117` - `DefaultEarlyQuitServiceConfig()` function
  - `server/evr/login_earlyquitconfig.go:33` - `MMLockoutSec` field definition

  **Acceptance Criteria**:
  - [ ] `grep "MMLockoutSec.*120" server/evr/login_earlyquitconfig.go` returns Level 1
  - [ ] `grep "MMLockoutSec.*300" server/evr/login_earlyquitconfig.go` returns Level 2
  - [ ] `grep "MMLockoutSec.*900" server/evr/login_earlyquitconfig.go` returns Level 3
  - [ ] `go build ./server/...` succeeds

  **Commit**: NO (groups with Task 7)

---

- [x] 5. Fix expiry scheduler to use penalty level instead of tier

  **What to do**:
  - Replace tier-based `lockoutDurations` map (lines 320-326) with penalty-level based lookup
  - Change `lockoutDurations[eqConfig.MatchmakingTier]` to use `penaltyLevel` instead
  - Use `GetLockoutDurationSeconds()` from shared constants
  - Fix the comparison to use `penaltyLevel` (0-3) not tier (1-4)

  **Must NOT do**:
  - Do NOT change the scheduler interval (30 seconds)
  - Do NOT modify the session iteration logic
  - Do NOT change the notification sending mechanism

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Single file edit with logic correction
  - **Skills**: `[]`

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Tasks 2, 3, 4)
  - **Blocks**: Task 7
  - **Blocked By**: Task 1

  **References**:
  - `server/evr_early_quit_message_trigger.go:317-380` - `checkAndNotifyExpiredPenalties()` function
  - `server/evr_early_quit_message_trigger.go:320-326` - Current tier-based map (WRONG)
  - `server/evr_early_quit_message_trigger.go:350` - `penaltyLevel := eqConfig.EarlyQuitPenaltyLevel` (already available!)
  - `server/evr_early_quit_message_trigger.go:356` - Bug: uses `eqConfig.MatchmakingTier` instead of `penaltyLevel`

  **Acceptance Criteria**:
  - [ ] Expiry logic uses `penaltyLevel` (0-3) not `MatchmakingTier` (1-4)
  - [ ] `grep "GetLockoutDuration" server/evr_early_quit_message_trigger.go` returns usage
  - [ ] `go build ./server/...` succeeds

  **Commit**: NO (groups with Task 7)

---

- [x] 6. Implement SpawnLock enforcement in MatchJoinAttempt

  **What to do**:
  - In `MatchJoinAttempt` function, check if player has active lockout
  - If `EnableSpawnLock` feature flag is true AND player has penalty > 0 AND within lockout window:
    - Reject join attempt with reason "Spawn locked due to early quit penalty"
  - Load early quit config for joining player using existing storage patterns
  - Calculate if within lockout window: `time.Since(LastEarlyQuitTime) < GetLockoutDuration(penaltyLevel)`

  **Must NOT do**:
  - Do NOT block spectators or moderators
  - Do NOT apply to private matches
  - Do NOT modify the existing join rejection logic for other reasons

  **Recommended Agent Profile**:
  - **Category**: `unspecified-low`
    - Reason: Requires understanding of match flow but straightforward addition
  - **Skills**: `[]`

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 3 (solo)
  - **Blocks**: Task 7
  - **Blocked By**: Task 3 (needs GetLockoutDuration available)

  **References**:
  - `server/evr_match.go:222-350` - `MatchJoinAttempt` function
  - `server/evr_match.go:227-240` - Early return pattern for unassigned lobbies
  - `server/evr_earlyquit.go:59-69` - `StorableMeta()` pattern for loading config
  - `server/evr/login_earlyquitfeatureflags.go:12` - `EnableSpawnLock` flag
  - `server/evr_match.go:695-746` - Early quit handling (shows how to load eqconfig)

  **Acceptance Criteria**:
  - [ ] `grep "EnableSpawnLock\|SpawnLock" server/evr_match.go` returns enforcement check
  - [ ] `grep "Spawn locked" server/evr_match.go` returns rejection reason
  - [ ] `go build ./server/...` succeeds
  - [ ] `go test -short -v ./server/... -run ".*MatchJoinAttempt.*"` passes

  **Commit**: NO (groups with Task 7)

---

- [x] 7. Integration testing and final verification

  **What to do**:
  - Run all early quit related tests
  - Verify no regressions in existing tests
  - Manually verify lockout duration consistency with grep commands
  - Build and verify no compilation errors

  **Must NOT do**:
  - Do NOT skip any test verification
  - Do NOT commit if tests fail

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Test execution and verification
  - **Skills**: `["git-master"]`
    - For final commit with proper message

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 4 (final)
  - **Blocks**: None
  - **Blocked By**: Tasks 2, 3, 4, 5, 6

  **References**:
  - `server/evr_earlyquit_test.go` - Unit tests for early quit
  - `server/evr_earlyquit_integration_test.go` - Integration tests
  - `server/evr_match_test.go:578` - `TestEvrMatch_MatchJoinAttempt` test

  **Acceptance Criteria**:
  - [ ] `go test -short -v ./server/... -run ".*[Ee]arly[Qq]uit.*"` passes
  - [ ] `go test -short -v ./server/... -run ".*MatchJoinAttempt.*"` passes
  - [ ] `go build ./server/...` succeeds
  - [ ] No hardcoded duration values remain in modified files

  **Commit**: YES
  - Message: `fix(earlyquit): unify lockout durations and implement feature flags

- Consolidate lockout durations to shared constants (2/5/15 min)
- Fix expiry scheduler to use penalty level instead of tier
- Implement SpawnLock enforcement in MatchJoinAttempt
- Implement QueueBlocking in lobbyFind
- Add AutoReport trigger at penalty level 3`
  - Files: `server/evr_earlyquit.go`, `server/evr_lobby_find.go`, `server/evr_match.go`, `server/evr/login_earlyquitconfig.go`, `server/evr_early_quit_message_trigger.go`
  - Pre-commit: `go test -short ./server/... -run ".*[Ee]arly[Qq]uit.*"`

---

## Commit Strategy

| After Task | Message | Files | Verification |
|------------|---------|-------|--------------|
| 1 | `feat(earlyquit): add shared lockout duration constants` | evr_earlyquit.go | go build |
| 7 | `fix(earlyquit): unify lockout durations and implement feature flags` | all 5 files | go test |

---

## Success Criteria

### Verification Commands
```bash
# Build succeeds
go build ./server/...

# All early quit tests pass
go test -short -v ./server/... -run ".*[Ee]arly[Qq]uit.*"

# Shared constants are used
grep -rn "GetLockoutDuration" server/
# Expected: matches in evr_lobby_find.go, evr_match.go, evr_early_quit_message_trigger.go
```

### Final Checklist
- [x] All "Must Have" present
- [x] All "Must NOT Have" absent
- [x] All tests pass
- [x] Lockout durations consistent: 0/120/300/900 seconds for levels 0/1/2/3
