# Fix Public Arena Team Size Limit

## TL;DR

> **Quick Summary**: Fix a bug where public arena matches allow 5 players on a team instead of the maximum 4. The post-join team size check logs an error but doesn't reject the join.
> 
> **Deliverables**:
> - Modified `MatchJoinAttempt` function to actually reject joins that would exceed team size limit
> 
> **Estimated Effort**: Quick
> **Parallel Execution**: NO - single task
> **Critical Path**: Task 1 only

---

## Context

### Original Request
User reported seeing 5 players on a team in public arena matches, when maximum should be 4.

### Interview Summary
**Key Discussions**:
- Public arena matches should enforce 4-player team limit
- Currently seeing 5 players occasionally

**Research Findings**:
- Root cause identified in `MatchJoinAttempt` function
- Post-addition check (lines 439-446) logs error but doesn't reject the join
- Returns `true` unconditionally at line 449

---

## Work Objectives

### Core Objective
Enforce the 4-player team size limit for public arena matches by actually rejecting joins that would exceed it.

### Concrete Deliverables
- Modified `server/evr_match.go` - `MatchJoinAttempt` function

### Definition of Done
- [x] Public arena matches reject 5th player attempting to join a team
- [x] Existing tests pass: `go test -short -vet=off ./server -run ".*evr.*"`

### Must Have
- Reject join when team size would exceed limit after addition
- Remove player from state if rejecting after addition
- Use `DefaultPublicArenaTeamSize` (4) as the hard limit for public arena

### Must NOT Have (Guardrails)
- Do NOT change Combat mode behavior (5 players is correct)
- Do NOT change private match behavior
- Do NOT modify the pre-addition check at line 398 (keep it as a first defense)

---

## Verification Strategy

### Test Decision
- **Infrastructure exists**: YES (Go tests exist)
- **User wants tests**: Manual verification preferred for this quick fix
- **Framework**: go test

### Automated Verification

```bash
# Run EVR-specific tests
go test -short -vet=off ./server -run ".*evr.*"

# Build to verify no compile errors
go build -o /dev/null ./...
```

---

## TODOs

- [x] 1. Fix team size enforcement in MatchJoinAttempt

  **What to do**:
  1. Locate the post-addition team size check at lines 439-449 in `server/evr_match.go`
  2. Modify it to actually reject the join if team size exceeds `DefaultPublicArenaTeamSize` (4)
  3. Remove the player from `presenceMap`, `presenceByEvrID`, and `joinTimestamps` before rejecting
  4. Call `updateLabel` to rebuild cache after removal
  5. Return `false` with `ErrJoinRejectReasonLobbyFull` error

  **Must NOT do**:
  - Do not change Combat mode behavior
  - Do not modify the constants (DefaultPublicArenaTeamSize is already 4)

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Single file change, well-defined scope, under 30 min work
  - **Skills**: []
    - No special skills needed for this Go code change

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Sequential (single task)
  - **Blocks**: None
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `server/evr_match.go:439-449` - Current broken check (logs but doesn't reject)
  - `server/evr_match.go:398-402` - Pre-addition check pattern (shows how to reject)
  - `server/evr_match.go:431-433` - Where player is added to presenceMap

  **Constant References**:
  - `server/evr_match.go:33` - `DefaultPublicArenaTeamSize = 4`
  - `server/evr_match.go:181` - `ErrJoinRejectReasonLobbyFull` error to use

  **WHY Each Reference Matters**:
  - Line 439-449 is what needs to be changed
  - Line 398-402 shows the pattern for rejecting: `return state, false, ErrorMessage`
  - Line 431-433 shows what fields to clean up when rejecting

  **Acceptance Criteria**:

  **Automated Verification:**
  ```bash
  # Build succeeds
  go build -o /dev/null ./...
  # Exit code: 0

  # EVR tests pass
  go test -short -vet=off ./server -run ".*evr.*"
  # Exit code: 0, all tests pass
  ```

  **Code Change Expected:**
  Replace lines 439-449 in `server/evr_match.go`:
  
  FROM:
  ```go
  // Check team sizes
  if state.Mode == evr.ModeArenaPublic {
      if state.RoleCount(evr.TeamBlue) > state.TeamSize || state.RoleCount(evr.TeamOrange) > state.TeamSize {
          logger.WithFields(map[string]interface{}{
              "blue":   state.RoleCount(evr.TeamBlue),
              "orange": state.RoleCount(evr.TeamOrange),
          }).Error("Oversized team.")
      }
  }

  return state, true, meta.Presence.String()
  ```

  TO:
  ```go
  // Enforce team size limits for public arena matches
  if state.Mode == evr.ModeArenaPublic {
      blueCount := state.RoleCount(evr.TeamBlue)
      orangeCount := state.RoleCount(evr.TeamOrange)
      maxTeamSize := DefaultPublicArenaTeamSize // Always 4 for public arena

      if blueCount > maxTeamSize || orangeCount > maxTeamSize {
          logger.WithFields(map[string]interface{}{
              "blue":          blueCount,
              "orange":        orangeCount,
              "max_team_size": maxTeamSize,
              "team_size":     state.TeamSize,
          }).Error("Rejecting join: team size limit exceeded for public arena match.")

          // Remove the player that was just added
          delete(state.presenceMap, sessionID)
          delete(state.presenceByEvrID, meta.Presence.EvrID)
          delete(state.joinTimestamps, sessionID)

          // Rebuild cache to reflect the removal
          if err := m.updateLabel(logger, dispatcher, state); err != nil {
              logger.Error("Failed to update label after rejecting oversized team: %v", err)
          }

          return state, false, ErrJoinRejectReasonLobbyFull.Error()
      }
  }

  return state, true, meta.Presence.String()
  ```

  **Commit**: YES
  - Message: `fix(evr): reject joins that exceed public arena team size limit`
  - Files: `server/evr_match.go`
  - Pre-commit: `go test -short -vet=off ./server -run ".*evr.*"`

---

## Commit Strategy

| After Task | Message | Files | Verification |
|------------|---------|-------|--------------|
| 1 | `fix(evr): reject joins that exceed public arena team size limit` | server/evr_match.go | `go test -short -vet=off ./server -run ".*evr.*"` |

---

## Success Criteria

### Verification Commands
```bash
go build -o /dev/null ./...   # Expected: exit 0, no errors
go test -short -vet=off ./server -run ".*evr.*"  # Expected: all tests pass
```

### Final Checklist
- [x] Public arena matches reject 5th player on a team
- [x] Build succeeds
- [x] EVR tests pass
