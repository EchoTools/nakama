---
description: 'Go conventions, build, and code review standards for the nakama EVR fork. Apply to all Go files.'
applyTo: '**/*.go'
---

> **REFACTOR ALERT:** This repo is undergoing EVR module extraction. Before
> starting any work, read `REFACTOR.yaml` and
> `project-management.instructions.md` to orient in the plan.
> **Do not make unrelated changes outside the current phase's tasks.**

# Go Development — nakama EVR Fork

This is a fork of [heroiclabs/nakama](https://github.com/heroiclabs/heroiclabs/nakama) with EchoVR-specific extensions. The project adds a custom EVR game server module to the upstream Nakama game server.

**Constraint:** Ignore satori-related code. This fork strips satori. Do not add it back.

## Go Version & Toolchain

- **Go version:** `1.25.5` (declared in `go.mod`). Do not use features from later versions.
- **Module path:** `github.com/heroiclabs/nakama/v3`
- **Cross-repo dependencies** (via `go.work`): `nevr-proto` (shared protobuf definitions), `vrmlgo/v5` (VRML league integration)

## Build

**ALWAYS use `make nakama` to build. Never run `go build` directly.** The Makefile handles CGO flags, debug symbols, ldflags for version injection, and path trimming.

```bash
# Build binary
make nakama

# Full clean build (after dependency changes)
go mod tidy && go mod vendor && make nakama
```

- Build uses `CGO_ENABLED=1` with `CGO_CFLAGS="-O0 -g"` (required for sqlite/nakama deps). The Makefile sets this.
- Build output: `./nakama` binary.
- If `make nakama` fails, run `go mod tidy && go mod vendor` first, then retry.

## Code Standards (from repo AGENTS.md)

Read the full `AGENTS.md` at repo root for the complete Go code review ruleset. Key points:

### You MUST
- Define interfaces at the CONSUMER, return concrete types from constructors.
- Wrap errors with `%w` and a function-scoped prefix on every boundary.
- Pass `context.Context` as the first parameter on any I/O or blocking call.
- Treat linter warnings as errors. `//nolint` requires an inline reason.
- Run local gate before declaring done: `gofmt -l`, `go vet`, `golangci-lint run`, `go test -race`, `go fix`, `go mod tidy`.

### You must NEVER
- Use `interface{}` (use `any`).
- Return a non-nil error AND a non-zero result on the same call.
- Call `log.Fatal` / `os.Exit` outside `main` / `cmd/`.
- Spawn a goroutine without a teardown path or an `errgroup` parent.
- Use `context.Background()` outside `main`, tests, or top-level servers.

### Import grouping
```go
import (
    "context"                              // stdlib
    "github.com/some/dep"                  // external
    "github.com/heroiclabs/nakama/v3/internal" // internal
)
```

### Logging
Use `log/slog` only. No zerolog, no zap, no logrus.

## Project Layout

```
server/            # Main application code (EVR + upstream)
  evr/             # EVR binary protocol types (mirrors nevr-proto/serviceapi/)
  evr_*.go         # EVR-specific server logic (pipeline, matchmaker, runtime)
  evr_discord_*.go # Discord bot integration
  api*.go          # Upstream Nakama API handlers
  console*.go      # Admin console
  core*.go         # Core Nakama logic
apigrpc/           # gRPC API definitions
console/           # Console frontend
data/              # Runtime data / config
flags/             # CLI flags
iap/               # In-app purchase handling
internal/          # Internal utilities
migrate/           # Database migrations
se/                # Social / encryption helpers
server/evr/wire/   # Wire protocol definitions (auto-generated)
```

## EVR Module Quick Reference

| File(s) | Purpose |
|---|---|
| `server/evr_pipeline.go` | Main EVR message processing pipeline |
| `server/evr_matchmaker*.go` | Custom EVR matchmaking with skill ratings |
| `server/evr_runtime*.go` | EVR-specific runtime hooks and RPCs |
| `server/evr_match*.go` | EVR match handler implementation |
| `server/evr_discord_*.go` | Discord bot integration (appbot, linked roles) |
| `server/evr_authenticate*.go` | EVR authentication flows |
| `server/evr_lobby*.go` | Lobby management |
| `server/evr/` | Binary protocol message types |

## Dependencies (notable)

- `buf.build/gen/go/echotools/nevr-api/protocolbuffers/go` — NEVR API protobuf definitions
- `github.com/echotools/nevr-fleetmanager` — Fleet manager client
- `github.com/echotools/vrmlgo/v5` — VRML league integration
- `github.com/intinig/go-openskill` (replaced) — Skill rating system
- `github.com/bwmarrin/discordgo` — Discord bot library
- `github.com/gofrs/uuid/v5` — UUID handling
- `github.com/stretchr/testify` — Test assertions

## Common Mistakes

- Adding satori-related imports or types (ignore satori entirely)
- Using `context.Background()` in library/server code
- Not running `go mod tidy && go mod vendor` after dependency changes
- Forgetting `CGO_ENABLED=1` in build commands
- Writing EVR protocol types without the `Symbol()` / `Stream()` interface pattern
- Using `interface{}` instead of `any`
