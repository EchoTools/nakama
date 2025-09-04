# EchoTools Nakama AI Coding Agent Instructions

>This file guides AI agents to be productive in the EchoTools Nakama codebase. Focus on these project-specific conventions, workflows, and architecture patterns.

## Architecture Overview
- **Specialized Nakama fork** for Echo VR: adds Discord integration, VR matchmaking, and game server registry.
- **Key components:**
  - `/server/`: Core logic, API, VR/Discord/game server features
  - `/data/`: Runtime data, logs, modules
  - `/protocol/`: Protocol definitions and flows (see `README.md` for message sequences)
- **External dependencies:** PostgreSQL (port 5432), Discord bot (token required), Docker Compose for orchestration
- **Entry point:** `main.go` (server), `Makefile` (build), `docker-compose.yml` (dev/test infra)

## Critical Workflows
### Build & Run
- **Build:**
  ```bash
  go mod vendor
  make nakama
  ./nakama --version
  ```
- **Start DB:**
  ```bash
  docker compose up -d postgres
  sleep 30
  ./nakama migrate up --database.address postgres:localdb@127.0.0.1:5432/nakama
  ```
- **Run server:**
  ```bash
  export DISCORD_BOT_TOKEN=your_token
  ./nakama --name nakama1 --database.address postgres:localdb@127.0.0.1:5432/nakama --logger.level INFO
  ```

### Testing
- Only run: `go test -short -vet=off ./server/evr/...` and `go test -short -vet=off ./server -run ".*evr.*"`
- Cancel any operation >10min. Do not run benchmarks.

### Formatting
- Use `gofmt -w .` before commit. Check with `gofmt -l .`

### Manual Validation
- Test API: 
  ```bash
  curl "127.0.0.1:7350/v2/account/authenticate/device?create=true" --user "defaultkey:" --data '{"id": "test-device-123"}'
  ```
- Check DB connection: run server with `--logger.level DEBUG` and look for "Database information" log.

## Project Conventions
- **Discord bot token is required** for all local/dev runs.
- **All config** can be set via environment or `data/nakama.yml`.
- **Only `/server/` and `/data/` are significant** for code changes; other dirs are upstream or vendor.
- **Release builds** use xgo and Docker multi-arch (see `build/README.md`).
- **Protocol flows**: see `/protocol/README.md` for message sequence diagrams.

## Troubleshooting
- If server fails to start: check DB, Discord token, port conflicts (7349/7350/7351), and logs.
- If tests fail: ensure PostgreSQL is running and only run EVR tests.
- If build fails: clean vendor (`rm -rf vendor && go mod vendor`), check Go version (>=1.25).

## Example Commands
```bash
# Build and verify
make nakama && ./nakama --version
# Start DB and migrate
docker compose up -d postgres
sleep 30
./nakama migrate up --database.address postgres:localdb@127.0.0.1:5432/nakama
# Run server
export DISCORD_BOT_TOKEN=your_token
./nakama --database.address postgres:localdb@127.0.0.1:5432/nakama
# Run EVR tests
go test -short -vet=off ./server/evr/...
# Format
gofmt -w .
```

## References
- Main project docs: `README.md`, `/protocol/README.md`, `/build/README.md`
- Upstream Nakama docs: https://heroiclabs.com/docs/
- Discord bot setup: see "Discord Bot Setup" in `README.md`