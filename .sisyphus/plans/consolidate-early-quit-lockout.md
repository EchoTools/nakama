# Consolidate Early Quit & Lockout System

## TL;DR

> **Quick Summary**: Combine `feat/early-quit-notifications` (PR #270) and `feat/early-quit-lockout-system` (PR #277) into a single cohesive PR, addressing all review feedback and fixing critical bugs.
> 
> **Deliverables**:
> - Single unified branch rebased on main
> - All Copilot review comments addressed
> - New PR with comprehensive description
> - Old PRs #270 and #277 closed
> 
> **Estimated Effort**: Medium
> **Parallel Execution**: NO - sequential (git operations, then fixes, then PR)
> **Critical Path**: Rebase → Bug fixes → Missing features → Cleanup → PR creation

---

## Context

### Original Request
Combine all early quit related branches from 2026-01-14 to now into a single cohesive branch and create a new PR, addressing review comments from existing PRs.

### Branches Involved
| Branch | PR | Status | Key Changes |
|--------|-----|--------|-------------|
| `enhance/early-quit-storage-format` | #263 | MERGED | Already in main - detailed quit history tracking |
| `feat/early-quit-notifications` | #270 | OPEN | SNS messages, feature flags, expiry scheduler |
| `feat/early-quit-lockout-system` | #277 | OPEN | Builds on #270, adds lockout constants, spawn lock |

### Review Comments to Address

**From PR #277 (8 comments):**
1. `evr_pipeline_login.go:917` - ClientProfile.EarlyQuitFeatures not populated after removing test stub
2. `evr_match.go:725` - Comment mismatch on lockout durations
3. `evr_early_quit_message_trigger.go:285-289` - No tests for scheduler/notification behavior
4. `evr_match.go:741-744` - Tier degradation logic inverted (`oldTier > newTier` wrong)
5. `evr_earlyquit.go:310` - Same tier degradation bug
6. `evr_early_quit_message_trigger.go:358-362` - Expiry notification spam (no tracking)
7. `evr_pipeline.go:244-248` - No shutdown hook for scheduler
8. `evr_match.go:259` - SpawnLock doesn't check ServiceSettings.EnableEarlyQuitPenalty

**From PR #270 (6 comments):**
1. Same expiry notification spam issue
2. Same tier degradation logic bug (multiple locations)
3. Same comment/array mismatch for lockout durations
4. Lockout duration lookup uses wrong field (MatchmakingTier vs EarlyQuitPenaltyLevel)
5. Same shutdown hook issue

---

## Work Objectives

### Core Objective
Create a single, bug-free PR that unifies all early quit penalty and lockout functionality.

### Concrete Deliverables
- New branch `feat/early-quit-unified` rebased on latest main
- All critical bugs fixed
- All review comments addressed
- New PR created with comprehensive description
- PRs #270 and #277 closed with reference to new PR

### Definition of Done
- [ ] `go build ./...` succeeds
- [ ] New PR passes CI checks
- [ ] All 8 unique review issues addressed
- [ ] Old PRs closed

### Must Have
- Fix tier degradation logic (critical bug)
- Fix expiry notification spam (critical bug)
- Populate EarlyQuitFeatures on login
- Rebase on latest main

### Must NOT Have (Guardrails)
- DO NOT force push to main
- DO NOT delete the original branches (leave for reference)
- DO NOT change lockout duration values (just consolidate definitions)
- DO NOT add new features beyond addressing review comments

---

## Verification Strategy

### Test Decision
- **Infrastructure exists**: YES (Go build, existing tests)
- **User wants tests**: Manual verification (no new unit tests requested)
- **Framework**: `go build`, `go test`

### Verification Commands
```bash
# Build verification
go build ./...

# Run existing tests
go test ./server/... -v -short

# Verify no duplicate lockout definitions
grep -r "lockoutDurations\|LockoutDuration" server/*.go
```

---

## Execution Strategy

### Sequential Execution (No Parallelization)
Git operations must be sequential. Fixes depend on successful rebase.

```
Task 1: Create unified branch from feat/early-quit-lockout-system
    ↓
Task 2: Rebase on latest main
    ↓
Task 3: Fix tier degradation logic (critical)
    ↓
Task 4: Fix expiry notification spam (critical)
    ↓
Task 5: Populate EarlyQuitFeatures on login
    ↓
Task 6: Fix SpawnLock service settings check
    ↓
Task 7: Fix comment/code mismatches
    ↓
Task 8: Wire scheduler shutdown hook
    ↓
Task 9: Verify build and tests
    ↓
Task 10: Create new PR
    ↓
Task 11: Close old PRs
```

---

## TODOs

- [x] 1. Create unified branch from feat/early-quit-lockout-system

  **What to do**:
  - Create new branch `feat/early-quit-unified` from `feat/early-quit-lockout-system`
  - This branch already contains all changes from #270 plus lockout improvements

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: [`git-master`]

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Blocks**: All subsequent tasks
  - **Blocked By**: None

  **References**:
  - `feat/early-quit-lockout-system` branch - source branch with most complete changes

  **Acceptance Criteria**:
  ```bash
  git checkout -b feat/early-quit-unified feat/early-quit-lockout-system
  git branch | grep "feat/early-quit-unified"
  # Assert: Branch exists
  ```

  **Commit**: NO

---

- [x] 2. Rebase unified branch on latest main

  **What to do**:
  - Rebase `feat/early-quit-unified` onto latest `main`
  - Resolve any conflicts (PR #263 storage format is already in main)
  - Verify the rebase succeeded

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: [`git-master`]

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Blocks**: Tasks 3-11
  - **Blocked By**: Task 1

  **References**:
  - Main branch includes PR #263 (storage format) merged on 2026-01-15
  - Common ancestor: `4d5a569205e5e07f23fa6821aa5cc80e7db29b90`

  **Acceptance Criteria**:
  ```bash
  git fetch origin main
  git rebase origin/main
  # Assert: Rebase completes (or conflicts resolved)
  git log --oneline -3
  # Assert: Shows commits from lockout branch on top of main
  ```

  **Commit**: NO (rebase only)

---

- [x] 3. Fix tier degradation logic (CRITICAL BUG)

  **What to do**:
  - Fix inverted tier degradation check in multiple locations
  - Tier1=1 (good), Tier2=2 (penalty), so degradation is `newTier > oldTier`
  - Locations to fix:
    1. `server/evr_match.go` - `SendTierChangeNotification` call
    2. `server/evr_earlyquit.go` - `SendTierChangeNotification` call

  **Must NOT do**:
  - Do not change the SendTierChangeNotification function signature
  - Do not change tier constant values

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Blocks**: Task 9 (verification)
  - **Blocked By**: Task 2

  **References**:
  - `server/evr_match.go:744` - Current: `oldTier > newTier`, should be `newTier > oldTier`
  - `server/evr_earlyquit.go:310` - Same fix needed
  - `server/evr_earlyquit.go:MatchmakingTier1`, `MatchmakingTier2` - Tier constants (1 and 2)

  **Acceptance Criteria**:
  ```bash
  grep -n "oldTier > newTier" server/evr_match.go server/evr_earlyquit.go
  # Assert: No matches (bug pattern removed)
  
  grep -n "newTier > oldTier" server/evr_match.go server/evr_earlyquit.go
  # Assert: 2 matches (correct pattern)
  
  go build ./server/...
  # Assert: Build succeeds
  ```

  **Commit**: YES
  - Message: `fix(earlyquit): correct tier degradation logic - newTier > oldTier indicates degradation`
  - Files: `server/evr_match.go`, `server/evr_earlyquit.go`

---

- [x] 4. Fix expiry notification spam (CRITICAL BUG)

  **What to do**:
  - Add tracking to prevent repeated expiry notifications every 30 seconds
  - Add `LastExpiryNotificationSent time.Time` field to `EarlyQuitConfig` struct
  - Update `checkAndNotifyExpiredPenalties` to:
    1. Check if notification was already sent for current penalty
    2. Only send if not already notified
    3. Update the tracking timestamp after sending
  - Save the updated config after sending notification

  **Must NOT do**:
  - Do not change the 30-second scheduler interval
  - Do not change notification message content

  **Recommended Agent Profile**:
  - **Category**: `unspecified-low`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Blocks**: Task 9
  - **Blocked By**: Task 2

  **References**:
  - `server/evr_earlyquit.go:EarlyQuitConfig` - Struct to add field to
  - `server/evr_early_quit_message_trigger.go:checkAndNotifyExpiredPenalties` - Function to fix
  - `server/evr_earlyquit.go:GetLockoutDuration` - Use this for duration lookup (not a local map)

  **Acceptance Criteria**:
  ```bash
  grep -n "LastExpiryNotificationSent" server/evr_earlyquit.go
  # Assert: Field exists in struct
  
  grep -n "LastExpiryNotificationSent" server/evr_early_quit_message_trigger.go
  # Assert: Field is checked before sending notification
  
  go build ./server/...
  # Assert: Build succeeds
  ```

  **Commit**: YES
  - Message: `fix(earlyquit): prevent repeated expiry notifications with tracking timestamp`
  - Files: `server/evr_earlyquit.go`, `server/evr_early_quit_message_trigger.go`

---

- [x] 5. Populate EarlyQuitFeatures on login

  **What to do**:
  - In `evr_pipeline_login.go`, after the test stub was removed, populate `clientProfile.EarlyQuitFeatures` from actual stored `EarlyQuitConfig`
  - Calculate penalty end time from `LastEarlyQuitTime + GetLockoutDuration(level)`
  - Only set if penalty is still active (current time < penalty end time)

  **Must NOT do**:
  - Do not add test/fake data
  - Do not change the EarlyQuitFeatures struct

  **Recommended Agent Profile**:
  - **Category**: `unspecified-low`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Blocks**: Task 9
  - **Blocked By**: Task 2

  **References**:
  - `server/evr_pipeline_login.go:loggedInUserProfileRequest` - Function where stub was removed (around line 917)
  - `server/evr_earlyquit.go:GetLockoutDuration` - Helper to calculate lockout duration
  - `server/evr/core_account.go:EarlyQuitFeatures` - Target struct to populate
  - PR #277 review comment with suggested code pattern

  **Acceptance Criteria**:
  ```bash
  grep -A20 "clientProfile.EarlyQuitFeatures" server/evr_pipeline_login.go
  # Assert: Shows population logic from eqConfig/params
  
  go build ./server/...
  # Assert: Build succeeds
  ```

  **Commit**: YES
  - Message: `fix(earlyquit): populate EarlyQuitFeatures from stored config on login`
  - Files: `server/evr_pipeline_login.go`

---

- [x] 6. Fix SpawnLock service settings check

  **What to do**:
  - In `evr_match.go` MatchJoinAttempt, add check for `ServiceSettings().Matchmaking.EnableEarlyQuitPenalty`
  - SpawnLock enforcement should be gated by BOTH feature flag AND service setting
  - Follow the pattern used elsewhere in the codebase

  **Must NOT do**:
  - Do not remove the existing featureFlags.EnableSpawnLock check
  - Do not change SpawnLock behavior when enabled

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Blocks**: Task 9
  - **Blocked By**: Task 2

  **References**:
  - `server/evr_match.go:259` - Current SpawnLock check location
  - `server/evr_lobby_find.go` - Example of checking `serviceSettings.Matchmaking.EnableEarlyQuitPenalty`
  - PR #277 review comment with suggested code pattern

  **Acceptance Criteria**:
  ```bash
  grep -B5 -A10 "EnableSpawnLock" server/evr_match.go
  # Assert: Shows EnableEarlyQuitPenalty check in condition
  
  go build ./server/...
  # Assert: Build succeeds
  ```

  **Commit**: YES
  - Message: `fix(earlyquit): gate SpawnLock on EnableEarlyQuitPenalty service setting`
  - Files: `server/evr_match.go`

---

- [ ] 7. Fix comment/code mismatches for lockout durations

  **What to do**:
  - Update misleading comments that don't match actual lockout duration values
  - Actual values in `EarlyQuitLockoutDurations`: 0s, 2m, 5m, 15m (levels 0-3)
  - Remove any hardcoded duration arrays/maps that duplicate the shared constant
  - Ensure all code uses `GetLockoutDuration()` or `GetLockoutDurationSeconds()` helpers

  **Must NOT do**:
  - Do not change actual lockout duration values
  - Do not change the shared `EarlyQuitLockoutDurations` map

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Blocks**: Task 9
  - **Blocked By**: Task 2

  **References**:
  - `server/evr_earlyquit.go:EarlyQuitLockoutDurations` - Source of truth for durations
  - `server/evr_match.go:725` - Comment says "(5min, 15min, 30min, 60min)" but values are different
  - `server/evr_early_quit_message_trigger.go:checkAndNotifyExpiredPenalties` - Should use shared helper, not local map

  **Acceptance Criteria**:
  ```bash
  # No duplicate lockout duration definitions
  grep -n "lockoutDurations :=" server/*.go
  # Assert: No matches (no local definitions)
  
  # Comments match actual values
  grep -n "5min, 15min, 30min, 60min" server/*.go
  # Assert: No matches (incorrect comment removed)
  
  go build ./server/...
  # Assert: Build succeeds
  ```

  **Commit**: YES
  - Message: `fix(earlyquit): correct lockout duration comments and use shared helpers`
  - Files: `server/evr_match.go`, `server/evr_early_quit_message_trigger.go`

---

- [ ] 8. Wire scheduler shutdown hook

  **What to do**:
  - Store reference to `earlyQuitMessageTrigger` in `EvrPipeline` struct
  - Call `earlyQuitMessageTrigger.Stop()` in pipeline shutdown
  - Update `StartLockoutExpiryScheduler` to also select on context cancellation

  **Must NOT do**:
  - Do not change the 30-second check interval
  - Do not remove the stopCh mechanism

  **Recommended Agent Profile**:
  - **Category**: `unspecified-low`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Blocks**: Task 9
  - **Blocked By**: Task 2

  **References**:
  - `server/evr_pipeline.go:EvrPipeline` - Struct to add field to
  - `server/evr_pipeline.go:NewEvrPipeline` - Where trigger is created
  - `server/evr_pipeline.go:Stop` or shutdown logic - Where to call Stop()
  - `server/evr_early_quit_message_trigger.go:StartLockoutExpiryScheduler` - Add ctx.Done() select

  **Acceptance Criteria**:
  ```bash
  grep -n "earlyQuitMessageTrigger" server/evr_pipeline.go
  # Assert: Field exists in struct and Stop() is called
  
  grep -A10 "case <-t.stopCh" server/evr_early_quit_message_trigger.go
  # Assert: Also shows case <-ctx.Done() handling
  
  go build ./server/...
  # Assert: Build succeeds
  ```

  **Commit**: YES
  - Message: `fix(earlyquit): wire scheduler shutdown to prevent goroutine leak`
  - Files: `server/evr_pipeline.go`, `server/evr_early_quit_message_trigger.go`

---

- [ ] 9. Verify build and run tests

  **What to do**:
  - Run full build
  - Run existing tests
  - Verify no regressions

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Blocks**: Task 10
  - **Blocked By**: Tasks 3-8

  **References**:
  - Project uses Go modules

  **Acceptance Criteria**:
  ```bash
  go build ./...
  # Assert: Exit code 0
  
  go test ./server/... -short 2>&1 | tail -20
  # Assert: No failures (or document known failures)
  ```

  **Commit**: NO

---

- [ ] 10. Create new unified PR

  **What to do**:
  - Push the `feat/early-quit-unified` branch to origin
  - Create PR with comprehensive description covering:
    - Summary of all changes
    - Credits to original PRs #270 and #277
    - List of review comments addressed
    - Files changed summary
  - Set base branch to `main`

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: [`git-master`]

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Blocks**: Task 11
  - **Blocked By**: Task 9

  **References**:
  - PR #270 title: "feat: add early quit penalty notifications via SNS messages"
  - PR #277 title: "feat: early quit penalty system with lockout duration management"

  **Acceptance Criteria**:
  ```bash
  git push -u origin feat/early-quit-unified
  # Assert: Push succeeds
  
  gh pr create --title "feat: unified early quit penalty and lockout system" --body "..."
  # Assert: PR created, URL returned
  ```

  **PR Body Template**:
  ```markdown
  ## Summary
  Unified early quit penalty system combining SNS notifications, lockout duration management, and spawn lock enforcement.

  This PR consolidates and supersedes:
  - #270 (feat: add early quit penalty notifications via SNS messages)
  - #277 (feat: early quit penalty system with lockout duration management)

  ## Changes
  - SNS notifications for penalty events (applied, expired, tier changes, lockouts)
  - Feature flags for penalty system configuration
  - Unified lockout duration constants and helpers
  - Spawn lock enforcement during active lockouts
  - Penalty expiry scheduler with proper shutdown handling

  ## Review Feedback Addressed
  - Fixed tier degradation logic (Tier1=1=good, Tier2=2=penalty)
  - Fixed expiry notification spam with tracking timestamp
  - Populated EarlyQuitFeatures on login from stored config
  - Added ServiceSettings check to SpawnLock enforcement
  - Corrected lockout duration comments
  - Wired scheduler shutdown hook

  ## Files Changed
  - `server/evr_early_quit_message_trigger.go` (new)
  - `server/evr/login_earlyquitupdatenotification.go` (new)
  - `server/evr/login_earlyquitfeatureflags.go` (new)
  - `server/evr_earlyquit.go`
  - `server/evr_match.go`
  - `server/evr_lobby_find.go`
  - `server/evr_pipeline.go`
  - `server/evr_pipeline_login.go`
  - `server/evr/login_earlyquitconfig.go`
  - `server/evr/core_account.go`
  - `server/evr/core_packet.go`
  - `server/evr/core_packet_types.go`
  ```

  **Commit**: NO (PR creation only)

---

- [ ] 11. Close old PRs with reference to new PR

  **What to do**:
  - Close PR #270 with comment referencing new unified PR
  - Close PR #277 with comment referencing new unified PR
  - Do NOT delete the branches (keep for reference)

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: [`git-master`]

  **Parallelization**:
  - **Can Run In Parallel**: YES (both can close simultaneously)
  - **Blocks**: None (final task)
  - **Blocked By**: Task 10

  **References**:
  - New PR URL from Task 10

  **Acceptance Criteria**:
  ```bash
  gh pr close 270 --comment "Superseded by #NEW_PR_NUMBER - unified early quit penalty and lockout system"
  # Assert: PR closed
  
  gh pr close 277 --comment "Superseded by #NEW_PR_NUMBER - unified early quit penalty and lockout system"
  # Assert: PR closed
  
  gh pr list --state open | grep -E "(270|277)"
  # Assert: No matches (both closed)
  ```

  **Commit**: NO

---

## Commit Strategy

| After Task | Message | Files |
|------------|---------|-------|
| 3 | `fix(earlyquit): correct tier degradation logic` | evr_match.go, evr_earlyquit.go |
| 4 | `fix(earlyquit): prevent repeated expiry notifications` | evr_earlyquit.go, evr_early_quit_message_trigger.go |
| 5 | `fix(earlyquit): populate EarlyQuitFeatures on login` | evr_pipeline_login.go |
| 6 | `fix(earlyquit): gate SpawnLock on service setting` | evr_match.go |
| 7 | `fix(earlyquit): correct lockout duration comments` | evr_match.go, evr_early_quit_message_trigger.go |
| 8 | `fix(earlyquit): wire scheduler shutdown hook` | evr_pipeline.go, evr_early_quit_message_trigger.go |

---

## Success Criteria

### Final Checklist
- [ ] New PR created and passing CI
- [ ] All 6 bug fixes committed
- [ ] PRs #270 and #277 closed
- [ ] `go build ./...` succeeds
- [ ] No duplicate lockout duration definitions
- [ ] Tier degradation logic correct (newTier > oldTier)
