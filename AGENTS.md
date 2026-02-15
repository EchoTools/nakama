# NAKAMA PROJECT KNOWLEDGE BASE

**Generated:** 2026-02-10  
**Commit:** 8bedbab  
**Branch:** copilot/init-deep-hierarchical-files

## OVERVIEW

EchoVR game server (Nakama fork) with custom binary protocol, matchmaking, Discord integration, and VRML league support. Go 1.25+, PostgreSQL backend.

## STRUCTURE

```
nakama/
├── main.go               # Server entry point
├── server/               # 339 Go files - core + EVR logic (see server/AGENTS.md)
│   ├── evr/              # 99 files - EVR binary protocol parsers (see server/evr/AGENTS.md)
│   ├── evr_*.go          # 150+ files - EVR extensions (pipeline, matchmaker, discord, runtime)
│   ├── api_*.go          # REST/gRPC API handlers
│   ├── runtime_*.go      # Lua/JS/Go runtime support
│   └── pipeline_*.go     # Request processing chains
├── apigrpc/              # Generated protobuf gRPC code
├── console/              # Admin dashboard (Angular UI)
├── internal/             # Shared utilities
│   ├── gopher-lua/       # Embedded Lua VM (see internal/gopher-lua/AGENTS.md)
│   ├── cronexpr/         # Cron parsing
│   ├── ctxkeys/          # Context key definitions
│   ├── intents/          # Permission system
│   └── skiplist/         # Ordered data structure
├── migrate/sql/          # Database migrations
├── data/modules/         # Runtime module examples
└── build/                # Docker/build configs
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| **EVR protocol messages** | `server/evr/` | Binary codec, 90+ message types |
| **EVR game server logic** | `server/evr_*.go` | Pipeline, matchmaker, Discord, VRML |
| **Matchmaking** | `server/evr_matchmaker.go`, `server/evr_lobby_*.go` | Skill ratings, backfill, team balance |
| **Discord integration** | `server/evr_discord_*.go` | Slash commands, guild management |
| **API handlers** | `server/api_*.go` | REST endpoints for accounts, storage, etc. |
| **Runtime hooks** | `server/runtime*.go` | Lua/JS/Go extension points |
| **Database schema** | `migrate/sql/` | PostgreSQL migrations |
| **Build/test** | `Makefile`, `.github/workflows/` | CI/CD, Docker builds |

## CRITICAL: TEST-FIRST (NON-NEGOTIABLE)

**PRODUCTION OUTAGE PREVENTION**

1. Write failing test (proves bug/feature) → 2. Verify fails → 3. Fix → 4. Verify passes
- **NO EXCEPTIONS**: "Small change", "refactoring", "typo" - ALL need tests if behavior changes
- **You Are Not Human**: Your assertions mean NOTHING without verification

```bash
# Step 1: Write test that FAILS
go test -v -vet=off ./server -run TestYourNewTest  # MUST fail
# Step 2: Make code change
# Step 3: Test MUST pass
go test -v -vet=off ./server -run TestYourNewTest  # MUST pass
```

## CONVENTIONS

- **Monolithic `/server`**: 339 files, one package, file prefixes (`evr_`, `api_`, `core_`) not subpackages
- **Root `main.go`**: Non-standard (should be `/cmd/nakama/main.go`)
- **Table-driven tests**: `[]struct{name, input, want}` with `t.Run()`, no testify
- **Error wrapping**: `fmt.Errorf("context: %w", err)` + sentinel errors
- **Context propagation**: `ctxkeys` package for UserID, Username, etc.

## ANTI-PATTERNS

- ❌ **`interface{}`**: Use `any` (Go 1.18+)
- ❌ **Skipping tests**: See TEST-FIRST above
- ❌ **Tests >10 min**: Cancel immediately
- ❌ **CockroachDB**: PostgreSQL ONLY
- ❌ **Removing tests**: Hides bugs
- ⚠️ **Full test suite**: Run EVR only: `go test -short -vet=off ./server/evr/...`
- ⚠️ **Benchmarks**: Too slow (>10 min)
- ⚠️ **Satori code**: Ignore (not used)

## UNIQUE PATTERNS

**EVR Protocol**: Symbol-based routing (64-bit hash), custom binary codec, `Message` interface with `Symbol()` + `Stream()`

**Pipelines**: Atomic globals, per-domain handlers, `func (p *EvrPipeline) handleXxx(ctx, session, msg) error`

**Registries**: `XxxRegistry` interface + `LocalXxx` struct, thread-safe (RWMutex + atomics)

**Caches**: `LocalXxxCache` with TTL + mutex

## COMMANDS

```bash
# Build (~2m)
make nakama
go mod vendor && make nakama  # With dep refresh

# Test (EVR only - full suite is slow)
go test -short -vet=off ./server/evr/...       # Protocol (fast)
go test -short -vet=off ./server -run ".*evr.*" # Server EVR
go test -v -vet=off ./server -run TestName      # Single test
go test -race -vet=off ./server/evr/...         # Race detection

# Database
docker compose up -d postgres && sleep 30
./nakama migrate up --database.address postgres:localdb@127.0.0.1:5432/nakama
./nakama --name nakama1 --database.address postgres:localdb@127.0.0.1:5432/nakama
# API: http://127.0.0.1:7350, Socket: ws://127.0.0.1:7349, Key: defaultkey

# Format
gofmt -w .
```

## GIT COMMITS

**Format**: `<type>[scope]: <description>` (Conventional Commits v1.0.0)  
**Types**: `feat`, `fix`, `docs`, `refactor`, `test`, `chore`  
**Scopes**: `evr`, `pipeline`, `matchmaker`, `runtime`, `discord`, `api`  
**Breaking**: Use `!` or `BREAKING CHANGE:` footer

## VALIDATION CHECKLIST

1. Write failing test → 2. Verify fails → 3. Fix code → 4. Verify passes → 5. EVR tests → 6. Build → 7. Format → 8. Commit

## NOTES

- **Cross-repo deps**: `nevr-common` (protobufs), `vrmlgo` (VRML league)
- **Large files**: `runtime.go` (~2800), `evr_runtime_rpc.go` (~2000), 111 files >500 lines
- **PostgreSQL only**: NOT CockroachDB
- **No `/cmd`**: Non-standard layout
