# justfile for Nakama EVR
# Converted from the Makefile — run `just` instead of `make`.

# git metadata
COMMIT := `git rev-parse --short HEAD`
GIT_DESCRIBE := `git describe --tags --always --abbrev=7 --dirty`
TAG := `git describe --tags --exact-match 2>/dev/null || echo "dev"`
PWD := `pwd`

DEBUG_FLAGS := "-trimpath -gcflags \"-trimpath " + PWD + "\" -gcflags=\"all=-N -l\" -asmflags \"-trimpath " + PWD + "\""

# Connection string used by the DB-backed tests. Override to point at your own
# CockroachDB/Postgres: just TEST_DB_URL=postgresql://... test-db
TEST_DB_URL := env_var_or_default("TEST_DB_URL", "postgresql://root@127.0.0.1:26257/nakama?sslmode=disable")

# Build nakama (debug). Default target.
all: nakama

# Debug build of the nakama binary. Just has no make-style file prerequisites, so this always runs; go's incremental build cache keeps it fast when nothing changed.
nakama:
    CGO_ENABLED=1 CGO_CFLAGS="-O0 -g" go build \
        {{ DEBUG_FLAGS }} \
        -ldflags "-X main.version={{ GIT_DESCRIBE }} -X main.commitID={{ COMMIT }}" \
        -o nakama

# Docker build of the local image (no push)
build:
    docker buildx build \
        --build-arg VERSION={{ GIT_DESCRIBE }} \
        -t ghcr.io/echotools/nakama:{{ TAG }} . -f build/Dockerfile.local

# Docker buildx push; refuses to run when TAG is "dev". Override with: just TAG=v1.2.3 release (just takes variable assignments BEFORE the recipe name), or run from a tagged commit
release:
    @if [ "{{ TAG }}" = "dev" ]; then \
        echo "ERROR: TAG is 'dev'. Refusing to push release images."; \
        echo "Set TAG to a version (e.g. TAG=v1.2.3) or run from a tagged commit."; \
        exit 1; \
    fi
    docker buildx build --push \
        --build-arg VERSION={{ GIT_DESCRIBE }} \
        -t ghcr.io/echotools/nakama:{{ TAG }} \
        -t ghcr.io/echotools/nakama:latest \
        . -f build/Dockerfile.local

# Benchmark targets

# Create a benchmark baseline (~30 seconds)
bench-baseline:
    @echo "Creating benchmark baseline (this takes ~30 seconds)..."
    @mkdir -p _benchmarks
    @go test -run='^$' -bench='BenchmarkPredictOutcomes$' -benchmem -count=6 ./server/ 2>&1 | \
        grep -E '^(goos|goarch|pkg|cpu|Benchmark|PASS|ok)' > _benchmarks/predict_outcomes_baseline.txt
    @echo "Baseline saved to _benchmarks/predict_outcomes_baseline.txt"
    @$(go env GOPATH)/bin/benchstat _benchmarks/predict_outcomes_baseline.txt || \
        (echo "Installing benchstat..." && go install golang.org/x/perf/cmd/benchstat@latest && \
        $(go env GOPATH)/bin/benchstat _benchmarks/predict_outcomes_baseline.txt)

# Compare current benchmarks against the saved baseline
bench-compare:
    @./scripts/bench-compare.sh

# Run the benchmark comparison and confirm no regression
bench-check: bench-compare
    @echo "Benchmark regression check passed"

# Needs no CockroachDB and no Discord bot token: tests that require a database
# skip themselves when none is reachable.

# Run the DB-free server test suite
test:
    go test ./server/... -count=1

# Run the DB-free suite with verbose output.
test-verbose:
    go test -v ./server/... -count=1

# Requires a reachable CockroachDB/Postgres at TEST_DB_URL. TEST_DB_REQUIRED
# makes an unreachable database a hard failure instead of a silent skip, so this
# recipe cannot pass vacuously.

# Run the FULL suite, including the DB-backed tests
test-db:
    TEST_DB_URL="{{ TEST_DB_URL }}" TEST_DB_REQUIRED=1 go test ./server/... -count=1

# Formatting.
# Scope is repo-wide: every *tracked* Go file except generated sources.
#
# Tracked-only is deliberate. vendor/ is not gitignored but is never committed,
# so `git ls-files` structurally cannot sweep it in — even on a machine where
# `go mod vendor` has been run. (Listing untracked files instead would drag the
# whole vendor tree in.) The tradeoff: a brand-new file is not checked until it
# is `git add`ed. That is harmless, since CI checks out a commit, where every
# file in the PR is tracked.
#
# The grep drops files carrying a "Code generated ... DO NOT EDIT" header —
# those are owned by their generators (protobuf, grpc-gateway, gopher-lua
# parser), not by us.
#
# Plain gofmt, not gofumpt: standard-library-canonical formatting, nothing more
# opinionated.
FMT_FILES := "git ls-files '*.go' | xargs grep -LE 'Code generated .* DO NOT EDIT'"

# Format all non-generated Go sources in place (prints the files it rewrote)
fmt:
    @{{ FMT_FILES }} | xargs gofmt -w -l

# Verify all non-generated Go sources are gofmt-clean; non-zero exit on failure
fmt-check:
    @unformatted="$({{ FMT_FILES }} | xargs gofmt -l)"; \
    if [ -n "$unformatted" ]; then \
        echo "ERROR: these files are not gofmt-formatted:"; \
        echo "$unformatted" | sed 's/^/  /'; \
        echo ""; \
        echo "Fix with: just fmt"; \
        exit 1; \
    fi; \
    echo "gofmt: all non-generated Go sources are formatted"

# GitHub Actions local testing with act.
# Use medium image for better compatibility (default is too minimal).
ACT_FLAGS := env_var_or_default("ACT_FLAGS", "--container-architecture linux/amd64")

# List all available GitHub Actions workflows and jobs
act-list:
    @act -l

# Run the build workflow locally
act-build:
    @act -j build_binary {{ ACT_FLAGS }}

# Run the tests workflow locally
act-tests:
    @act -j run_tests {{ ACT_FLAGS }}

# Validate GitHub Actions workflow syntax
act-lint:
    @command -v actionlint >/dev/null 2>&1 || (echo "Installing actionlint..." && go install github.com/rhysd/actionlint/cmd/actionlint@latest)
    @actionlint .github/workflows/*.yml .github/workflows/*.yaml

# Alias for act-list (show available workflows)
act: act-list
