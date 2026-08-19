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

# ---------------------------------------------------------------------------
# Test scope.
#
# These recipes used to run `./server/...`, which meant `internal/` was covered
# by NO routine gate. That is not a theoretical hole: `TestIntent_MarshalText`
# sat red on main and nobody got a red signal, because nothing anyone runs
# executed the package (#554).
#
# The scope is now discovered rather than enumerated, so a package added under
# internal/ tomorrow is gated tomorrow, without anyone remembering to add it.
#
# internal/gopher-lua is the one exclusion, and it is vendored third-party code
# rather than ours. Including it would cost more than it is worth, measured:
#
#   1. `go test` runs a subset of vet, and gopher-lua has 19 non-constant-format
#      -string findings, so the package does not even BUILD under test. Covering
#      it means `-vet=off`, which would disable printf/atomic/bool/... checks on
#      OUR code in order to accommodate vendored code. That is turning a
#      fail-closed control off to keep a green light on.
#   2. Its Lua 5.1 conformance suite needs a fixture directory
#      (_lua5.1-tests/libs/) that git cannot track because it is empty on
#      checkout, so the suite is red on a fresh clone until someone mkdirs it.
#
# docker-compose-tests.yml already carries both workarounds (`-vet=off` and a
# volume for that directory), so gopher-lua is covered there and only there.
# Excluded here, deliberately and visibly -- not silently dropped.
#
# The trailing fallback is load-bearing. If `go list` fails or the filter ever
# matches everything, the substitution is empty -- and `go test -count=1` with
# no package arguments does not error, it tests the current directory, which is
# a package with no test files. That is a green run over nothing: the exact
# failure mode this scope change exists to remove. Substituting a path that
# cannot exist turns that silence into a hard, self-naming failure.
TEST_PKGS := "$(go list ./... | grep -v '/internal/gopher-lua' | grep . || echo ./TEST_PKGS_MATCHED_NO_PACKAGES)"

# Needs no CockroachDB and no Discord bot token: tests that require a database
# skip themselves when none is reachable.

# Run the DB-free test suite
test:
    GOFLAGS="${GOFLAGS:-} {{ TEST_LIMIT_FLAG }}" GO_TEST_MEMORY_LIMIT="{{ GO_TEST_MEMORY_LIMIT }}" \
        go test {{ TEST_PKGS }} -count=1

# Run the DB-free suite and refuse a green result that covered almost nothing.
#
# Same suite as `test`, plus a coverage floor per package and a visible count of
# what skipped. This is what CI runs, because `ok` is not by itself a claim
# about how much executed -- the `server` package once reported in 0.07s
# instead of ~130s (#553), and a package that SKIPS its way to empty reports
# success just as loudly. See scripts/test-audit.sh for why the floor is a test
# count and not a duration.
test-audit:
    GOFLAGS="${GOFLAGS:-} {{ TEST_LIMIT_FLAG }}" GO_TEST_MEMORY_LIMIT="{{ GO_TEST_MEMORY_LIMIT }}" \
        TEST_PKGS="{{ TEST_PKGS }}" ./scripts/test-audit.sh

# Run the DB-free suite with verbose output.
test-verbose:
    GOFLAGS="${GOFLAGS:-} {{ TEST_LIMIT_FLAG }}" GO_TEST_MEMORY_LIMIT="{{ GO_TEST_MEMORY_LIMIT }}" \
        go test -v {{ TEST_PKGS }} -count=1

# Requires a reachable CockroachDB/Postgres at TEST_DB_URL. TEST_DB_REQUIRED
# makes an unreachable database a hard failure instead of a silent skip, so this
# recipe cannot pass vacuously.

# Run the FULL suite, including the DB-backed tests
test-db:
    TEST_DB_URL="{{ TEST_DB_URL }}" TEST_DB_REQUIRED=1 \
        GOFLAGS="${GOFLAGS:-} {{ TEST_LIMIT_FLAG }}" GO_TEST_MEMORY_LIMIT="{{ GO_TEST_MEMORY_LIMIT }}" \
        go test {{ TEST_PKGS }} -count=1

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

# ---------------------------------------------------------------------------
# Static analysis, and the one entry point everything resolves against.
#
# AGENTS.md has listed `golangci-lint` as a MUST-run-before-committing gate for
# years. It was a gate in exactly zero places: not in .githooks/pre-push, not in
# .github/workflows/build.yaml, and there was no recipe at all -- so the only
# thing enforcing it was prose, and for three of those years the config would not
# even load (a0c12bae8). `just verify` is where that stops being true.

# Uncapped. Bare `golangci-lint run` applies max-issues-per-linter=50 and
# max-same-issues=3 and reports 153 of the findings present, which looks exactly
# like a cleaner tree. Every number this repo records is taken with these flags.
LINT_FLAGS := "--max-issues-per-linter 0 --max-same-issues 0"

# Per-checkout lint cache, keyed on the checkout's own path.
#
# golangci-lint defaults to one shared ~/.cache/golangci-lint for every checkout
# on the machine. Two consequences, both observed on 2026-08-19 with two cogs
# working in sibling worktrees of this repo:
#
#   1. It takes an exclusive lock. The second run dies with
#      `Error: parallel golangci-lint is running` -- so one worktree linting
#      blocks every other worktree, including CI-shaped local runs.
#   2. Worse, and silent: the cache is keyed in a way that let one worktree's
#      results surface in another's report, carrying THAT worktree's paths. One
#      cog cleaned the cache, and it re-poisoned within the same session with
#      paths under `../agent-abdd5b1f3c3316791/...`. That is AGENTS.md defect
#      class 6 arriving from a live sibling rather than from a stale directory,
#      which the foreign-path guard below catches but cannot prevent.
#
# A cache per checkout removes both. Cost is real and worth naming: each
# checkout pays its own cold run (~40s) and ~12MB. Under /var/tmp, not /tmp --
# /tmp is RAM-backed here.
#
# An explicitly set GOLANGCI_LINT_CACHE wins, so this can still be overridden.
LINT_CACHE := "/var/tmp/nakama-golangci-cache/" + sha256(justfile_directory())

# The backlog ratchet. Measured, not chosen: 290 with a cold cache, down
# from 374 at 17b79b0fd. BOLT 9 cleared 30 (govet inline 18, SA1019 8, SA1006 2,
# S1011 1, S1002 1); BOLT 2 cleared 54 (the discarded RestrictAPIFunctionAccess
# returns in registerAPIGuards, all errcheck). It is a
# CEILING, not a target -- `just lint` fails if the count rises and tells you to
# lower this number when it falls. It exists only until the backlog reaches zero,
# at which point this variable and the whole comparison are DELETED and a bare
# non-zero exit becomes the gate. A ratchet kept past zero is furniture
# (AGENTS.md defect class 4).
LINT_BASELINE := "289"

# Run the uncapped linter and hold the backlog ratchet; non-zero if it rises.
#
# Three failure modes are handled explicitly, because each has already happened
# here or is one keystroke away:
#
#   1. THE LINTER DID NOT RUN. golangci-lint exits 0 with no issues, 1 with
#      issues, and something else on a config or usage error. From 2023 to
#      2026-08-16 it exited 3 on every invocation ("unsupported version of the
#      configuration") and the workflow that called it had been failing the same
#      way, unnoticed. Anything other than 0 or 1 is a hard failure here, with
#      the output printed -- never a silent zero-issue pass.
#
#   2. THE FINDINGS ARE NOT ABOUT THIS TREE. AGENTS.md defect class 6: a stale
#      analyzer cache made this command report 447 findings against 374 actually
#      present, 123 of them citing paths under a /var/tmp scratch copy that no
#      longer existed. It also silently defeated the generated-file exclusion,
#      since detecting "DO NOT EDIT." requires READING the file -- one generated
#      protobuf contributed 71 phantom findings on its own. golangci-lint emits
#      paths relative to the repo root, so a leading `/` or `../` means the
#      finding is not about this tree. Hard failure, with the fix.
#
#   3. THE BACKLOG SILENTLY GREW. That is the ratchet below.

# Uncapped golangci-lint; non-zero if the backlog rises above LINT_BASELINE
lint:
    @set -u; \
    export GOLANGCI_LINT_CACHE="${GOLANGCI_LINT_CACHE:-{{ LINT_CACHE }}}"; \
    out="$(golangci-lint run {{ LINT_FLAGS }} 2>&1)"; rc=$?; \
    if [ "$rc" != "0" ] && [ "$rc" != "1" ]; then \
        echo "ERROR: golangci-lint exited $rc -- it did not run, it failed."; \
        echo "A zero-issue result from a linter that never ran is the failure"; \
        echo "mode this check exists for. Output follows:"; \
        printf '%s\n' "$out" | sed 's/^/  /'; \
        exit 1; \
    fi; \
    foreign="$(printf '%s\n' "$out" | grep -oE '^(/|\.\./)[^ :]*\.go:[0-9]+:[0-9]+:' | sort -u)"; \
    if [ -n "$foreign" ]; then \
        echo "ERROR: golangci-lint reported findings whose paths are not in this repo:"; \
        printf '%s\n' "$foreign" | head -5 | sed 's/^/  /'; \
        echo "  ... $(printf '%s\n' "$foreign" | wc -l | tr -d ' ') distinct foreign paths"; \
        echo ""; \
        echo "This is a stale analyzer cache (AGENTS.md defect class 6). The count"; \
        echo "is inflated and the file:line citations point at nothing."; \
        echo "Fix with: golangci-lint cache clean"; \
        exit 1; \
    fi; \
    count="$(printf '%s\n' "$out" | grep -cE '^[^ ]+\.go:[0-9]+:[0-9]+: ')"; \
    if [ "$count" -gt "{{ LINT_BASELINE }}" ]; then \
        echo "ERROR: lint findings rose to $count, above the {{ LINT_BASELINE }} baseline."; \
        printf '%s\n' "$out" | tail -20 | sed 's/^/  /'; \
        echo ""; \
        echo "See what THIS branch added with:  just lint-new"; \
        echo "See the full report with:         golangci-lint run {{ LINT_FLAGS }}"; \
        exit 1; \
    fi; \
    if [ "$count" -lt "{{ LINT_BASELINE }}" ]; then \
        echo "lint: $count findings -- BELOW the {{ LINT_BASELINE }} baseline."; \
        echo "Lower LINT_BASELINE in the justfile to $count in this same commit,"; \
        echo "or the ground you just took is given back to the next change."; \
        exit 1; \
    fi; \
    echo "lint: $count findings, at the {{ LINT_BASELINE }} baseline (0 foreign paths)"

# Findings introduced by the current branch, regardless of the backlog.
#
# This is the check that actually holds the line, and it is what CI runs on a
# pull request: the ratchet above only catches a NET rise, so fixing one old
# finding while adding one new one passes it. --new-from-merge-base does not
# care about the backlog at all, only about what this branch added.
#
# Requires full history (actions/checkout fetch-depth: 0).

# Lint only what this branch added, against REF; non-zero on any new finding
lint-new REF="origin/main":
    @GOLANGCI_LINT_CACHE="${GOLANGCI_LINT_CACHE:-{{ LINT_CACHE }}}" \
        golangci-lint run {{ LINT_FLAGS }} --new-from-merge-base {{ REF }} \
        && echo "lint: no new findings against {{ REF }}"

# go vet over the same scope the tests use.
#
# NOT `go vet ./...`, which AGENTS.md prescribed until 2026-08-19 and which
# cannot pass: it walks the vendored internal/gopher-lua, whose 25 findings are
# not ours to fix. A mandatory command that can never exit 0 does not get
# satisfied, it gets ignored. See TEST_PKGS above for the same exclusion and for
# why the `grep .` fallback in it is load-bearing.

# go vet over the test scope (not ./..., which cannot pass); non-zero on failure
vet:
    @go vet {{ TEST_PKGS }} && echo "vet: clean over the test scope"

# Refuse a tree whose go.mod/go.sum do not survive `go mod tidy`.
#
# `go mod tidy` rewrites both files in place, so this snapshots and restores them
# whatever the outcome -- a check that mutates the tree it is checking leaves the
# developer with changes they did not make. .githooks/pre-push check 5 does the
# same thing for the same reason; this is that check, available without a push.

# Verify go.mod/go.sum survive `go mod tidy` unchanged; restores them either way
mod-tidy-check:
    @set -u; \
    backup="$(mktemp -d)"; \
    cp go.mod go.sum "$backup/"; \
    go mod tidy; \
    drift="$(git status --porcelain -- go.mod go.sum)"; \
    cp "$backup/go.mod" "$backup/go.sum" .; \
    rm -rf "$backup"; \
    if [ -n "$drift" ]; then \
        echo "ERROR: go.mod/go.sum are not tidy:"; \
        printf '%s\n' "$drift" | sed 's/^/  /'; \
        echo "Fix with: go mod tidy"; \
        exit 1; \
    fi; \
    echo "mod tidy: go.mod and go.sum are tidy"

# THE verify entry point. Every "done / verified / it's green" claim about this
# repo resolves against this recipe and nothing else.
#
# It runs ALL six checks and then reports, rather than aborting on the first
# failure. That is deliberate: `just verify` stopping at gofmt, leaving you to
# discover after fixing it that the tests were red too, is how a one-command
# gate turns back into a seven-command checklist. The exit code is non-zero if
# any check failed, and the summary names which.
#
# Content is AGENTS.md's "You MUST run before committing" block, minus the two
# that are not gates: `go fix` and `gofmt -w` MUTATE (their check-only forms are
# fmt-check and lint), and govulncheck depends on an upstream advisory database
# that can turn a commit red without the tree changing -- it belongs on a
# schedule, which .github/workflows/deep-security-audit.yml already gives it.

# THE gate: fmt-check + exec-bit-check + vet + mod-tidy-check + lint + test-audit
verify:
    #!/usr/bin/env bash
    set -u
    failed=()
    for check in fmt-check exec-bit-check vet mod-tidy-check lint test-audit; do
        echo ""
        echo "=== just $check ==="
        if ! just "$check"; then
            failed+=("$check")
        fi
    done
    echo ""
    echo "======================================================================"
    if [ ${#failed[@]} -eq 0 ]; then
        echo "verify: all 6 checks passed"
        exit 0
    fi
    echo "verify: ${#failed[@]} of 6 checks FAILED -- ${failed[*]}"
    exit 1
