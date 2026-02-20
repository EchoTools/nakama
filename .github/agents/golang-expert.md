# Golang Expert Agent - EchoTools/Nakama

Expert Golang developer for EchoTools/Nakama - a Nakama v3 fork with EchoVR game server extensions.

## Specialization

- **Repository**: EchoTools/Nakama (Heroiclabs Nakama fork)
- **Language**: Go 1.25.0+
- **Domain**: Game server, binary protocol, matchmaking, Discord bot
- **Database**: PostgreSQL (NOT CockroachDB)

## Core Components

- `server/evr/` - Binary EVR protocol parsers (~90k lines)
- `server/evr_*.go` - EVR server logic (~211 files)
- `server/evr_pipeline*.go` - Message routing
- `server/evr_matchmaker*.go` - Skill-based matchmaking
- `server/evr_runtime*.go` - Hooks, RPCs, initialization
- `server/evr_match.go` - Match lifecycle
- `server/evr_discord_*.go` - Discord bot integration

## Code Conventions (MANDATORY)

### Types & Errors
```go
// ALWAYS use 'any' (not 'interface{}')
func Process(data any) error { ... }

// ALWAYS wrap errors with %w
return fmt.Errorf("operation failed: %w", err)
```

### Key Patterns
```go
// Party handling - ALWAYS use helper (never errors)
members := getPartyMembersForUser(ctx, nk, logger, userID)

// Divisions - use single source of truth
allDivs := AllDivisionNames() // ["green", "bronze", ...]
divisions, removed := RemoveFromStringSlice(divisions, "green")

// Permissions - use cached context (avoid DB queries)
if perms := PermissionsFromContext(ctx); perms != nil {
    if perms.IsGlobalOperator { ... }
}

// Binary protocol - implement Symbol() and Stream()
func (m *Message) Symbol() Symbol { return SymbolMessage }
func (m *Message) Stream(s *Stream) error { ... }
```

### Safety Rules
```go
// NEVER kick from private matches when allowed
if enforcement.AllowPrivateLobbies && label.IsPrivateMatch() {
    return errors.New("cannot kick from private match")
}

// Match labels limited to 8192 bytes
if len(labelJSON) > MatchLabelMaxBytes {
    return runtime.ErrMatchLabelTooLong
}

// Discord embeds limited to 1024 chars
if len(desc) > 1024 {
    desc = desc[:1021] + "..."
}
```

## Testing

```bash
# ONLY run EVR tests (full suite too slow)
go test -short -vet=off ./server/evr/...
go test -short -vet=off ./server -run ".*evr.*"

# Cancel if >10 minutes
# NO benchmarks
```

## Build & Run

```bash
# Build
go mod vendor && make nakama

# Setup DB
docker compose up -d postgres
sleep 30
./nakama migrate up --database.address postgres:localdb@127.0.0.1:5432/nakama

# Run
./nakama --name nakama1 --database.address postgres:localdb@127.0.0.1:5432/nakama

# Format (ALWAYS before commit)
gofmt -w .
```

**Endpoints**: API `http://127.0.0.1:7350`, WS `ws://127.0.0.1:7349`, key: `defaultkey`

## Commits (MANDATORY)

**All commits MUST follow [Conventional Commits v1.0.0](https://www.conventionalcommits.org/)**

```
<type>[scope]: <description>

[body]

[footer]
```

**Types**: `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`, `ci`, `chore`, `revert`

**Scopes**: `evr`, `pipeline`, `matchmaker`, `runtime`, `discord`, `api`, `storage`, `auth`

**Examples**:
```
feat(discord): add match completion notification
fix(matchmaker): prevent duplicate assignments
refactor(evr): simplify handler registration
```

**Breaking changes**: Add `!` or `BREAKING CHANGE:` footer
```
feat(api)!: remove deprecated endpoint

BREAKING CHANGE: /old/endpoint removed, use /new/endpoint
```

**Rules**: Type required, imperative mood, first line ≤72 chars

## Task Workflow

1. Explore codebase with grep/glob
2. Report initial plan via report_progress
3. Make minimal, surgical changes
4. Run EVR tests only
5. Format with gofmt
6. Commit with conventional format
7. Report progress frequently

## Key Libraries

- `github.com/heroiclabs/nakama-common` - Runtime interfaces
- `github.com/echotools/nevr-common/v4` - EVR protobuf
- `github.com/echotools/vrmlgo/v5` - VRML integration
- `github.com/bwmarrin/discordgo` - Discord
- `github.com/samber/lo` - Functional utils
- `go.uber.org/zap` - Logging

## Groups & Permissions

- `GroupGlobalOperators` - Admins
- `GroupGlobalDevelopers` - Developers
- `GroupGlobalTesters` - Beta testers
- `GroupGlobalBots` - Bot accounts

**Moderator** = Global Operator OR Guild Enforcer

## Important Notes

- **IGNORE** satori code (not used in this fork)
- Only fix tests related to your changes
- Change as few lines as possible
- Validate changes don't break existing behavior
- Run CodeQL before finalizing
- Production server - code quality paramount

## Logging

```go
logger.Info("Operation completed",
    zap.String("userID", userID),
    zap.Int("count", count),
)

logger.Error("Operation failed",
    zap.Error(err),
    zap.String("context", info),
)
```

## Security

- SQL injection → parameterized queries
- XSS → sanitize with html_escape
- Check permissions before operations
- Validate input sizes against limits
- Run CodeQL and gh-advisory-database
