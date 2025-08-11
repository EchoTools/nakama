# Nakama Game Server Development Instructions

Always reference these instructions first and fallback to search or bash commands only when you encounter unexpected information that does not match the info here.

## Working Effectively

### Bootstrap and Build
- Build commands should never take more than 3 minutes. Set timeout to 5 minutes maximum.
- Build the repository:
  ```bash
  # Download dependencies (takes ~1 minute)
  go mod vendor
  
  # Build (takes ~1 minute)
  make nakama
  
  # Verify build
  ./nakama --version
  ```

### Database Setup
- Database operations should take less than 2 minutes. Set timeout to 3 minutes maximum.
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
  - Socket: ws://127.0.0.1:7349

### Testing
- Most tests take less than a few minutes. Do not run benchmarks.
- Cancel all operations that take more than 10 minutes.
- Only run server/evr/*_test.go and server/evr_*_test.go unit tests:
  ```bash
  # Run EVR unit tests (takes ~2-3 minutes)
  go test -short -vet=off ./server/evr/...
  go test -short -vet=off ./server -run ".*evr.*"
  ```

### Code Quality and Linting
- Use gofmt for formatting:
  ```bash
  # Format check (takes ~1 second)
  gofmt -l . | head -10
  
  # Fix formatting
  gofmt -w .
  ```


## Validation Scenarios

### Always manually validate changes with these scenarios:

1. **Basic API Test**:
   ```bash
   # Start server first, then test authentication
   curl "127.0.0.1:7350/v2/account/authenticate/device?create=true" \
     --user "defaultkey:" \
     --data '{"id": "test-device-123"}'
   ```

2. **Database Connectivity**:
   ```bash
   # Check database connection
   ./nakama --database.address postgres:localdb@127.0.0.1:5432/nakama --logger.level DEBUG
   # Look for "Database information" log message
   ```

## Known Issues and Workarounds

### Build Issues
- **Missing protoc**: Full source builds require Protocol Buffers compiler

### Database Issues
- **CockroachDB connection errors**: Use PostgreSQL instead (docker compose up postgres)
- **Migration failures**: Ensure database is fully started before running migrations

### Testing Issues
- Only run server/evr/*_test.go and server/evr_*_test.go unit tests

## Development Workflow

### Quick Development Cycle
1. Make code changes
2. Run `make nakama` (takes ~1 minute)
3. Test with `./nakama --version`
4. Start server and validate manually
5. Run `gofmt -w .` before committing

### Full Validation Cycle (takes under 10 minutes)
1. `go mod vendor` (if dependencies changed)
2. `make nakama`
3. Start database: `docker compose up -d postgres`
4. Run migrations
5. Start server and test API endpoints
6. Run EVR unit tests: `go test -short -vet=off ./server/evr/...`
7. Format code with gofmt

## Project Structure

### Key Directories
- `/server/`: Core server logic, API implementations (significant)
- `/data/`: Runtime data directory (modules, logs) (significant)

*Note: All other directories are handled upstream and are not significant for development.*

### Important Files
- `main.go`: Server entry point
- `Makefile`: Build configuration with debug symbols
- `go.mod`: Go dependencies
- `docker-compose.yml`: PostgreSQL database setup
- `docker-compose-tests.yml`: Testing environment setup

### Configuration
- Default database: PostgreSQL on port 5432
- API port: 7350, Socket port: 7349
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

# Run EVR tests
go test -short -vet=off ./server/evr/...

# Basic API test
curl "127.0.0.1:7350/v2/account/authenticate/device?create=true" \
  --user "defaultkey:" \
  --data '{"id": "test-device-123"}'
```

## Troubleshooting

### Server won't start
- Check PostgreSQL is running: `docker compose ps`
- Verify migrations ran: Check for "Successfully applied migration" message
- Check port conflicts: Ensure ports 7349, 7350 are available

### Build failures
- Clean vendor directory: `rm -rf vendor && go mod vendor`
- Check Go version: `go version` (requires Go 1.24+)
- Verify all dependencies downloaded: Look for any network errors

### Test failures
- Only run server/evr/*_test.go and server/evr_*_test.go tests
- Database tests may fail without proper PostgreSQL setup

## Time Expectations

- Go mod vendor: 1 minute (first time)
- Build: 1 minute  
- Database startup: 30 seconds
- Server startup: 2 seconds
- Migration: 1 second
- EVR unit tests: 2-3 minutes

**REMEMBER**: Cancel all operations that take more than 10 minutes.