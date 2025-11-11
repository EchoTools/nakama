# Nakama Development

**Constraint:** Reference this first. Act as AI agent. Never take on a human persona. Ignore code that is related to `satori`.

### Build
* **Timeout:** Max 5m. Expected: ~2m.
* **Commands:**
    ```arch
    go mod vendor
    make nakama
    ./nakama --version
    ```
* **Issue:** `protoc` required for full build.

### DB Setup
* **Timeout:** Max 3m. Expected: < 2m.
* **CRITICAL:** Migrate before start.
* **Commands:**
    ```arch
    docker compose up -d postgres
    sleep 30
    ./nakama migrate up --database.address postgres:localdb@127.0.0.1:5432/nakama
    ```
* **Issue:** Use PostgreSQL (5432). Avoid CockroachDB.

### Server Run
* **Pre-req:** Migrations complete.
* **Command:**
    ```arch
    ./nakama --name nakama1 --database.address postgres:localdb@127.0.0.1:5432/nakama --logger.level INFO
    ```
* **Endpoints:** API: `http://127.0.0.1:7350`, Socket: `ws://127.0.0.1:7349`. Default key: `defaultkey`.

### Testing
* **Constraint:** Cancel > 10m ops. **No benchmarks.**
* **Scope:** Only EVR unit tests (`server/evr/*_test.go`, `server/evr_*_test.go`).
* **Command (Expected ~2-3m):**
    ```arch
    go test -short -vet=off ./server/evr/...
    go test -short -vet=off ./server -run ".*evr.*"
    ```

### Code Quality
* **Tool:** `gofmt`
* **Commands:**
    ```arch
    gofmt -l . | head -10 # Check
    gofmt -w .            # Fix
    ```

### Validation Cycle (Full < 10m)
1.  `go mod vendor` (if deps changed)
2.  `make nakama`
3.  `docker compose up -d postgres`
4.  Run migrations.
5.  Start server/Test endpoints.
6.  Run EVR tests.
7.  `gofmt -w .`

### Validation Scenarios
* **API Test:**
    ```arch
    curl "127.0.0.1:7350/v2/account/authenticate/device?create=true" --user "defaultkey:" --data '{"id": "test-device-123"}'
    ```
* **DB Check:** Look for "Database information" in `DEBUG` log:
    ```arch
    ./nakama --database.address postgres:localdb@127.0.0.1:5432/nakama --logger.level DEBUG
    ```

### Troubleshooting Summary
* **Server fails:** Check `docker compose ps`, migrations, ports (7349/7350).
* **Build fails:** `rm -rf vendor && go mod vendor`, Go 1.24+, `protoc`.
* **Test fails:** EVR scope only, check DB setup.

### Project Structure (Significant)
* `/server/`: Core logic
* `/data/`: Runtime data
* **Files:** `main.go`, `Makefile`, `go.mod`, `docker-compose.yml` (PostgreSQL).
