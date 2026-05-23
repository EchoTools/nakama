---
description: 'Known code smells and anti-patterns in the nakama EVR fork. Agents should avoid introducing these during refactoring.'
applyTo: '**/*.go'
---

> **REFACTOR ALERT:** This repo is undergoing EVR module extraction. Before
> starting any work, read `REFACTOR.yaml` and
> `project-management.instructions.md` to orient in the plan.
> **Do not make unrelated changes outside the current phase's tasks.**

# Code Smells — nakama EVR Fork

This document catalogs known code smells, anti-patterns, and high-risk patterns in the nakama EVR fork. During refactoring, **do not introduce new instances of these patterns**. If you encounter existing instances, flag them for cleanup rather than propagating them.

The repo includes a `.smell-scan-manifest.json` that tracks SHA256 hashes of all Go files. If you modify a file, its hash in the manifest becomes stale — the smell manifest should be updated after refactoring work.

## Go Anti-Patterns (nakama-specific)

### ⚠️ Mandatory: use `any`, never `interface{}`
- The AGENTS.md forbids `interface{}`. `go vet` will catch this. Don't let it through.

### ⚠️ Must: wrap errors with `%w`
- Every `fmt.Errorf` that re-wraps an error MUST use `%w`. Missing `%w` breaks `errors.Is`/`errors.As` chains.

### ⚠️ Must: use `context.Context` as first param
- Every I/O or blocking function must take `ctx context.Context` as its first parameter. No exceptions.

### ⚠️ Must: goroutine teardown paths
- Every goroutine spawned must have a documented exit path. No fire-and-forget goroutines in production code.

## EVR Module Smells

### 🚩 Protocol Type Registration
**Smell:** Adding a new message type to `server/evr/` but forgetting to register it in `NewMessageFromHash()` in `core_packet_types.go`.

**Why it matters:** Unregistered messages will not be routed by the pipeline. They'll be silently dropped.

**Fix:** For every new `*_message.go` file in `server/evr/`, add an entry to the `NewMessageFromHash()` switch statement.

### 🚩 Stream() decode without allocation guard
**Smell:** Using `s.StreamBytes` or similar in decode mode without checking `s.Mode == DecodeMode` before allocating.

```go
// BAD — doesn't check mode
func (m *MyMessage) Stream(s *EasyStream) error {
    return RunErrorFunctions([]func() error{
        func() error { return s.StreamBytes(&m.Payload, len(m.Payload)) },
    })
}

// GOOD — allocates only in decode mode
func (m *MyMessage) Stream(s *EasyStream) error {
    if s.Mode == DecodeMode {
        m.Payload = make([]byte, s.Len())
    }
    return RunErrorFunctions([]func() error{
        func() error { return s.StreamBytes(&m.Payload, len(m.Payload)) },
    })
}
```

### 🚩 Business logic in protocol types
**Smell:** Adding non-serialization logic to files in `server/evr/`.

**Why it matters:** Protocol type files should contain only the `Symbol()` / `Stream()` interface and simple validation. Business logic (matchmaking, enforcement, pipeline handling) belongs in the `server/evr_*.go` files.

### 🚩 Pipeline handler returning opaque errors
**Smell:** Returning generic errors from pipeline handlers without enough context for debugging.

**Fix:** Wrap errors with function-scoped prefixes: `fmt.Errorf("evrPipeline.handleMyMessage: %w", err)`.

## Structural Smells

### 🚩 God packages
**Smell:** Files that grow beyond ~800 lines. In the nakama codebase, `server/evr_pipeline.go`, `server/evr_matchmaker.go`, and similar core files are already large.

**During refactoring:** If you're adding significant new logic to a file over ~800 lines, extract the new code into a dedicated file (e.g., `evr_pipeline_matchmaking.go` rather than adding to `evr_pipeline.go`).

### 🚩 Dead code accumulation
**Smell:** Functions, types, or constants that are no longer used but remain in the codebase.

**During refactoring:** If you refactor a function and its old version is no longer called, **remove the old code**. Don't leave it as a comment. Don't gate it behind an `unused` flag.

### 🚩 Copy-paste duplication
**Smell:** The same logic appearing in multiple files with minor variations.

**In this codebase:** The EVR pipeline, matchmaker, and enforcement modules all have similar patterns for logging, error handling, and database access. Extract shared patterns rather than duplicating them.

### 🚩 `context.Background()` in production code
**Smell:** Using `context.Background()` anywhere other than `main()`, tests, or top-level server initialization.

**Why it matters:** `context.Background()` never cancels. Production code should use the context passed to the handler or derive one with a timeout.

## Concurrency Smells

### 🚩 Unbounded goroutine spawning
**Smell:** `go func()` inside a loop with no rate limiting or bound.

**Fix:** Use an `errgroup.Group` with a bounded worker pool, or at minimum cap the number of concurrent goroutines.

### 🚩 Mutex lock/unlock/lock patterns
**Smell:** Releasing and re-acquiring a mutex mid-function (TOCTOU risk).

```go
// BAD — releases lock, then re-acquires
mu.Lock()
if condition {
    mu.Unlock()
    doSomething()
    mu.Lock()
}
mu.Unlock()
```

**Fix:** Hold the lock through the critical section, or restructure to avoid the gap.

### 🚩 Package-level mutable state
**Smell:** `var` declarations at package level that are mutated at runtime (maps, slices, counters) without synchronization.

**Fix:** Encapsulate in a struct with explicit locking, or use `sync.Map` / `atomic` types.

## Testing Smells

### 🚩 Live network calls in unit tests
**Smell:** Tests that make real HTTP calls, connect to real databases, or hit production APIs.

**Fix:** Use `httptest.NewServer`, mock interfaces, or gate behind `//go:build integration`.

### 🚩 `time.Sleep` in tests
**Smell:** Tests that use `time.Sleep` to wait for asynchronous operations.

**Fix:** Use channels, `sync.WaitGroup`, or `testutil.Eventually` patterns.

### 🚩 `t.Error()` without fatal
**Smell:** Using `t.Error()` instead of `t.Fatal()` or `require.*` — the test continues running after a failure, potentially causing confusing cascading failures.

**Fix:** Use `t.Fatal()` or `require.NoError()` / `require.Equal()`.

## .smell-scan-manifest.json

The `.smell-scan-manifest.json` at the repo root tracks all Go source files with their SHA256 hashes. When you modify Go files during refactoring, the manifest becomes stale. The expected workflow:

1. Refactor code
2. Run the local gate (`make nakama`, `go test`, `go vet`, etc.)
3. If you modified `.go` files, re-generate the manifest or update the relevant entries
4. Commit both the code changes and the updated manifest together

To regenerate the manifest:
```bash
# Generates fresh SHA256 hashes for all non-vendor Go files
python3 -c "
import json, hashlib, os
files = {}
for root, dirs, fnames in os.walk('.'):
    if '/vendor/' in root or '/.git/' in root: continue
    for f in sorted(fnames):
        if f.endswith('.go'):
            path = os.path.join(root, f)[2:]
            h = hashlib.sha256()
            with open(os.path.join(root, f), 'rb') as fh: h.update(fh.read())
            files[path] = {'sha256': h.hexdigest(), 'scanned_at': None, 'has_annotations': False}
with open('.smell-scan-manifest.json', 'w') as f:
    json.dump({'version': 1, 'created': '2026-05-23T16:25:00Z', 'last_scan': None, 'files': files}, f, indent=2)
"
```

## Prioritization for the Refactor Push

When refactoring, prioritize fixing:

1. **Production correctness** — context leaks, goroutine leaks, missing error wrapping, TOCTOU races
2. **Test hygiene** — live network calls in tests, `time.Sleep`, `t.Error` instead of `t.Fatal`
3. **Structural cleanup** — dead code removal, god package splitting, duplication extraction
4. **Low-hanging fruit** — `interface{}` → `any`, missing `//nolint` comments, stale imports
