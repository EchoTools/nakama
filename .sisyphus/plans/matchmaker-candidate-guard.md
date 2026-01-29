# Matchmaker Candidate Explosion Guard

## TL;DR

> **Quick Summary**: Add a configurable cap on TopN search hits per ticket in the default matchmaker to prevent O(N²) candidate explosion that caused a 45-minute incident with >28M candidates.
> 
> **Deliverables**:
> - New `MaxSearchHits` config field in `MatchmakerConfig`
> - Guard logic in `processDefault()` that caps TopN search
> - Metric counter when guard triggers
> - Warning log when guard triggers
> - Automated tests for guard behavior
> 
> **Estimated Effort**: Medium
> **Parallel Execution**: YES - 2 waves
> **Critical Path**: Task 1 → Task 3 → Task 4 → Task 5

---

## Context

### Original Request
Add a guardrail to the default Nakama matchmaker to prevent candidate explosion (O(N²) scaling) that caused a 45-minute incident with >28 million candidate matches.

### Interview Summary
**Key Discussions**:
- **Guard behavior**: Hard cap + early exit for the cycle, proceed with matches found so far
- **Primary guard budget**: Max search hits per ticket (cap TopN to configurable value)
- **Observability**: Metric increment + warning log when guard triggers
- **Configuration**: New `matchmaker.*` config fields (runtime tunable via YAML)

**Research Findings**:
- `processDefault` in `matchmaker_process.go:88` uses `bluge.NewTopNSearch(indexCount, indexQuery)` where `indexCount = len(all tickets)`
- With 5000 inactive + 100 active tickets, this creates 510,000 candidate examinations per cycle
- `processCustom` already has a cap (24 hits: 8 oldest + 16 newest) at lines 481-486
- Existing tests use `createTestMatchmaker()` helper in `matchmaker_test.go`
- `matchmaker_overload_test.go` already has overload reproduction tests we can extend

### Metis Review
**Identified Gaps** (addressed):
- Default cap value: 1000 (high enough for legitimate matches, prevents 28M explosion)
- Cap = 0 semantic: Treat as error in validation (must be >= 10)
- Log rate limiting: Use existing logger patterns (one warning per guard trigger)
- No external consumers expect unbounded results (confirmed by code analysis)

---

## Work Objectives

### Core Objective
Prevent the default matchmaker from exploding into O(N²) candidate generation by capping the TopN search hits per ticket, with configurable threshold and observability when the guard triggers.

### Concrete Deliverables
- `server/config.go`: New `MaxSearchHits` field in `MatchmakerConfig`
- `server/matchmaker_process.go`: Guard logic at line 88 using `min(indexCount, maxSearchHits)`
- `server/metrics.go`: New `MatchmakerSearchCapped` method on `Metrics` interface
- `server/matchmaker_test.go` or new file: Tests for config validation, guard trigger, overload with guard

### Definition of Done
- [ ] `go build ./...` succeeds with no errors
- [ ] `go test -v -run TestMatchmakerMaxSearchHits ./server/...` passes
- [ ] `go test -v -timeout 30s -run TestMatchmakerOverload ./server/...` completes within 30s (was timing out)
- [ ] Guard logs warning when `indexCount > MaxSearchHits`

### Must Have
- `MaxSearchHits` config field with default 1000
- Validation: `MaxSearchHits >= 10` (prevent misconfiguration)
- Guard applied at `bluge.NewTopNSearch` call in `processDefault`
- Metric incremented when guard triggers
- Warning logged with ticket info when guard triggers

### Must NOT Have (Guardrails)
- **DO NOT** modify `processCustom` — it already has its own 24-hit cap
- **DO NOT** add hot-reload capability — requires server restart (standard pattern)
- **DO NOT** change EVR SBMM matchmaker (`evr_matchmaker.go`) — out of scope
- **DO NOT** touch backfill logic (`evr_lobby_backfill.go`) — separate concern
- **DO NOT** add complex adaptive caps or per-query limits — keep it simple
- **DO NOT** add dashboard/console UI for this config — YAML only
- **DO NOT** over-engineer: ~50 lines production code + ~100 lines tests is sufficient

---

## Verification Strategy

### Test Decision
- **Infrastructure exists**: YES (`go test`, existing `matchmaker_*_test.go` files)
- **User wants tests**: TDD approach
- **Framework**: Go testing (`go test`)

### TDD Structure

Each implementation task follows RED-GREEN-REFACTOR:

1. **RED**: Write failing test first
   - Test file: `server/matchmaker_guard_test.go` (new) or extend `matchmaker_test.go`
   - Test command: `go test -v -run TestMatchmaker ./server/...`
   - Expected: FAIL (test exists, implementation doesn't)

2. **GREEN**: Implement minimum code to pass
   - Command: `go test -v -run TestMatchmaker ./server/...`
   - Expected: PASS

3. **REFACTOR**: Clean up while keeping green
   - Command: `go test -v -run TestMatchmaker ./server/...`
   - Expected: PASS (still)

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 1 (Start Immediately):
├── Task 1: Add MaxSearchHits config field
└── Task 2: Add MatchmakerSearchCapped metric method

Wave 2 (After Wave 1):
├── Task 3: Implement guard logic in processDefault
└── Task 4: Write guard trigger tests

Wave 3 (After Wave 2):
└── Task 5: Verify overload test passes with guard
```

### Dependency Matrix

| Task | Depends On | Blocks | Can Parallelize With |
|------|------------|--------|---------------------|
| 1 | None | 3 | 2 |
| 2 | None | 3 | 1 |
| 3 | 1, 2 | 4, 5 | None |
| 4 | 3 | 5 | None |
| 5 | 3, 4 | None | None (final) |

### Agent Dispatch Summary

| Wave | Tasks | Recommended Agents |
|------|-------|-------------------|
| 1 | 1, 2 | delegate_task(category="quick", run_in_background=true) |
| 2 | 3, 4 | sequential after Wave 1 |
| 3 | 5 | final verification |

---

## TODOs

- [ ] 1. Add MaxSearchHits config field to MatchmakerConfig

  **What to do**:
  - Add `MaxSearchHits int` field to `MatchmakerConfig` struct in `server/config.go`
  - Add YAML/JSON tags: `yaml:"max_search_hits" json:"max_search_hits"`
  - Add usage documentation in struct tag
  - Set default value of 1000 in `NewMatchmakerConfig()`
  - Add validation in config validation (if exists) or note for Task 3: `MaxSearchHits >= 10`

  **Must NOT do**:
  - Do NOT add hot-reload capability
  - Do NOT add to any other config structs

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Single file change, straightforward struct addition following existing patterns
  - **Skills**: [`git-master`]
    - `git-master`: Needed for atomic commit of config change

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Task 2)
  - **Blocks**: Task 3
  - **Blocked By**: None (can start immediately)

  **References**:
  - `server/config.go:1307-1332` - `MatchmakerConfig` struct definition (follow this exact pattern)
  - `server/config.go:1324-1331` - `NewMatchmakerConfig()` default values (add default here)
  - `server/config.go:1308` - Example of existing field with tags: `MaxTickets int yaml:"max_tickets" json:"max_tickets" usage:"..."`

  **WHY Each Reference Matters**:
  - Lines 1307-1332 show the exact struct layout and tag format to follow
  - `NewMatchmakerConfig()` is where default 1000 must be set
  - `MaxTickets` field shows the naming convention (`Max*` prefix)

  **Acceptance Criteria**:

  ```bash
  # Build succeeds
  go build -o /dev/null ./...
  # Assert: Exit code 0

  # Field exists with correct default
  grep -q "MaxSearchHits.*int.*yaml:\"max_search_hits\"" server/config.go
  # Assert: Exit code 0

  # Default value is 1000
  grep -A 10 "func NewMatchmakerConfig" server/config.go | grep -q "MaxSearchHits.*1000"
  # Assert: Exit code 0
  ```

  **Commit**: YES
  - Message: `feat(matchmaker): add MaxSearchHits config field`
  - Files: `server/config.go`
  - Pre-commit: `go build ./...`

---

- [ ] 2. Add MatchmakerSearchCapped metric method

  **What to do**:
  - Add `MatchmakerSearchCapped(delta int64)` method to `Metrics` interface in `server/metrics.go`
  - Implement method on `LocalMetrics` struct
  - Use counter: `m.PrometheusScope.Counter("matchmaker_search_capped").Inc(delta)`
  - Add no-op implementation in test mock `testMetrics` in `server/match_common_test.go`

  **Must NOT do**:
  - Do NOT add labels/tags (keep simple counter)
  - Do NOT modify existing metric methods

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Small interface addition, following existing patterns exactly
  - **Skills**: [`git-master`]
    - `git-master`: Needed for atomic commit

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Task 1)
  - **Blocks**: Task 3
  - **Blocked By**: None (can start immediately)

  **References**:
  - `server/metrics.go:33-70` - `Metrics` interface definition (add method here)
  - `server/metrics.go:431-436` - `Matchmaker()` method pattern (follow this style)
  - `server/metrics.go:453-459` - `CustomCounter()` implementation (similar counter pattern)
  - `server/match_common_test.go:181` - `testMetrics` mock (add no-op implementation)

  **WHY Each Reference Matters**:
  - Interface at lines 33-70 is where the method signature must be added
  - `Matchmaker()` method shows the naming and documentation style
  - `CustomCounter()` shows how to increment a counter with `Inc(delta)`
  - `testMetrics` mock must implement the interface for tests to compile

  **Acceptance Criteria**:

  ```bash
  # Build succeeds (interface satisfied)
  go build -o /dev/null ./...
  # Assert: Exit code 0

  # Interface has new method
  grep -q "MatchmakerSearchCapped" server/metrics.go
  # Assert: Exit code 0

  # LocalMetrics implements it
  grep -A 5 "func (m \*LocalMetrics) MatchmakerSearchCapped" server/metrics.go | grep -q "Counter"
  # Assert: Exit code 0

  # testMetrics implements it (for test compilation)
  grep -q "func.*testMetrics.*MatchmakerSearchCapped" server/match_common_test.go
  # Assert: Exit code 0
  ```

  **Commit**: YES
  - Message: `feat(metrics): add MatchmakerSearchCapped counter`
  - Files: `server/metrics.go`, `server/match_common_test.go`
  - Pre-commit: `go build ./...`

---

- [ ] 3. Implement guard logic in processDefault

  **What to do**:
  - In `server/matchmaker_process.go`, find line 88: `bluge.NewTopNSearch(indexCount, indexQuery)`
  - Replace `indexCount` with `min(indexCount, m.config.GetMatchmaker().MaxSearchHits)`
  - Add check: if `indexCount > maxSearchHits`, call `m.metrics.MatchmakerSearchCapped(1)` and log warning
  - Warning log format: `m.logger.Warn("matchmaker search capped", zap.String("ticket", activeIndex.Ticket), zap.Int("cap", maxSearchHits), zap.Int("total_indexes", indexCount))`
  - Add config validation at matchmaker startup: if `MaxSearchHits < 10`, log fatal error

  **Must NOT do**:
  - Do NOT modify `processCustom` (already has own cap at lines 481-486)
  - Do NOT add complex adaptive logic
  - Do NOT include query string in logs (may contain user properties)

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Localized change to single function, clear implementation
  - **Skills**: [`git-master`]
    - `git-master`: Needed for atomic commit

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 2 (sequential)
  - **Blocks**: Task 4, Task 5
  - **Blocked By**: Task 1, Task 2

  **References**:
  - `server/matchmaker_process.go:88` - The exact line to modify: `bluge.NewTopNSearch(indexCount, indexQuery)`
  - `server/matchmaker_process.go:28-35` - `processDefault` function signature and logger access
  - `server/matchmaker_process.go:481-486` - `processCustom` cap pattern (DO NOT TOUCH, just shows existing guard pattern)
  - `server/matchmaker.go:277-330` - `NewLocalMatchmaker` for config validation location
  - `server/config.go:605` - `GetMatchmaker()` accessor method

  **WHY Each Reference Matters**:
  - Line 88 is THE line to modify - this is the root cause of the O(N²) scaling
  - Lines 28-35 show how `m.logger` and `m.config` are accessed in this function
  - Lines 481-486 show how processCustom already caps hits (validates our approach)
  - `NewLocalMatchmaker` is where startup validation should go
  - `GetMatchmaker()` is how to access the config

  **Acceptance Criteria**:

  ```bash
  # Build succeeds
  go build -o /dev/null ./...
  # Assert: Exit code 0

  # Guard logic exists (min function or equivalent)
  grep -E "min\(indexCount.*MaxSearchHits|MaxSearchHits.*indexCount" server/matchmaker_process.go
  # Assert: Exit code 0

  # Warning log exists
  grep -q "matchmaker search capped" server/matchmaker_process.go
  # Assert: Exit code 0

  # Metric call exists
  grep -q "MatchmakerSearchCapped" server/matchmaker_process.go
  # Assert: Exit code 0
  ```

  **Commit**: YES
  - Message: `feat(matchmaker): add TopN search cap guard in processDefault`
  - Files: `server/matchmaker_process.go`, `server/matchmaker.go` (if validation added there)
  - Pre-commit: `go build ./... && go test -v -run TestMatchmaker -short ./server/...`

---

- [ ] 4. Write guard trigger tests

  **What to do**:
  - Create new test file `server/matchmaker_guard_test.go` OR add to existing `matchmaker_test.go`
  - Test 1: `TestMatchmakerMaxSearchHitsConfigValidation` - verify config defaults and validation
  - Test 2: `TestMatchmakerGuardTriggersAtCap` - create tickets exceeding cap, verify:
    - Process completes without timeout
    - Metric was incremented
    - Log warning was emitted (via test logger)
  - Test 3: `TestMatchmakerGuardDoesNotTriggerBelowCap` - verify normal behavior unaffected
  - Use `createTestMatchmaker()` helper from `matchmaker_test.go`

  **Must NOT do**:
  - Do NOT test `processCustom` (out of scope)
  - Do NOT create flaky time-based tests (use deterministic assertions)

  **Recommended Agent Profile**:
  - **Category**: `unspecified-low`
    - Reason: Test writing following existing patterns, moderate complexity
  - **Skills**: [`git-master`]
    - `git-master`: Needed for atomic commit

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 2 (after Task 3)
  - **Blocks**: Task 5
  - **Blocked By**: Task 3

  **References**:
  - `server/matchmaker_test.go:1616-1690` - `createTestMatchmaker()` helper function (use this)
  - `server/matchmaker_test.go:37-64` - `TestMatchmakerAddOnly` (simple test pattern)
  - `server/matchmaker_overload_test.go:35-130` - `TestMatchmakerOverloadTimeoutDetection` (overload pattern)
  - `server/matchmaker_overload_test.go:74-78` - How to force tickets to max intervals (inactive state)
  - `server/match_common_test.go:168-185` - `testMetrics` struct (may need to capture metric calls)

  **WHY Each Reference Matters**:
  - `createTestMatchmaker()` is the standard helper - must use it for consistency
  - `TestMatchmakerAddOnly` shows the basic test structure and logger setup
  - `TestMatchmakerOverloadTimeoutDetection` shows how to create many tickets and trigger overload
  - Lines 74-78 show how to manipulate `index.Intervals` to create inactive tickets
  - `testMetrics` may need extension to capture `MatchmakerSearchCapped` calls for assertion

  **Acceptance Criteria**:

  ```bash
  # Tests exist and pass
  go test -v -run "TestMatchmakerMaxSearchHits|TestMatchmakerGuard" ./server/... 2>&1 | grep -E "PASS|ok"
  # Assert: Exit code 0, output contains "PASS"

  # At least 2 test functions exist
  grep -c "func TestMatchmaker.*Guard\|func TestMatchmaker.*MaxSearchHits" server/matchmaker_guard_test.go server/matchmaker_test.go 2>/dev/null | grep -E "[2-9]|[1-9][0-9]"
  # Assert: At least 2 test functions
  ```

  **Commit**: YES
  - Message: `test(matchmaker): add guard trigger and config tests`
  - Files: `server/matchmaker_guard_test.go` or `server/matchmaker_test.go`
  - Pre-commit: `go test -v -run TestMatchmakerGuard -short ./server/...`

---

- [ ] 5. Verify overload test passes with guard

  **What to do**:
  - Run existing `TestMatchmakerOverloadTimeoutDetection` with the guard in place
  - Verify it completes within 30 seconds (was timing out at 45+ minutes before)
  - If test needs adjustment (e.g., assertions about guard triggering), update it
  - Run full matchmaker test suite to ensure no regressions

  **Must NOT do**:
  - Do NOT skip or delete existing overload tests
  - Do NOT increase test timeouts to hide the problem

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Verification task, running existing tests
  - **Skills**: [`git-master`]
    - `git-master`: Needed if any test adjustments required

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 3 (final)
  - **Blocks**: None (final task)
  - **Blocked By**: Task 3, Task 4

  **References**:
  - `server/matchmaker_overload_test.go:35-130` - `TestMatchmakerOverloadTimeoutDetection` (the test to verify)
  - `server/matchmaker_overload_test.go:51` - `inactiveTicketCount := 5000` (the load that was causing timeout)
  - `server/matchmaker_overload_test.go:80-130` - Active ticket addition and timing assertions

  **WHY Each Reference Matters**:
  - This is THE test that reproduces the incident scenario
  - 5000 inactive tickets is the load that triggers O(N²) explosion
  - With guard at 1000, this should complete quickly instead of timing out

  **Acceptance Criteria**:

  ```bash
  # Overload test completes within 30s
  timeout 60 go test -v -timeout 30s -run TestMatchmakerOverloadTimeoutDetection ./server/... 2>&1 | grep -E "PASS|ok"
  # Assert: Exit code 0, "PASS" appears, completes within 60s total

  # Full matchmaker test suite passes (short mode)
  go test -short -v -run "TestMatchmaker" ./server/... 2>&1 | tail -5
  # Assert: Final line shows "ok" or "PASS"

  # Build still works
  go build -o /dev/null ./...
  # Assert: Exit code 0
  ```

  **Commit**: YES (if test adjustments needed) or NO (if just verification)
  - Message: `test(matchmaker): verify overload test passes with guard`
  - Files: `server/matchmaker_overload_test.go` (if modified)
  - Pre-commit: `go test -short -run TestMatchmaker ./server/...`

---

## Commit Strategy

| After Task | Message | Files | Verification |
|------------|---------|-------|--------------|
| 1 | `feat(matchmaker): add MaxSearchHits config field` | `server/config.go` | `go build ./...` |
| 2 | `feat(metrics): add MatchmakerSearchCapped counter` | `server/metrics.go`, `server/match_common_test.go` | `go build ./...` |
| 3 | `feat(matchmaker): add TopN search cap guard in processDefault` | `server/matchmaker_process.go` | `go build && go test -short` |
| 4 | `test(matchmaker): add guard trigger and config tests` | `server/matchmaker_guard_test.go` | `go test -run TestMatchmakerGuard` |
| 5 | (optional) `test(matchmaker): verify overload test passes with guard` | `server/matchmaker_overload_test.go` | `go test -timeout 30s -run TestMatchmakerOverload` |

---

## Success Criteria

### Verification Commands
```bash
# 1. Build succeeds
go build -o /dev/null ./...
# Expected: Exit code 0

# 2. Config field exists with default
grep -A 15 "type MatchmakerConfig struct" server/config.go | grep MaxSearchHits
# Expected: MaxSearchHits int with yaml/json tags

# 3. Guard tests pass
go test -v -run "TestMatchmakerGuard|TestMatchmakerMaxSearchHits" ./server/...
# Expected: PASS

# 4. Overload test completes quickly
timeout 60 go test -v -timeout 30s -run TestMatchmakerOverloadTimeoutDetection ./server/...
# Expected: PASS within 30 seconds

# 5. Full matchmaker suite passes
go test -short -run TestMatchmaker ./server/...
# Expected: ok github.com/heroiclabs/nakama/server
```

### Final Checklist
- [ ] `MaxSearchHits` config field present with default 1000
- [ ] Guard logic applied at `bluge.NewTopNSearch` call
- [ ] Metric `matchmaker_search_capped` incremented when guard triggers
- [ ] Warning logged with ticket info when guard triggers
- [ ] Tests verify guard behavior
- [ ] Overload test completes within 30s (was 45+ minutes)
- [ ] No changes to `processCustom`, EVR SBMM, or backfill logic
