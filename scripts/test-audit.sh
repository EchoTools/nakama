#!/usr/bin/env bash
# test-audit.sh — run the test suite and refuse a green result that covered
# almost nothing.
#
# WHY THIS EXISTS
#
# The `server` package once died 0.07s into a run that normally takes ~130s,
# because the EVR module's InitModule hit a zap fatal and os.Exit'd the whole
# test binary (#553). Every EVR test in the repo's largest package went unrun.
#
# That particular failure was loud -- it reported FAIL. The one this guards is
# the quiet cousin: a suite that still reports `ok` while executing a small
# fraction of what it should, because the tests SKIPPED rather than died. A
# skipped test is a pass as far as the exit code is concerned. Build tags, a
# missing fixture, an environment probe that starts returning false -- any of
# them can hollow out a package while every signal stays green.
#
# So this does not gate on duration. Duration tracks how fast the machine is;
# it would drift on every runner change and tell you nothing about coverage.
# It gates on how many tests actually PASSED, which is the number that collapses
# when a package stops really running.
#
# The floors are deliberately far below current counts. This is a
# catastrophic-collapse detector, not a drift detector: it should fire when a
# package loses most of its suite, and never because someone deleted a test.
#
# It also prints how many tests skipped, which is not a failure but is the thing
# that makes "CI is green" an honest statement rather than a vague one.
#
# Usage:
#   scripts/test-audit.sh [-- extra go test args...]
#
# Environment:
#   TEST_PKGS  space-separated package list (defaults to the justfile's scope)

set -euo pipefail

# The floors live in the Python block below, in one place. Do not add a copy
# here: two tables that must agree are two tables that will not.

if [ -z "${TEST_PKGS:-}" ]; then
    TEST_PKGS=$(go list ./... | grep -v '/internal/gopher-lua' | grep . || {
        echo "test-audit: go list matched no packages" >&2
        exit 1
    })
fi

raw=$(mktemp -t nakama-test-audit.XXXXXX)
trap 'rm -f "$raw"' EXIT

# `go test -json` is the only way to get per-test outcomes, but its output is
# unreadable. Tee it and reprint the human text, so this reads like a normal
# run and the audit is additive rather than a tradeoff.
set +e
# shellcheck disable=SC2086
go test -json -count=1 ${TEST_PKGS} "$@" >"$raw" 2>&1
go_status=$?
set -e

python3 - "$raw" "$go_status" <<'PY'
import collections
import json
import sys

raw_path, go_status = sys.argv[1], int(sys.argv[2])

FLOORS = {
    "github.com/heroiclabs/nakama/v3/server": 2300,
    "github.com/heroiclabs/nakama/v3/server/evr": 190,
}

passed = collections.Counter()
skipped = collections.Counter()
failed = collections.Counter()
seen = set()

with open(raw_path, encoding="utf-8", errors="replace") as fh:
    for line in fh:
        line = line.rstrip("\n")
        if not line.startswith("{"):
            # go test can emit non-JSON on catastrophic failures (a build error
            # or a runtime panic before the harness starts). Passing it through
            # rather than swallowing it is the whole point on the day it
            # happens.
            print(line)
            continue
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            print(line)
            continue

        if event.get("Action") == "output":
            sys.stdout.write(event.get("Output", ""))

        pkg = event.get("Package")
        if pkg:
            seen.add(pkg)
        if not event.get("Test"):
            continue
        action = event.get("Action")
        if action == "pass":
            passed[pkg] += 1
        elif action == "skip":
            skipped[pkg] += 1
        elif action == "fail":
            failed[pkg] += 1

sys.stdout.flush()

total_pass = sum(passed.values())
total_skip = sum(skipped.values())
total_fail = sum(failed.values())

print()
print("test audit")
print("-" * 72)
print(f"{'package':47s} {'pass':>6s} {'skip':>6s} {'fail':>6s}")
for pkg in sorted(set(passed) | set(skipped) | set(failed)):
    short = pkg.replace("github.com/heroiclabs/nakama/v3/", "./")
    print(f"{short:47s} {passed[pkg]:6d} {skipped[pkg]:6d} {failed[pkg]:6d}")
print("-" * 72)
print(f"{'total':47s} {total_pass:6d} {total_skip:6d} {total_fail:6d}")

if total_skip:
    # Not an error. Stated loudly because a green run that skipped 129 tests is
    # a different claim from a green run that skipped none, and the exit code
    # cannot tell them apart.
    print()
    print(f"note: {total_skip} test(s) skipped -- green here does not mean these ran.")

violations = []
for pkg, floor in sorted(FLOORS.items()):
    short = pkg.replace("github.com/heroiclabs/nakama/v3/", "./")
    if pkg not in seen:
        violations.append(
            f"{short}: did not run at all (expected at least {floor} passing tests)"
        )
    elif passed[pkg] < floor:
        violations.append(
            f"{short}: {passed[pkg]} passing test(s), floor is {floor}"
        )

if violations and go_status != 0:
    # The tests already failed, and a build error or a genuine failure drives
    # the pass count to zero all by itself. Leading with "COVERAGE FLOOR
    # VIOLATED" here points the reader at the floor -- and at the advice to
    # change it -- when the thing to fix is printed far earlier in the log.
    print()
    print("note: the coverage floor was also missed, but the test run itself failed.")
    print("      Fix the failure above first; the floor is a consequence, not the cause.")
    for v in violations:
        print(f"  {v}")
    sys.exit(go_status)

if violations:
    print()
    print("COVERAGE FLOOR VIOLATED -- this run did not execute what it claims to.")
    for v in violations:
        print(f"  {v}")
    print()
    print("A package that stops running reports success just as loudly as one")
    print("that ran. If the drop is intentional, change the floor in")
    print("scripts/test-audit.sh in the same commit, so it is reviewed.")
    sys.exit(1)

sys.exit(go_status)
PY
