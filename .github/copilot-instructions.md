# Nakama Development (EchoVR Fork)

**Constraint:** This is a fork of heroiclabs/nakama with EchoVR-specific extensions. Ignore satori-related code.

## Architecture

This Nakama fork adds EchoVR game server functionality:
- `server/evr/` - EVR binary protocol parsers (mirrors `nevr-common/serviceapi/`)
- `server/evr_*.go` - EVR-specific server logic (pipeline, matchmaker, runtime)
- Standard Nakama in `server/` - API, console, runtime, matchmaker

**Cross-repo dependencies** (via go.work):
- `nevr-common` → Shared protobuf definitions
- `vrmlgo` → VRML league integration

## Build & Run

```bash
# Build (requires protoc for full build, ~2m expected)
go mod vendor && make nakama

# Database setup (PostgreSQL, NOT CockroachDB)
docker compose up -d postgres
sleep 30
./nakama migrate up --database.address postgres:localdb@127.0.0.1:5432/nakama

# Run server
./nakama --name nakama1 --database.address postgres:localdb@127.0.0.1:5432/nakama
```

Endpoints: API `http://127.0.0.1:7350`, Socket `ws://127.0.0.1:7349`, default key: `defaultkey`

## Testing

### Build failures
- Clean vendor directory: `rm -rf vendor && go mod vendor`
- Check Go version: `go version` (requires Go 1.25.0+)
- Verify all dependencies downloaded: Look for any network errors

No benchmarks - they take too long. Cancel any test running >10 minutes.

## Key EVR Components

- `evr_pipeline.go` - Main EVR message processing pipeline
- `evr_matchmaker.go` - Custom EVR matchmaking with skill ratings
- `evr_runtime.go` - EVR-specific runtime hooks and RPCs
- `evr_match.go` - EVR match handler implementation
- `evr_discord_*.go` - Discord bot integration

## Code Patterns

### Go Type Conventions
- Use `any` instead of `interface{}` (Go 1.18+)
- Prefer typed parameters over empty interfaces

### EVR Binary Protocol
Messages in `server/evr/` use binary encoding with packet headers:
```go
// All EVR messages implement this pattern
type MyMessage struct {
    // Fields parsed from binary packet
}
func (m *MyMessage) Symbol() Symbol { return SymbolMyMessage }
func (m *MyMessage) Stream(s *Stream) { /* binary encode/decode */ }
```

### Pipeline Handlers
EVR pipeline in `server/evr_pipeline*.go` routes messages:
```go
func (p *EVRPipeline) handleMyMessage(ctx context.Context, msg *evr.MyMessage) error {
    // Handle incoming EVR protocol message
}
```

## Git Commit Convention

**MANDATORY**: All commits MUST follow [Conventional Commits v1.0.0](https://www.conventionalcommits.org/en/v1.0.0/).

### Format

```
<type>[optional scope]: <description>

[optional body]

[optional footer(s)]
```

### Types

| Type | Usage | SemVer |
|------|-------|--------|
| `feat` | New feature | MINOR |
| `fix` | Bug fix | PATCH |
| `docs` | Documentation only | - |
| `style` | Code style/formatting | - |
| `refactor` | Code restructuring | - |
| `perf` | Performance improvements | - |
| `test` | Adding or updating tests | - |
| `build` | Build system or dependencies | - |
| `ci` | CI/CD configuration | - |
| `chore` | Maintenance tasks | - |
| `revert` | Reverting commits | - |

### Scopes (Optional)

`evr`, `pipeline`, `matchmaker`, `runtime`, `discord`, `api`, `storage`, `auth`

### Breaking Changes

**CRITICAL**: Indicate with `!` or `BREAKING CHANGE:` footer:
- `feat(api)!: remove deprecated endpoint`
- `BREAKING CHANGE: description` (in footer)

### Examples

```
feat(discord): add channel notification for match completion

fix(matchmaker): prevent duplicate match assignments

Fixes: #123

refactor(evr): simplify message handler registration

Extract handler map initialization to separate function
for improved readability. No behavior changes.
```

### Rules

1. Type is REQUIRED
2. Use imperative mood: "add" not "added"
3. First line ≤ 72 characters
4. Body after blank line
5. Footer after blank line

## Validation Cycle

1. `make nakama` (build)
2. Run migrations
3. Start server, verify endpoints
4. Run EVR tests
5. `gofmt -w .` (format)
6. **Commit with conventional format**
