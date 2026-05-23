---
description: 'Testing conventions, patterns, and commands for the nakama EVR fork. Apply to test files.'
applyTo: '**/*_test.go'
---

> **REFACTOR ALERT:** This repo is undergoing EVR module extraction. Before
> starting any work, read `REFACTOR.yaml` and
> `project-management.instructions.md` to orient in the plan.
> **Do not make unrelated changes outside the current phase's tasks.**

# Testing — nakama EVR Fork

## Test Patterns

### Table-Driven Tests (Preferred)

Use table-driven tests for all logic-heavy functions. This is the standard pattern in the codebase:

```go
func TestCalculateMmr(t *testing.T) {
    t.Parallel()
    tests := []struct {
        name string
        input Input
        want  int
    }{
        {name: "basic case", input: Input{A: 1, B: 2}, want: 3},
        {name: "edge case", input: Input{A: 0, B: 0}, want: 0},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got := CalculateMmr(tt.input)
            require.Equal(t, tt.want, got)
        })
    }
}
```

### Assertions

Use `github.com/stretchr/testify/require` (not `assert`). `require` stops the test immediately, which is preferred in this codebase:

```go
import "github.com/stretchr/testify/require"

require.NoError(t, err)
require.Equal(t, expected, actual)
require.True(t, condition)
require.NotNil(t, result)
require.ElementsMatch(t, expectedSlice, actualSlice)
```

### Test File Layout

- Test files live NEXT TO the code: `foo_test.go` beside `foo.go`.
- Use `_test` package suffix for black-box tests: `package evr_test` (preferred).
- Use the same package for white-box tests when testing internal behavior.

### Named Test Functions

```go
func Test_functionName_scenario(t *testing.T)
func TestMyFunction_WhenInputIsValid(t *testing.T)
func TestMyFunction_WhenInputIsInvalid(t *testing.T)
```

## Running Tests

```bash
# Run all server tests
go test ./server/...

# Run specific package tests
go test ./server/evr/...

# Run with race detection (required before committing)
go test -race ./server/...

# Run a specific test
go test -run TestCalculateMmr ./server/...

# Run tests verbose
go test -v -run TestCalculateMmr ./server/...

# Full local gate (from AGENTS.md)
make fmt     # gofmt -w . && goimports -w .
make vet     # go vet ./...
make lint    # golangci-lint run ./...
make test    # go test -race -count=1 ./...
```

## Test Dependencies

- **Database-dependent tests**: Need PostgreSQL running via Docker Compose. Run `docker compose up -d postgres` before these tests.
- **Integration tests**: Use build tag `//go:build integration`. These require the full setup (Postgres + nakama).

## EVR Protocol Testing

For EVR protocol message tests in `server/evr/`:

```go
func TestLoginSuccessStream(t *testing.T) {
    t.Parallel()

    // Create a message, encode it, decode it, verify round-trip
    original := &LoginSuccess{Field1: "test", Field2: 42}
    data, err := Encode(original)
    require.NoError(t, err)

    decoded := &LoginSuccess{}
    err = Decode(data, decoded)
    require.NoError(t, err)
    require.Equal(t, original.Field1, decoded.Field1)
}
```

## Test Fixtures

- Test fixtures live in `server/testdata/`.
- Use `t.TempDir()` for temporary test directories.
- Use `t.ArtifactDir()` (Go 1.26) for test output files.

## What NOT to Do

- No `time.Sleep` in tests — use channels, WaitGroup, or `testutil.Eventually`.
- No `t.Error()` then continue — use `t.Fatal()` or `require.*`.
- No mocking frameworks that monkey-patch. Use interfaces + `moq`/`mockery` at boundaries.
- No test helpers in `main_test.go` — use shared `testutil` package.
- Do not run benchmarks — they take too long. Cancel any test running >10 minutes.
