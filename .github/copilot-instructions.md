# Nakama Game Server Development Instructions

Always reference these instructions first and fallback to search or bash commands only when you encounter unexpected information that does not match the info here.

## Working Effectively

### Bootstrap and Build (NEVER CANCEL - Build takes up to 60 minutes)
- **CRITICAL**: All build commands can take 45-60 minutes. Set timeout to 90+ minutes and NEVER CANCEL.
- Bootstrap, build, and test the repository:
  ```bash
  # Download dependencies (takes ~1 minute)
  go mod vendor
  
  # Simple build (takes ~1 minute, recommended for most development)
  go build -trimpath -mod=vendor
  
  # Production build with debug info (takes ~1 minute)
  make nakama
  
  # Verify build
  ./nakama --version
  ```

### Database Setup (NEVER CANCEL - Database setup takes 5+ minutes)
- **CRITICAL**: Database operations can take 10+ minutes. Set timeout to 20+ minutes and NEVER CANCEL.
- Start PostgreSQL database:
  ```bash
  # Start database (takes ~30 seconds)
  docker compose up -d postgres
  
  # Wait for database to be ready (takes ~30 seconds)
  sleep 30
  
  # Run migrations (takes ~1 second)
  ./nakama migrate up --database.address postgres:localdb@127.0.0.1:5432/nakama
  ```

### Run the Server
- **IMPORTANT**: Always run migration before starting server.
- Start Nakama server:
  ```bash
  # Start server with PostgreSQL
  ./nakama --name nakama1 --database.address postgres:localdb@127.0.0.1:5432/nakama --logger.level INFO
  ```
- Server URLs:
  - API: http://127.0.0.1:7350
  - Console: http://127.0.0.1:7351 (username: admin, password: password)
  - Socket: ws://127.0.0.1:7349

### Testing (NEVER CANCEL - Tests take 30+ minutes)
- **CRITICAL**: Tests can take 30-45 minutes. Set timeout to 60+ minutes and NEVER CANCEL.
- **WARNING**: Full Docker Compose tests may fail due to TLS proxy issues. Use local tests instead.
- Run unit tests without database:
  ```bash
  # Quick unit tests (takes ~1 minute, some may fail - this is expected)
  go test -short -vet=off ./internal/...
  ```
- Docker-based testing (may fail):
  ```bash
  # Full integration tests (takes 30+ minutes, may fail due to proxy issues)
  # NEVER CANCEL - Let it complete even if it takes 45+ minutes
  docker compose -f ./docker-compose-tests.yml up --build --abort-on-container-exit
  docker compose -f ./docker-compose-tests.yml down -v
  ```

### Code Quality and Linting
- **WARNING**: golangci-lint config has version compatibility issues. Use gofmt instead.
- Run formatting and basic linting:
  ```bash
  # Format check (takes ~1 second)
  gofmt -l . | head -10
  
  # Fix formatting
  gofmt -w .
  ```

### Console UI (Angular) - KNOWN ISSUES
- **WARNING**: Console UI has Angular version conflicts. Build may fail.
- Build console (takes 2+ minutes, may fail):
  ```bash
  cd console/ui
  
  # Install dependencies (takes ~2 minutes)
  npm install --legacy-peer-deps
  
  # Build (may fail due to Angular version mismatch)
  npm run build
  ```
- **WORKAROUND**: If console build fails, the server still works. Console is pre-built in most cases.

## Validation Scenarios

### Always manually validate changes with these scenarios:

1. **Basic API Test**:
   ```bash
   # Start server first, then test authentication
   curl "127.0.0.1:7350/v2/account/authenticate/device?create=true" \
     --user "defaultkey:" \
     --data '{"id": "test-device-123"}'
   ```

2. **Console Access**:
   - Navigate to http://127.0.0.1:7351
   - Login with admin/password
   - Verify console loads without errors

3. **Database Connectivity**:
   ```bash
   # Check database connection
   ./nakama --database.address postgres:localdb@127.0.0.1:5432/nakama --logger.level DEBUG
   # Look for "Database information" log message
   ```

## Known Issues and Workarounds

### Build Issues
- **golangci-lint version conflict**: Use gofmt instead
- **Angular version mismatch**: Use --legacy-peer-deps for npm install
- **Docker test proxy errors**: Use local unit tests instead
- **Missing protoc**: Full source builds require Protocol Buffers compiler

### Database Issues
- **CockroachDB connection errors**: Use PostgreSQL instead (docker compose up postgres)
- **Migration failures**: Ensure database is fully started before running migrations

### Testing Issues
- **Unit tests expect CockroachDB**: Many tests fail without CockroachDB on port 26257
- **Lua test failures**: Some Lua runtime tests fail due to missing test files

## Development Workflow

### Quick Development Cycle
1. Make code changes
2. Run `make nakama` (takes ~1 minute)
3. Test with `./nakama --version`
4. Start server and validate manually
5. Run `gofmt -w .` before committing

### Full Validation Cycle (takes 60+ minutes)
1. `go mod vendor` (if dependencies changed)
2. `make nakama`
3. Start database: `docker compose up -d postgres`
4. Run migrations
5. Start server and test API endpoints
6. Run available unit tests
7. Format code with gofmt

## Project Structure

### Key Directories
- `/server/`: Core server logic, API implementations
- `/apigrpc/`: gRPC and Protocol Buffer definitions
- `/console/`: Angular-based web console UI
- `/internal/`: Internal packages (skiplist, cronexpr, gopher-lua)
- `/data/`: Runtime data directory (modules, logs)
- `/build/`: Docker files and build scripts

### Important Files
- `main.go`: Server entry point
- `Makefile`: Build configuration with debug symbols
- `go.mod`: Go dependencies
- `docker-compose.yml`: PostgreSQL database setup
- `docker-compose-tests.yml`: Testing environment setup

### Configuration
- Default console credentials: admin/password
- Default database: PostgreSQL on port 5432
- API port: 7350, Console port: 7351, Socket port: 7349
- Default server key: "defaultkey"

## Common Commands Reference

```bash
# Quick build and test cycle
make nakama && ./nakama --version

# Start development environment
docker compose up -d postgres
./nakama migrate up --database.address postgres:localdb@127.0.0.1:5432/nakama
./nakama --database.address postgres:localdb@127.0.0.1:5432/nakama

# Clean build
rm nakama && make nakama

# Format code
gofmt -w .

# Check for formatting issues
gofmt -l .

# Basic API test
curl "127.0.0.1:7350/v2/account/authenticate/device?create=true" \
  --user "defaultkey:" \
  --data '{"id": "test-device-123"}'
```

## Troubleshooting

### Server won't start
- Check PostgreSQL is running: `docker compose ps`
- Verify migrations ran: Check for "Successfully applied migration" message
- Check port conflicts: Ensure ports 7349, 7350, 7351 are available

### Build failures
- Clean vendor directory: `rm -rf vendor && go mod vendor`
- Check Go version: `go version` (requires Go 1.24+)
- Verify all dependencies downloaded: Look for any network errors

### Test failures
- Database tests fail: Expected, requires CockroachDB setup
- Lua tests fail: Expected, missing test files
- Proxy errors: Network connectivity issue, try local tests only

## Time Expectations

- Go mod vendor: 1 minute (first time)
- Simple build: 1 minute  
- Makefile build: 1 minute
- Database startup: 30 seconds
- Server startup: 2 seconds
- Migration: 1 second
- npm install (console): 2 minutes
- Unit tests: 1-30 minutes (many will fail without proper setup)
- Full integration tests: 30-45 minutes (may fail due to proxy issues)

**REMEMBER**: NEVER CANCEL long-running operations. Builds and tests can take much longer than expected.