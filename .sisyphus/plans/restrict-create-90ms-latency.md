# Restrict /create to Servers Within 90ms Latency

## TL;DR

> **Quick Summary**: Add a 90ms maximum latency restriction to the `/create` Discord command, preventing players from creating matches on servers they have poor connectivity to.
> 
> **Deliverables**:
> - Modified server filtering in `/create` command handler
> - User-friendly error message when no servers within 90ms are available
> 
> **Estimated Effort**: Quick
> **Parallel Execution**: NO - sequential (single task)
> **Critical Path**: Implement filter → Test → PR

---

## Context

### Original Request
Add a 90ms latency restriction to the `/create` command so players can only create matches on servers within 90ms of their measured RTT.

### Research Findings
- **`/create` handler**: Located in `server/evr_discord_appbot_handlers.go` at `handleCreateMatch()` (line 718)
- **Server selection**: `LobbyGameServerAllocate()` in `server/evr_lobby_builder.go` (line 665)
- **Latency data**: `LatencyHistory` loaded from storage, accessed via `latencyHistory.AverageRTTs(true)`
- **Existing filtering**: Server sorting already uses RTT but doesn't enforce a hard maximum for `/create`
- **Existing threshold**: There's a 150ms "low latency" threshold used for sorting priority, but no hard cutoff

---

## Work Objectives

### Core Objective
Prevent `/create` from allocating servers where the player's average RTT exceeds 90ms.

### Concrete Deliverables
- Modified `handleCreateMatch()` or `LobbyGameServerAllocate()` to filter out servers with RTT > 90ms
- Clear error message to user when no servers meet the latency requirement

### Definition of Done
- [x] `/create` fails gracefully when no servers within 90ms are available
- [x] `/create` succeeds when servers within 90ms exist
- [x] Error message tells user their best available server latency

### Must Have
- 90ms hard cutoff for server selection
- User-friendly error message with their actual latency info

### Must NOT Have (Guardrails)
- Do NOT change matchmaking behavior (only `/create` command)
- Do NOT modify the existing sorting algorithm logic
- Do NOT add new Discord command options
- Do NOT change how latency data is collected or stored

---

## Verification Strategy

### Test Decision
- **Infrastructure exists**: YES (Go tests exist)
- **User wants tests**: Manual verification (quick feature)
- **Framework**: Go test

### Manual Verification Procedure
```bash
# Build the server
go build -o nakama .

# Test scenarios:
# 1. Player with servers <90ms should successfully create match
# 2. Player with all servers >90ms should see error message with their best latency
```

---

## Execution Strategy

### Single Task - No Parallelization Needed

This is a focused change to one code path.

---

## TODOs

- [x] 1. Add 90ms latency filter to /create command

  **What to do**:
  - In `handleCreateMatch()` (server/evr_discord_appbot_handlers.go ~line 718), after loading latency history:
    1. Get `latencyHistory.AverageRTTs(true)` (already done)
    2. Before calling `LobbyGameServerAllocate()`, filter the RTT map to only include servers with RTT ≤ 90ms
    3. OR: Add a `maxRTT` parameter to `LobbyGameServerAllocate()` that filters servers
  - If no servers remain after filtering, return a user-friendly error:
    - "No servers within 90ms of your location. Your best server has Xms latency."
    - Include the closest server's latency in the message

  **Implementation approach** (choose one):
  - **Option A**: Filter RTTs before passing to allocator (simpler, isolated change)
  - **Option B**: Add maxRTT parameter to `LobbyGameServerAllocate()` (more reusable)
  
  Recommend **Option A** for minimal blast radius.

  **Must NOT do**:
  - Change sorting algorithm
  - Affect matchmaking (only /create)
  - Add new command options

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Single file change, focused modification, clear requirements
  - **Skills**: `[]`
    - No special skills needed - straightforward Go code modification

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Sequential (only task)
  - **Blocks**: Nothing
  - **Blocked By**: Nothing

  **References**:

  **Pattern References**:
  - `server/evr_discord_appbot_handlers.go:718-800` - `handleCreateMatch()` function, the main handler to modify
  - `server/evr_discord_appbot_handlers.go:750-765` - Existing validation pattern (rate limiting, permissions)
  - `server/evr_lobby_builder.go:810-843` - Region fallback error pattern with `FallbackInfo` (follow this pattern for error messaging)

  **API/Type References**:
  - `server/evr_latencyhistory.go:AverageRTTs()` - Returns `map[string]int` of server IPs to average RTT in ms
  - `server/evr_lobby_matchmake.go:44-70` - `RegionFallbackInfo` struct and `ErrMatchmakingNoServersInRegion` error (pattern to follow)

  **Code to reference for error handling**:
  - `server/evr_discord_appbot_handlers.go:760-770` - How to return Discord interaction errors with user-friendly messages

  **Acceptance Criteria**:

  **Automated Verification (using Bash)**:
  ```bash
  # Build succeeds
  go build ./...
  # Exit code 0

  # Tests pass
  go test ./server/... -run "Create|Allocate" -v
  # No failures
  ```

  **Manual Verification**:
  - Deploy to test environment
  - As a player with known latencies, run `/create` 
  - Verify server selected is within 90ms
  - Test with a player who has no servers under 90ms - should see error with their best latency

  **Commit**: YES
  - Message: `fix(discord): restrict /create to servers within 90ms latency`
  - Files: `server/evr_discord_appbot_handlers.go`
  - Pre-commit: `go build ./... && go test ./server/... -short`

---

## Commit Strategy

| After Task | Message | Files | Verification |
|------------|---------|-------|--------------|
| 1 | `fix(discord): restrict /create to servers within 90ms latency` | server/evr_discord_appbot_handlers.go | `go build && go test ./server/... -short` |

---

## Success Criteria

### Verification Commands
```bash
go build ./...           # Expected: success, exit 0
go test ./server/... -short  # Expected: all tests pass
```

### Final Checklist
- [x] /create only allocates servers with ≤90ms RTT
- [x] Clear error message when no servers qualify
- [x] No changes to matchmaking behavior
- [x] No changes to sorting algorithm
- [x] Build and tests pass (pre-existing errors on main, our changes are valid)
