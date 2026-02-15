# SERVER PACKAGE

**Scope:** `/server` (339 Go files - core Nakama + EVR extensions)

## OVERVIEW

Monolithic package: core Nakama APIs + 150+ EVR files (pipeline, matchmaker, Discord, VRML).

## WHERE TO LOOK

| Task | Files |
|------|-------|
| **EVR protocol** | `evr_*.go` (150+ files) |
| **Matchmaking** | `evr_matchmaker.go`, `evr_lobby_*.go` |
| **Discord** | `evr_discord_*.go` (10 files) |
| **VRML** | `evr_runtime_vrml_*.go` (5 files) |
| **API** | `api_*.go` (20+ files) |
| **Runtime** | `runtime_*.go` (30+ files, Lua/JS/Go) |
| **Registries** | `*_registry.go` (session, match, party, status) |
| **Pipelines** | `pipeline_*.go`, `evr_pipeline_*.go` |

## CONVENTIONS

- **No subpackages**: All in `/server` despite 339 files
- **File prefixes**: `evr_`, `api_`, `core_`, `runtime_`, `pipeline_`
- **Runtime mirrors**: `runtime_lua_*.go`, `runtime_javascript_*.go`, `runtime_go_*.go` same structure
- **Registry pattern**: `XxxRegistry` interface + `LocalXxx` impl
- **Cache pattern**: `LocalXxxCache` with TTL + mutex

## KEY FILES (>500 lines)

| File | Lines | Purpose |
|------|-------|---------|
| `runtime.go` | ~2800 | Hook registration (refactor if modifying) |
| `evr_runtime_rpc.go` | ~2000 | 38+ RPC handlers |
| `evr_discord_appbot.go` | ~1500 | 27+ Discord commands |
| `evr_lobby_backfill.go` | ~1000 | Backfill algorithms |
| `evr_matchmaker.go` | ~1200 | Skill-based matchmaking |

## PATTERNS

```go
// Pipeline handler
func (p *EvrPipeline) handleLogin(ctx context.Context, session *sessionWS, msg *evr.LoginRequest) error

// Registry usage
session := sessionRegistry.Get(sessionID)
matchRegistry.CreateMatch(ctx, matchID, ...)

// Cache
type LocalProfileCache struct {
    sync.RWMutex
    cache map[string]*cachedProfile
}

// Error handling
return fmt.Errorf("load match %s: %w", matchID, err)
```

## TESTING

```bash
# EVR only (fast)
go test -short -vet=off ./server -run ".*evr.*"

# Single test
go test -v -vet=off ./server -run TestName
```
