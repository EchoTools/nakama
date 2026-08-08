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

# Per-test-binary memory cap. See scripts/go-test-limit.sh for the rationale:
# a runaway test must die itself rather than letting the machine-wide OOM killer
# pick an unrelated victim. Raise for one run with:
#   just GO_TEST_MEMORY_LIMIT=8G test        (or GO_TEST_MEMORY_LIMIT=off to disable)
#
# The path must be ABSOLUTE: `go test -exec` runs the wrapper with the working
# directory set to the package under test, so a relative path fails to resolve.
GO_TEST_MEMORY_LIMIT := env_var_or_default("GO_TEST_MEMORY_LIMIT", "4G")
TEST_LIMIT_FLAG := "-exec=" + justfile_directory() + "/scripts/go-test-limit.sh"

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
    GOFLAGS="${GOFLAGS:-} {{ TEST_LIMIT_FLAG }}" GO_TEST_MEMORY_LIMIT="{{ GO_TEST_MEMORY_LIMIT }}" \
        go test ./server/... -count=1

# Run the DB-free suite with verbose output.
test-verbose:
    GOFLAGS="${GOFLAGS:-} {{ TEST_LIMIT_FLAG }}" GO_TEST_MEMORY_LIMIT="{{ GO_TEST_MEMORY_LIMIT }}" \
        go test -v ./server/... -count=1

# Requires a reachable CockroachDB/Postgres at TEST_DB_URL. TEST_DB_REQUIRED
# makes an unreachable database a hard failure instead of a silent skip, so this
# recipe cannot pass vacuously.

# Run the FULL suite, including the DB-backed tests
test-db:
    TEST_DB_URL="{{ TEST_DB_URL }}" TEST_DB_REQUIRED=1 \
        GOFLAGS="${GOFLAGS:-} {{ TEST_LIMIT_FLAG }}" GO_TEST_MEMORY_LIMIT="{{ GO_TEST_MEMORY_LIMIT }}" \
        go test ./server/... -count=1

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

# ---------------------------------------------------------------------------
# Hook arming.
#
# `.githooks/pre-push` ships with the repo but does NOT activate itself.
# core.hooksPath is local config and cannot be committed, so until something
# sets it, git never looks at .githooks and every push is unguarded -- with no
# warning, because a hook that is not wired up is indistinguishable from one
# that approved. A fresh clone, a newly created worktree, and an agent starting
# cold are all unarmed by default, which is exactly the population the
# destination guard exists for.
#
# just evaluates variables before running any recipe, so this arms the clone on
# the first `just <anything that runs>` -- `just test`, `just nakama`,
# `just fmt`. The unguarded window shrinks from "until someone reads AGENTS.md
# and runs a git config command" to "until someone runs one just recipe", and
# running a recipe is the first thing anyone working here does.
#
# IT DOES NOT CLOSE THE WINDOW, AND NOTHING CAN. Git deliberately refuses to let
# a repository activate its own hooks: a clone that armed itself would be
# arbitrary code execution on `git clone`. That is a security boundary, not an
# oversight, so "the hook arms itself" is not achievable at any level of effort
# -- only "the hook is armed earlier, by something the user already runs".
# Whoever clones and pushes without ever running a recipe is still unguarded.
# The self-arming backstop for that case is
# .github/workflows/main-push-audit.yaml, which is server-side and cannot be
# skipped -- but detects after the push has landed rather than preventing it.
#
# `just --list` alone does NOT arm: just evaluates variables lazily and --list
# does not trigger them (verified, just 1.57). That is acceptable -- --list does
# not push anything.
#
# Backticks run with the working directory set to the justfile's directory
# regardless of where just was invoked from, and regardless of -f (verified,
# just 1.57), so this cannot arm the wrong repository.
#
# Opt out with NAKAMA_NO_AUTO_HOOKS=1. Every failure path here is non-fatal: a
# missing git, or a directory that is not a repository, must never break
# `just test`.
_HOOKS_ARMED := ```
    if [ -n "${NAKAMA_NO_AUTO_HOOKS:-}" ]; then
        echo opted-out
    elif [ ! -x .githooks/pre-push ]; then
        # Nothing to point at, or it is not executable. Git skips a
        # non-executable hook SILENTLY, so arming toward one would install the
        # appearance of a guard without the guard. See exec-bit-check.
        echo unavailable
    elif [ "$(git config --get core.hooksPath 2>/dev/null || true)" = ".githooks" ]; then
        echo armed
    elif git config core.hooksPath .githooks 2>/dev/null; then
        # Announced once, on the run that changes it, and silent forever after.
        echo "git hooks armed: core.hooksPath -> .githooks (pre-push guards now active)" >&2
        echo armed
    else
        echo unavailable
    fi
```

# Point git at the repo's tracked hooks (.githooks) and report the result.
#
# Recipes arm the clone on their own (see _HOOKS_ARMED above). This recipe
# remains the explicit form: it is what to run after NAKAMA_NO_AUTO_HOOKS, what
# to point someone at, and what answers "is this clone guarded?" without having
# to infer it from silence.
hooks:
    @git config core.hooksPath .githooks
    @echo "core.hooksPath -> $(git config --get core.hooksPath)"
    @echo "auto-arm status: {{ _HOOKS_ARMED }}"

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

# Executable bits.
# Scripts documented as `./script.sh` must be tracked 100755, or they arrive
# non-executable in every fresh clone and the invocation fails.
#
# This rots invisibly. Git records only the owner-x bit (100755 vs 100644), and
# this clone carried core.fileMode=false for a while, which tells git to ignore
# on-disk modes entirely: a script could be executable on disk, be committed as
# 100644, and `git status` would never say a word. That is how
# scripts/bench-compare.sh — run by `just bench-compare` — shipped broken.
#
# `git ls-files -s` reads the mode out of the index, so this check is immune to
# core.fileMode and gives the same answer locally and in CI.
#
# build/do-marketplace/scripts/ is exempt: packer's shell provisioner uploads
# each script to the build droplet and chmods it there, so the tracked mode is
# irrelevant. That directory's own 01-test says so in its header comment.
EXEC_BIT_EXEMPT := "^build/do-marketplace/scripts/"

# Verify every tracked *.sh and .githooks/* with a shebang is tracked executable;
# non-zero exit on failure.
#
# .githooks/ is in scope because a git hook that is not executable does not run
# AND does not complain — git skips it silently. A guard that silently stops
# guarding is worse than no guard, since the absence of a refusal reads as
# permission.
exec-bit-check:
    @nonexec="$(git ls-files -s '*.sh' '.githooks/*' | grep -v '^100755' | cut -f2 \
        | grep -vE '{{ EXEC_BIT_EXEMPT }}' \
        | while read -r f; do if [ "$(head -c 2 "$f")" = '#!' ]; then echo "$f"; fi; done)"; \
    if [ -n "$nonexec" ]; then \
        echo "ERROR: these shell scripts have a shebang but are not tracked executable (100755):"; \
        echo "$nonexec" | sed 's/^/  /'; \
        echo ""; \
        echo "Fix with: git update-index --chmod=+x <file>"; \
        echo "A plain chmod is NOT enough — it is not recorded when core.fileMode=false."; \
        exit 1; \
    fi; \
    echo "exec bits: every tracked *.sh and .githooks/* with a shebang is 100755"

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
