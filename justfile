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

# THE lint gate: zero findings on the lines this branch changed.
#
# It was a count-ratchet until 2026-09-08 -- LINT_BASELINE, a hand-maintained
# CEILING ("268", set by 30f142505) that this recipe compared a full-tree count
# against, failing both when the count rose above it AND when it fell below it
# without the number being lowered in the same commit. Deleted, because it was
# never satisfiable. Measured 2026-09-08, cold cache, private cache dir:
#
#   6e9e5dbb8 (main)   golangci-lint 2.13.1  ->  270    vs LINT_BASELINE 268
#   30f142505          golangci-lint 2.13.1  ->  270    the commit that SET 268
#   6e9e5dbb8 (main)   golangci-lint 2.12.2  ->  268    the version CI pins
#
# The whole delta is two SA4023 findings at
# server/evr_discord_reservation_commands.go:261-262 that staticcheck reports
# from 2.13.1 on and not from 2.12.2. Not a code regression: linter drift. A
# ceiling whose measurement moves with the developer's linter version is a
# chore, not a gate, and this one was already red on the commit that authored
# it -- nobody could have committed under it without lowering it again.
#
# What replaces it is the check that was already holding the line on pull
# requests: --new-from-merge-base. It does not care about the backlog at all,
# only about what this branch added -- so fixing one old finding while adding a
# new one no longer nets out to a pass. merge-base rather than --new-from-rev
# deliberately: it does not fire on findings that main introduced under you.
#
# Requires full history (actions/checkout fetch-depth: 0).
#
# Three failure modes are handled explicitly, because each has already happened
# here or is one keystroke away:
#
#   1. THE LINTER DID NOT RUN. golangci-lint exits 0 with no issues, 1 with
#      issues, and something else on a config or usage error. From 2023 to
#      2026-08-16 it exited 3 on every invocation ("unsupported version of the
#      configuration") and the workflow that called it had been failing the same
#      way, unnoticed. Anything other than 0 or 1 is a hard failure here, with
#      the output printed -- never a silent zero-issue pass. A REF that does not
#      resolve lands here too, which is the fail-closed direction.
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
#   3. THIS BRANCH ADDED A FINDING. That is the gate below, and its threshold
#      is zero. There is no number to raise.

# Zero new findings against REF; non-zero on anything this branch added
lint REF="origin/main":
    @set -u; \
    export GOLANGCI_LINT_CACHE="${GOLANGCI_LINT_CACHE:-{{ LINT_CACHE }}}"; \
    out="$(golangci-lint run {{ LINT_FLAGS }} --new-from-merge-base {{ REF }} 2>&1)"; rc=$?; \
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
    if [ "$count" != "0" ]; then \
        echo "ERROR: $count new lint finding(s) against {{ REF }}."; \
        printf '%s\n' "$out" | sed 's/^/  /'; \
        echo ""; \
        echo "These are on lines this branch changed. There is no baseline to"; \
        echo "raise -- fix them, or the finding ships."; \
        echo "Full-tree backlog (a report, not a gate): just lint-all"; \
        exit 1; \
    fi; \
    echo "lint: no new findings against {{ REF }} (0 foreign paths)"

# Kept as an alias, not a second opinion: .githooks/pre-push,
# .github/workflows/build.yaml and three years of muscle memory name it, and it
# is now the same check `just lint` runs.

# Alias for `just lint REF`
lint-new REF="origin/main": (lint REF)

# The full-tree backlog, uncapped and printed whole. A REPORT, NOT A GATE: it
# exits 0 whatever the count. A number nobody is required to move is
# information; a number everybody is required to move is the chore that was just
# deleted. It still fails on the two integrity guards above -- a report from a
# linter that did not run, or one citing another checkout's paths, is worse than
# no report at all.
#
# LINT_FLAGS is not optional here: bare `golangci-lint run` truncates at
# max-issues-per-linter=50 / max-same-issues=3 and reported 153 of 377, a 60%
# under-report that looks exactly like a cleaner tree.

# Full-tree uncapped backlog report; exits 0 on any count (not a gate)
lint-all:
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
    printf '%s\n' "$out"; \
    echo ""; \
    echo "lint-all: $count findings in the full tree (0 foreign paths)."; \
    echo "This is a REPORT and does not gate. The gate is 'just lint':"; \
    echo "zero new findings against origin/main."

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

# ---------------------------------------------------------------------------
# THE RELEASE gate: verify + lint everything that ships + no open blocker.
#
# `just lint` alone cannot answer "is this releasable". It is
# --new-from-merge-base, and on `main` the merge base IS HEAD, so it inspects
# zero lines and passes vacuously. CI has the same hole: lint-new runs only on
# pull_request, and the push-to-main job runs lint-all, which exits 0 at any
# count. Nothing lints the whole set of changes that ship between two releases.
#
# MILESTONE is REQUIRED and has no default on purpose. A defaulted milestone
# goes stale the moment a release ships, and `gh issue list --milestone` returns
# an empty list with exit 0 for a title that matches nothing -- so a stale or
# mistyped default reports "no blockers" having checked nothing. Naming it each
# run is the only version of this that cannot rot:
#
#     just release-check v3.27.2-evr.324
#     just release-check v3.27.2-evr.325 v3.27.2-evr.324
#
# REF keeps a default because its failure mode is the safe one: an older
# baseline lints MORE than ships, which is noise, not a vacuous pass. It
# defaults to the tag production actually runs, which is not the newest tag --
# v3.27.2-evr.323 was tagged and is not deployed (identified in issue #588 from
# log caller line numbers).
#
# This recipe does NOT tag and does NOT push. `just release` is a separate,
# human-only step -- see CLAUDE.md.
release-check MILESTONE REF="v3.27.2-evr.322":
    #!/usr/bin/env bash
    set -u
    failed=()

    # 0. Refuse to run against a REF or MILESTONE that would make a step
    #    vacuous. A gate that inspects nothing and reports success is the
    #    failure mode this recipe exists to close, so these are hard exits,
    #    not failures collected for the summary.
    #
    #    REF must be a RELEASE TAG, not any commit-ish. `HEAD~1` resolves, is
    #    an ancestor, and is not HEAD -- it passes a naive check and lints one
    #    commit instead of the whole release range. So: it must be a tag, and
    #    it must look like the tags that ship (`*evr*`, which is what
    #    .github/workflows/dockerhub-nakama.yaml fires on).
    case "{{ REF }}" in
        *evr*) ;;
        *)  echo "ERROR: REF '{{ REF }}' is not a release tag (no 'evr' in the name)."
            echo "The baseline must be the tag production runs, e.g. v3.27.2-evr.322."
            exit 1 ;;
    esac
    if ! git rev-parse -q --verify "refs/tags/{{ REF }}^{commit}" >/dev/null 2>&1; then
        echo "ERROR: REF '{{ REF }}' is not an existing tag in this repository."
        echo "Fetch tags, or pass the tag production is running."
        exit 1
    fi
    if ! git merge-base --is-ancestor "refs/tags/{{ REF }}" HEAD; then
        echo "ERROR: REF '{{ REF }}' is not an ancestor of HEAD."
        echo "Nothing meaningful to lint: the merge base is not the release point."
        exit 1
    fi
    if [ "$(git rev-parse "refs/tags/{{ REF }}^{commit}")" = "$(git rev-parse HEAD^{commit})" ]; then
        echo "ERROR: REF '{{ REF }}' is HEAD; there is nothing to lint."
        echo "This is the vacuous pass this gate exists to refuse."
        exit 1
    fi

    #    MILESTONE must exist and be OPEN. `gh issue list --milestone` returns
    #    an empty list with exit 0 for a title that matches nothing, so a typo
    #    -- or a default left pointing at a milestone that has already shipped
    #    -- reports "no blockers" and the gate passes having checked nothing.
    for dep in gh jq; do
        if ! command -v "$dep" >/dev/null 2>&1; then
            echo "ERROR: '$dep' is required to check release blockers and is not installed."
            echo "Refusing to pass a check that cannot run."
            exit 1
        fi
    done
    ms_state="$(gh api "repos/{owner}/{repo}/milestones?state=all&per_page=200" \
                --jq '.[] | select(.title == "{{ MILESTONE }}") | .state' 2>&1)"; rc=$?
    if [ "$rc" != "0" ]; then
        echo "ERROR: could not list milestones (gh exit $rc)."
        printf '%s\n' "$ms_state" | sed 's/^/  /'
        exit 1
    fi
    if [ -z "$ms_state" ]; then
        echo "ERROR: milestone '{{ MILESTONE }}' does not exist."
        echo "An unknown milestone lists zero blockers and passes having checked nothing."
        exit 1
    fi
    if [ "$ms_state" != "open" ]; then
        echo "ERROR: milestone '{{ MILESTONE }}' is '$ms_state', not open."
        echo "A shipped milestone has no open blockers left and would pass"
        echo "having checked nothing. Name the release being cut."
        exit 1
    fi
    if [ "{{ MILESTONE }}" = "{{ REF }}" ]; then
        echo "ERROR: milestone '{{ MILESTONE }}' is the same as the baseline tag."
        echo "The milestone names the release being CUT, not the one running."
        exit 1
    fi

    echo ""
    echo "=== just verify ==="
    if ! just verify; then failed+=("verify"); fi

    echo ""
    echo "=== lint everything since {{ REF }} ==="
    if ! just lint "{{ REF }}"; then failed+=("lint-since-{{ REF }}"); fi

    echo ""
    echo "=== open release-blockers in {{ MILESTONE }} ==="
    {
        out="$(gh issue list --milestone "{{ MILESTONE }}" --label release-blocker \
               --state open --limit 200 --json number,title 2>&1)"; rc=$?
        if [ "$rc" != "0" ]; then
            echo "ERROR: gh failed (exit $rc). Refusing to pass a check that did not run."
            printf '%s\n' "$out" | sed 's/^/  /'
            failed+=("blockers")
        else
            n="$(printf '%s' "$out" | jq 'length')"
            if [ "$n" != "0" ]; then
                printf '%s' "$out" | jq -r '.[] | "  #\(.number) \(.title)"'
                echo "ERROR: $n open release-blocker(s) in {{ MILESTONE }}."
                failed+=("blockers")
            else
                echo "blockers: none open in {{ MILESTONE }} (milestone verified open)"
            fi
        fi
    }

    echo ""
    echo "======================================================================"
    if [ ${#failed[@]} -eq 0 ]; then
        echo "release-check: releasable -- {{ REF }}..HEAD is clean and no blocker is open"
        echo "Tagging is a human step. See CLAUDE.md."
        exit 0
    fi
    echo "release-check: ${#failed[@]} of 3 FAILED -- ${failed[*]}"
    exit 1
