# Fix PR Review Comments for #276 and #279

## TL;DR

> **Quick Summary**: Address Copilot review comments on two PRs - fix team size enforcement timing in PR #276 and latency filter logic bug in PR #279.
> 
> **Deliverables**:
> - PR #276: Move team size check before player state mutation
> - PR #276: Remove now-unnecessary cleanup code
> - PR #279: Fix empty latency history edge case
> - PR #279: Use boolean flag instead of -1 sentinel
> - PR #279: Add comment explaining 90ms threshold choice
> 
> **Estimated Effort**: Quick
> **Parallel Execution**: NO - sequential (different branches)
> **Critical Path**: PR #276 fixes → commit → PR #279 fixes → commit

---

## Context

### Original Request
Address review comments from Copilot on PR #276 and PR #279.

### PR #276: fix(evr): reject joins that exceed public arena team size limit
**URL**: https://github.com/EchoTools/nakama/pull/276
**Branch**: `fix/reject-arena-team-size-limit`

**Review Issues Identified**:
1. **Critical**: Reservations for party members not cleaned up on reject
2. **High**: Team size check happens AFTER player added to state (race condition)
3. **Medium**: Missing test coverage

### PR #279: fix(discord): restrict /create to servers within 90ms latency
**URL**: https://github.com/EchoTools/nakama/pull/279
**Branch**: (needs checkout)

**Review Issues Identified**:
1. **Critical**: Logic flaw when `extIPs` is empty - `minLatencyMs=-1` causes error condition to fail
2. **Medium**: Using `-1` as sentinel value is error-prone
3. **Low**: Hardcoded 90ms differs from existing `HighLatencyThresholdMs=100`

---

## Work Objectives

### Core Objective
Fix all critical and high-priority review comments to make PRs merge-ready.

### Concrete Deliverables
- `server/evr_match.go`: Move team size check before state mutation
- `server/evr_discord_appbot_handlers.go`: Fix latency filter logic

### Definition of Done
- [x] All review comments addressed
- [x] Code compiles: `go build ./...`
- [x] No new lint errors

### Must Have
- Fix the race condition in PR #276 by checking BEFORE mutation
- Fix the logic bug in PR #279 for empty latency history

### Must NOT Have (Guardrails)
- Do NOT add test files (review comment suggested but not required for merge)
- Do NOT change `HighLatencyThresholdMs` constant - just document why 90ms is different
- Do NOT refactor beyond what's needed to fix the review comments

---

## Verification Strategy

### Test Decision
- **Infrastructure exists**: YES (Go tests exist)
- **User wants tests**: NO (just fix review comments, tests optional)
- **Framework**: go test

### Automated Verification

```bash
# Build verification
go build ./server/...

# Run existing tests to ensure no regression
go test -short -vet=off ./server -run ".*Match.*" -timeout 60s
```

---

## Execution Strategy

### Sequential Execution (Different Branches)

```
Step 1: Fix PR #276 on branch fix/reject-arena-team-size-limit
├── Task 1: Refactor team size check
└── Task 2: Commit changes

Step 2: Fix PR #279 (checkout branch first)
├── Task 3: Fix latency filter logic
└── Task 4: Commit changes
```

---

## TODOs

- [x] 1. PR #276: Move team size enforcement before state mutation

  **What to do**:
  
  In `server/evr_match.go`, the current code adds the player to state first, then checks team size and cleans up if exceeded. This causes a race condition where invalid state is briefly broadcast.
  
  **The fix**: Move the team size check to BEFORE adding the player (around line 402-407, after role assignment but before adding reservations). This eliminates the need for cleanup code entirely.
  
  **Current code structure** (lines ~402-470):
  ```go
  // check the available slots
  if slots, err := state.OpenSlotsByRole(...) // line 402-407
  
  // Add reservations to the reservation map  // line 409-428
  for _, p := range meta.Reservations { ... }
  
  // Add the player                           // line 430-438
  state.presenceMap[sessionID] = meta.Presence
  ...
  
  // Update label (broadcasts state)          // line 440-442
  m.updateLabel(...)
  
  // Check team sizes (TOO LATE!)             // line 444-470
  if state.Mode == evr.ModeArenaPublic { ... }
  ```
  
  **Replace with**:
  ```go
  // check the available slots
  if slots, err := state.OpenSlotsByRole(meta.Presence.RoleAlignment); err != nil {
      return state, false, ErrJoinRejectReasonFailedToAssignTeam.Error()
  } else if slots < len(meta.Presences()) {
      return state, false, ErrJoinRejectReasonLobbyFull.Error()
  }

  // Enforce team size limits for public arena matches BEFORE adding player to state
  // This prevents broadcasting an invalid state that exceeds the team capacity
  if state.Mode == evr.ModeArenaPublic {
      maxTeamSize := DefaultPublicArenaTeamSize // Always 4 for public arena
      targetTeam := meta.Presence.RoleAlignment
      currentCount := state.RoleCount(targetTeam)

      // Check if adding this player would exceed the limit
      if currentCount >= maxTeamSize {
          logger.WithFields(map[string]interface{}{
              "blue":          state.RoleCount(evr.TeamBlue),
              "orange":        state.RoleCount(evr.TeamOrange),
              "target_team":   targetTeam,
              "max_team_size": maxTeamSize,
              "team_size":     state.TeamSize,
          }).Warn("Rejecting join: team size limit would be exceeded for public arena match.")

          return state, false, ErrJoinRejectReasonLobbyFull.Error()
      }
  }

  // Add reservations to the reservation map
  // ... rest of code unchanged ...
  ```
  
  **Then DELETE the old post-mutation check** (lines ~444-470):
  ```go
  // DELETE THIS ENTIRE BLOCK:
  // Enforce team size limits for public arena matches
  if state.Mode == evr.ModeArenaPublic {
      blueCount := state.RoleCount(evr.TeamBlue)
      orangeCount := state.RoleCount(evr.TeamOrange)
      maxTeamSize := DefaultPublicArenaTeamSize // Always 4 for public arena

      if blueCount > maxTeamSize || orangeCount > maxTeamSize {
          // ... all cleanup code ...
          return state, false, ErrJoinRejectReasonLobbyFull.Error()
      }
  }
  ```

  **Must NOT do**:
  - Do not add test files
  - Do not change the error message type

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: [`git-master`]
    - `git-master`: For proper commit handling

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Sequential
  - **Blocks**: Task 2
  - **Blocked By**: None

  **References**:
  - `server/evr_match.go:402-470` - MatchJoinAttempt function, team size enforcement
  - `server/evr_match.go:448` - `DefaultPublicArenaTeamSize` constant usage

  **Acceptance Criteria**:
  ```bash
  # Build succeeds
  go build ./server/...
  # Exit code 0

  # The old post-mutation check block should NOT exist
  grep -n "Rejecting join: team size limit exceeded" server/evr_match.go
  # Should find only the NEW pre-check (around line 410-420), NOT the old post-check

  # The new pre-check should exist before "Add reservations"
  grep -B5 "Add reservations to the reservation map" server/evr_match.go | grep -q "team size limit would be exceeded"
  # Exit code 0 (pattern found)
  ```

  **Commit**: YES
  - Message: `fix(evr): move team size check before state mutation to prevent race condition`
  - Files: `server/evr_match.go`
  - Pre-commit: `go build ./server/...`

---

- [x] 2. PR #279: Checkout branch

  **What to do**:
  ```bash
  gh pr checkout 279
  ```

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: [`git-master`]

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Blocked By**: Task 1 commit

  **Acceptance Criteria**:
  ```bash
  git branch --show-current
  # Should show the PR #279 branch name
  ```

  **Commit**: NO

---

- [x] 3. PR #279: Fix latency filter logic for empty history

  **What to do**:
  
  In `server/evr_discord_appbot_handlers.go`, fix the latency filtering logic around lines 774-790.
  
  **Current buggy code**:
  ```go
  // Filter servers to only those with RTT <= 90ms
  const maxLatencyMs = 90
  filteredIPs := make(map[string]int)
  minLatencyMs := -1

  for ip, latency := range extIPs {
      if minLatencyMs == -1 || latency < minLatencyMs {
          minLatencyMs = latency
      }
      if latency <= maxLatencyMs {
          filteredIPs[ip] = latency
      }
  }

  // If no servers within 90ms, return error with best available latency
  if len(filteredIPs) == 0 && minLatencyMs > maxLatencyMs {
      return nil, 0, fmt.Errorf("no servers within 90ms of your location. Your best server has %dms latency", minLatencyMs)
  }
  ```
  
  **Problems**:
  1. When `extIPs` is empty, `minLatencyMs` stays `-1`
  2. Condition `-1 > 90` is false, so error not returned
  3. Empty `filteredIPs` passed to allocator, causing confusing downstream error
  
  **Replace with**:
  ```go
  // Filter servers to only those with RTT <= 90ms
  // Note: 90ms is stricter than the 100ms HighLatencyThresholdMs used in matchmaking
  // because /create is a manual server selection that should prioritize low latency
  const maxLatencyMs = 90
  filteredIPs := make(map[string]int)
  minLatencyMs := 0
  hasLatencyData := false

  for ip, latency := range extIPs {
      if !hasLatencyData || latency < minLatencyMs {
          minLatencyMs = latency
          hasLatencyData = true
      }
      if latency <= maxLatencyMs {
          filteredIPs[ip] = latency
      }
  }

  // If no servers within 90ms, return appropriate error
  if len(filteredIPs) == 0 {
      if !hasLatencyData {
          return nil, 0, fmt.Errorf("no latency history exists for your account; play some matches first to establish server latency data")
      }
      return nil, 0, fmt.Errorf("no servers within 90ms of your location. Your best server has %dms latency", minLatencyMs)
  }
  ```

  **Must NOT do**:
  - Do not change the 90ms threshold value
  - Do not modify HighLatencyThresholdMs constant

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: [`git-master`]

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Blocked By**: Task 2

  **References**:
  - `server/evr_discord_appbot_handlers.go:774-790` - handleCreateMatch latency filtering
  - `server/evr_lobby_builder.go:32` - HighLatencyThresholdMs constant (100ms) for context

  **Acceptance Criteria**:
  ```bash
  # Build succeeds
  go build ./server/...
  # Exit code 0

  # Boolean flag pattern should exist
  grep -q "hasLatencyData" server/evr_discord_appbot_handlers.go
  # Exit code 0

  # Old -1 sentinel pattern should NOT exist in this function
  grep "minLatencyMs := -1" server/evr_discord_appbot_handlers.go
  # Exit code 1 (not found)

  # New error message for no latency history should exist
  grep -q "no latency history exists" server/evr_discord_appbot_handlers.go
  # Exit code 0
  ```

  **Commit**: YES
  - Message: `fix(discord): handle empty latency history in /create command`
  - Files: `server/evr_discord_appbot_handlers.go`
  - Pre-commit: `go build ./server/...`

---

## Commit Strategy

| After Task | Message | Files | Verification |
|------------|---------|-------|--------------|
| 1 | `fix(evr): move team size check before state mutation to prevent race condition` | server/evr_match.go | go build ./server/... |
| 3 | `fix(discord): handle empty latency history in /create command` | server/evr_discord_appbot_handlers.go | go build ./server/... |

---

## Success Criteria

### Verification Commands
```bash
# Both files compile
go build ./server/...

# PR #276 branch - team size check moved before state mutation
git diff HEAD~1 server/evr_match.go | grep -A5 "team size limit would be exceeded"

# PR #279 branch - latency logic fixed
git diff HEAD~1 server/evr_discord_appbot_handlers.go | grep -A3 "hasLatencyData"
```

### Final Checklist
- [x] PR #276: Team size check happens before player added to state
- [x] PR #276: No cleanup code needed (check happens before mutation)
- [x] PR #279: Empty latency history returns clear error message
- [x] PR #279: Uses boolean flag instead of -1 sentinel
- [x] PR #279: Comment explains why 90ms differs from 100ms threshold
- [x] Both PRs compile successfully
