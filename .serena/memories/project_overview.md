# Nakama Project Overview

## Project Purpose
Nakama is a distributed game server for social and realtime games and apps. It's a production-ready server written in Go.

## Key Features
- User authentication and management (social networks, email, device ID)
- Storage, social graphs, chat, multiplayer
- Leaderboards, tournaments, parties
- Purchase validation, in-app notifications
- Runtime code support (Lua, TypeScript/JavaScript, Go)
- Matchmaking, dashboard, metrics

## Tech Stack
- **Language**: Go 1.25.0
- **Database**: PostgreSQL/CockroachDB
- **Protocols**: gRPC, HTTP1.1+JSON (REST), WebSockets, rUDP
- **Key Dependencies**: gRPC, Protocol Buffers, Docker support

## Code Structure
- `server/` - Main server code
  - `evr_*.go` - EVR (game-specific) implementations
  - `api_*.go` - API implementations for various features
  - `console_*.go` - Console/dashboard APIs
  - `core_*.go` - Core functionality
  - `runtime_*.go` - Runtime implementations
- `apigrpc/` - gRPC definitions (generated)
- `console/` - Console UI code
- `migrate/` - Database migrations
- `internal/` - Internal utilities

## Early Quit System
Located in:
- `server/evr_earlyquit.go` - Core early quit logic and data structures
- `server/evr_match.go` - Where early quit is recorded (lines ~620-690)

### Key Components
- **EarlyQuitConfig**: Struct storing early quit statistics per user
  - EarlyQuitPenaltyLevel, TotalEarlyQuits, TotalCompletedMatches
  - MatchmakingTier (Tier 1 = good standing, Tier 2 = penalty)
  - Methods: IncrementEarlyQuit(), IncrementCompletedMatches(), UpdateTier()

- **Early Quit Recording Flow** (in `evr_match.go` ~line 622):
  1. When a player leaves during a match (before round ends)
  2. Load player's EarlyQuitConfig from storage
  3. Increment early quit count
  4. Check for tier change
  5. Send Discord DM if tier changed
  6. Write config back to storage

## Session Management
- `SessionRegistry` interface for managing active sessions
- `sessionRegistry.Get(sessionID)` returns `nil` if session doesn't exist
- Players logout when their session is removed from registry

## Code Style Conventions
- Uses `zap.Logger` for structured logging
- Error handling with explicit `if err != nil` checks
- Mutex locks for concurrent access to shared data structures
- Context-based cancellation and deadlines
- Comments on public functions and complex logic

## Build Commands
```bash
# Development build
GOWORK=off CGO_ENABLED=1 CGO_CFLAGS="-O0 -g" go build -trimpath -mod=vendor -ldflags "..." -o nakama

# Docker build
docker buildx build --build-arg VERSION=... -t ghcr.io/echotools/nakama:TAG .
```

## Testing
```bash
docker-compose -f ./docker-compose-tests.yml up --build --abort-on-container-exit
docker-compose -f ./docker-compose-tests.yml down -v
```

---

## Additional Notes
- Ensure to keep dependencies updated and check for security vulnerabilities regularly.
- Follow the contribution guidelines for any changes made to the codebase.
- Maintain documentation for any new features or changes introduced.
