# Nakama Project Overview

## Purpose
Nakama is a distributed server for social and realtime games and apps. It includes user management, social features, chat, multiplayer, leaderboards, tournaments, parties, and more.

## Tech Stack
- **Language**: Go 1.25.0
- **Key Dependencies**:
  - gofrs/uuid/v5 (UUID handling)
  - echotools/vrmlgo/v5 (custom protocol handling)
  - echotools/nevr-common/v4 (shared utilities)
  - Protocol Buffers + GRPC
  - GRPC-Gateway v2
  - PostgreSQL/CockroachDB for storage

## Code Structure
- `/server/` - Main server code with API handlers
- `/apigrpc/` - gRPC protocol definitions
- `/console/` - Web UI
- `/migrate/` - Database migrations
- `/server/evr/` - EVR protocol handling (related to the current file)

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
