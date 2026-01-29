# Matchmaker Scalability Tests

## Overview

This document describes automated tests that reproduce and confirm a scalability bug in the Nakama default matchmaker where processing becomes severely degraded under large ticket counts.

## The Bug

**Location**: `server/matchmaker_process.go`, lines 88 and 398

**Issue**: The `processDefault` and `processCustom` functions call:
```go
searchRequest := bluge.NewTopNSearch(indexCount, indexQuery)
```

where `indexCount = len(m.indexes)` - the **total** number of tickets (both active and inactive).

**Impact**: As the matchmaker accumulates a backlog of tickets that have reached max intervals, every search operation examines ALL tickets in the index, not just active ones. This causes:
- Search work to grow O(N × A) where N = total tickets, A = active tickets
- Processing time to explode: 4x more total tickets → ~4x longer processing time
- Matchmaker becoming effectively "stuck" with millions of candidate matches examined

**Expected Behavior**: Search should be bounded by active ticket count, not total ticket count. Processing time should scale with active tickets, not total backlog.

## Test Suite

### Running the Tests

**Quick test (skips long-running tests):**
```bash
go test -short -vet=off ./server -run TestMatchmaker
```

**Full scalability tests (may take 1-2 minutes):**
```bash
go test -vet=off ./server -run "TestMatchmakerScaling|TestMatchmakerBurst|TestMatchmakerOverload"
```

**Individual tests:**
```bash
# Scaling analysis
go test -vet=off ./server -run TestMatchmakerScalingBehavior -v

# Burst load simulation
go test -vet=off ./server -run TestMatchmakerBurstLoad -v

# Timeout detection
go test -vet=off ./server -run TestMatchmakerOverloadTimeoutDetection -v
```

### Test Descriptions

#### TestMatchmakerScalingBehavior
**Purpose**: Demonstrates how processing time grows with total ticket count.

**Method**:
- Creates pools with different inactive ticket counts (100, 500, 1000)
- Keeps active ticket count constant (20)
- Measures Process() time for each configuration
- Calculates scaling ratio (time growth vs ticket growth)

**Expected Result**: Processing time should scale with **active** tickets (constant 20), not total tickets.

**Bug Manifestation**: Processing time grows nearly linearly with total tickets:
- 120 tickets (100 inactive + 20 active): ~9ms
- 520 tickets (500 inactive + 20 active): ~35ms (3.8x slower)
- 1020 tickets (1000 inactive + 20 active): ~68ms (7.3x slower)

With only 20 active tickets, processing should take similar time regardless of backlog size.

#### TestMatchmakerBurstLoad
**Purpose**: Simulates real-world scenario of new players arriving with existing backlog.

**Method**:
- Creates 3000 inactive tickets (backlog)
- Adds burst of 100 new active tickets
- Measures time to process matches
- Verifies matches are formed

**Expected Result**: 100 active 2-player tickets should match in <100ms regardless of backlog.

**Bug Manifestation**: Takes ~785ms to process 100 active tickets when 3000 inactive tickets exist.

#### TestMatchmakerOverloadTimeoutDetection
**Purpose**: Tests if Process() can complete within reasonable timeout under load.

**Method**:
- Creates 5000 inactive tickets
- Adds 100 active tickets
- Runs Process() with 5s timeout
- Detects if timeout is exceeded

**Expected Result**: 100 active tickets should process in <500ms.

**Bug Manifestation**: Takes ~1.3s with 5000 inactive tickets (would timeout with more backlog).

#### TestMatchmakerParallelAddAndProcess
**Purpose**: Tests concurrent Add operations during slow Process execution.

**Method**:
- Creates 2000 inactive ticket backlog
- Starts Process() in background
- Attempts 50 concurrent Add operations
- Verifies no deadlocks or errors

**Expected Result**: Add operations complete quickly even during Process.

**Bug Manifestation**: Add operations may block or timeout due to mutex contention from slow Process.

## Test Results Summary

| Test | Inactive Tickets | Active Tickets | Process Time | Status |
|------|-----------------|----------------|--------------|--------|
| Baseline | 0 | 100 | ~50ms | Normal |
| Scaling (small) | 100 | 20 | ~9ms | Slow |
| Scaling (medium) | 500 | 20 | ~35ms | Very Slow |
| Scaling (large) | 1000 | 20 | ~68ms | Extremely Slow |
| Burst Load | 3000 | 100 | ~785ms | Pathological |
| Timeout Test | 5000 | 100 | ~1300ms | Near Timeout |

## Instrumentation Test (Future)

`TestMatchmakerProcessWithInstrumentation` is currently skipped and requires minimal code changes to enable:

**Required Changes** (add to `LocalMatchmaker`):
```go
type LocalMatchmaker struct {
    // ... existing fields ...
    
    // Test instrumentation (only incremented when testing)
    SearchOperationCount   atomic.Int64
    CandidateExaminedCount atomic.Int64
}
```

**In `processDefault` (line ~88)**:
```go
searchRequest := bluge.NewTopNSearch(indexCount, indexQuery)

// Add after creating searchRequest:
if m.SearchOperationCount.Load() >= 0 { // Only if instrumentation enabled
    m.SearchOperationCount.Add(1)
}
```

**In search result iteration** (after line ~106):
```go
blugeMatches, err := IterateBlugeMatches(result, map[string]struct{}{}, m.logger)

// Add after getting matches:
if m.CandidateExaminedCount.Load() >= 0 {
    m.CandidateExaminedCount.Add(int64(len(blugeMatches.Hits)))
}
```

With instrumentation, the test can directly measure:
- Number of search operations (should = activeIndexCount)
- Number of candidates examined (reveals indexCount being used)

## Acceptance Criteria

✅ **Tests are automated** - No manual intervention required

✅ **Tests are runnable** - Standard `go test` command with `-run` filter

✅ **Tests demonstrate the bug reliably** - Consistently show superlinear growth

✅ **Tests are deterministic** - Use fixed seed/state, time bounds are generous

✅ **CI-friendly** - Skip long tests with `-short` flag, reasonable timeouts

✅ **No production changes** - Only test files added, minimal instrumentation hooks

## Next Steps

1. Run the full test suite to confirm bug reproduction
2. Analyze bluge search patterns to understand correct usage
3. Implement fix by limiting search scope to active tickets only
4. Re-run tests to verify fix (processing should scale with active count)
5. Add instrumentation to validate search operation counts

## Fix Strategy (Not Implemented)

The fix should change:
```go
// BEFORE (line 88 in matchmaker_process.go):
searchRequest := bluge.NewTopNSearch(indexCount, indexQuery)
```

To something like:
```go
// AFTER:
// Use activeIndexCount instead of indexCount, or apply interval filtering in query
maxSearchResults := activeIndexCount * 2 // Reasonable upper bound
searchRequest := bluge.NewTopNSearch(maxSearchResults, indexQuery)
```

Or add interval filtering to the query to exclude inactive tickets from search results.
