# AGENTS.md - AI Agent Development Guidelines

## Project Overview

Nakama game server (heroiclabs/nakama fork) with EchoVR-specific extensions.
- **Language**: Go 1.25+ | **Database**: PostgreSQL (NOT CockroachDB)
- `server/evr/` - EVR binary protocol message parsers
- `server/evr_*.go` - EVR-specific server logic (pipeline, matchmaker, runtime)
- `server/` - Standard Nakama (API, console, runtime)

**Cross-repo deps** (go.work): `nevr-common` (protobufs), `vrmlgo` (VRML league)

## Build Commands

```bash
make nakama                    # Standard debug build (~2m)
go mod vendor && make nakama   # With dependency refresh
```

## Test Commands

**Only run EVR-specific tests** - full suite is slow.

```bash
# EVR protocol tests (fast, recommended)
go test -short -vet=off ./server/evr/...

# EVR server tests
go test -short -vet=off ./server -run ".*evr.*"

# Single test by exact name
go test -v -vet=off ./server -run "TestEarlyQuitConfig_UpdateTier"

# With race detection
go test -race -vet=off ./server/evr/...
```

**Cancel any test >10 minutes.** Avoid benchmarks.

## Database & Run

```bash
docker compose up -d postgres && sleep 30
./nakama migrate up --database.address postgres:localdb@127.0.0.1:5432/nakama
./nakama --name nakama1 --database.address postgres:localdb@127.0.0.1:5432/nakama
```

Endpoints: API `http://127.0.0.1:7350`, Socket `ws://127.0.0.1:7349`, Key: `defaultkey`

## Code Style

### Formatting
- `gofmt -w .` (enforced) | Tabs for Go, spaces for others | LF line endings

### Imports (standard Go grouping)
```go
import (
    "context"
    "fmt"

    "github.com/gofrs/uuid/v5"
    "go.uber.org/zap"

    "github.com/heroiclabs/nakama/v3/server/evr"
)
```

### Types & Naming
- Use `any` not `interface{}` (Go 1.18+)
- Files: `evr_<component>.go` | Types: PascalCase | Vars: camelCase

### Error Handling
```go
return fmt.Errorf("failed to load match: %w", err)  // Wrap with context
var ErrLobbyFull = errors.New("lobby full")          // Sentinel errors
```

### EVR Binary Protocol (`server/evr/`)
```go
type MyMessage struct { Field1 uint64 }
func (m *MyMessage) Symbol() Symbol { return SymbolMyMessage }
func (m *MyMessage) Stream(s *Stream) error { return s.Stream(&m.Field1) }
```

### Pipeline Handlers (`server/evr_pipeline*.go`)
```go
func (p *EvrPipeline) handleMyMessage(ctx context.Context, session *sessionWS, msg *evr.MyMessage) error {
    return nil
}
```

### Tests (table-driven)
```go
func TestMyFunction(t *testing.T) {
    tests := []struct {
        name     string
        input    int
        expected int
    }{
        {"basic case", 1, 2},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            if got := MyFunction(tt.input); got != tt.expected {
                t.Errorf("MyFunction(%d) = %d, want %d", tt.input, got, tt.expected)
            }
        })
    }
}
```

## Git Commits (Conventional Commits v1.0.0)

### Format
```
<type>[scope]: <description>

[body]
[footer]
```

### Types
| Type | Usage |
|------|-------|
| `feat` | New feature |
| `fix` | Bug fix |
| `docs` | Documentation |
| `refactor` | Restructuring |
| `test` | Tests |
| `chore` | Maintenance |

### Scopes
`evr`, `pipeline`, `matchmaker`, `runtime`, `discord`, `api`, `storage`, `auth`

### Examples
```
feat(discord): add channel notification for match completion
fix(matchmaker): prevent duplicate match assignments
```

### Rules
- Type REQUIRED, imperative mood ("add" not "added"), <=72 chars first line
- Breaking changes: `feat(api)!:` or `BREAKING CHANGE:` footer

## Key Files

| File | Purpose |
|------|---------|
| `evr_pipeline.go` | EVR message processing |
| `evr_matchmaker.go` | Skill-based matchmaking |
| `evr_match.go` | Match handler |
| `evr_discord_*.go` | Discord integration |
| `evr/core_packet.go` | Binary protocol codec |

## Validation Checklist

1. `make nakama` - Build passes
2. `go test -short -vet=off ./server/evr/...` - Tests pass
3. `gofmt -w .` - Formatted
4. Commit with conventional format

## Notes

- **Ignore satori code** - Not used in this fork
- **PostgreSQL only** - Not CockroachDB
- **Binary protocol** - EVR uses custom binary encoding, not JSON/protobuf
