#!/usr/bin/env bash
# go-test-limit.sh — run a Go test binary inside a bounded cgroup.
#
# Wired in via GOFLAGS=-exec=..., so it applies to EVERY `go test` invocation
# regardless of caller: `just test`, a raw `GOWORK=off go test ./server/...`,
# or an agent's ad-hoc command. `go test -exec` wraps the TEST BINARY, so each
# package gets its own cgroup and its own limit.
#
# WHY THIS EXISTS
#
# A runaway test used 11.6 GB and kept climbing. The kernel OOM killer picks
# victims machine-wide by badness heuristic, so it did not kill the test — it
# killed two unrelated developer sessions. The failure signal landed on the
# wrong process and it took two crashes plus a manual `ps` to find the culprit.
#
# The point of this cap is NOT resource hygiene. It is deterministic, correctly
# attributed failure: the offending test binary is the only process eligible to
# die, and it dies with a message naming itself.
#
# MemorySwapMax=0 is load-bearing. With swap available the cgroup pushes pages
# to swap and thrashes for minutes, degrading the whole machine, before anything
# dies. Disabling swap for the scope makes the limit bite promptly.
#
# Tunables:
#   GO_TEST_MEMORY_LIMIT=8G     raise/lower the per-test-binary cap
#   GO_TEST_MEMORY_LIMIT=off    disable entirely (also: unset GOFLAGS)

set -uo pipefail

readonly LIMIT="${GO_TEST_MEMORY_LIMIT:-4G}"

if [ "$#" -eq 0 ]; then
	echo "go-test-limit.sh: no command given" >&2
	exit 2
fi

# Escape hatch: run unwrapped.
if [ "${LIMIT}" = "off" ] || [ "${LIMIT}" = "0" ]; then
	exec "$@"
fi

# Detect, do not assume. systemd-run may be missing (containers, macOS), or
# present but unusable (no user D-Bus session, cgroup v1, memory controller not
# delegated to the user slice). Probe once and cache the verdict for the run so
# a multi-package `go test ./...` pays for it a single time.
probe_cgroup_cap() {
	command -v systemd-run >/dev/null 2>&1 || return 1
	systemd-run --user --scope -q \
		-p MemoryMax="${LIMIT}" -p MemorySwapMax=0 \
		-- true >/dev/null 2>&1
}

probe_cache="${TMPDIR:-/tmp}/.go-test-limit-probe-$(id -u)"
readonly probe_cache
if [ -f "${probe_cache}" ] && [ -z "${GO_TEST_MEMORY_LIMIT_REPROBE:-}" ]; then
	cap_ok="$(cat "${probe_cache}" 2>/dev/null || echo no)"
else
	if probe_cgroup_cap; then cap_ok=yes; else cap_ok=no; fi
	printf '%s' "${cap_ok}" >"${probe_cache}" 2>/dev/null || true
fi

# Graceful degradation: no usable cgroup support means run the tests anyway,
# unwrapped. A missing cap must never fail a test run that would otherwise pass.
if [ "${cap_ok}" != "yes" ]; then
	exec "$@"
fi

# Backgrounded + `wait` so that when the cgroup OOM killer SIGKILLs the scope,
# bash does not print its own "Killed  systemd-run --user --scope ..." line.
# That message leaks the wrapper's internals into test output and buries the
# attributed diagnostic below.
systemd-run --user --scope -q \
	-p MemoryMax="${LIMIT}" -p MemorySwapMax=0 \
	-- "$@" &
wait $! 2>/dev/null
rc=$?

# 137 = 128 + SIGKILL. Under MemoryMax with swap disabled, that is the cgroup
# OOM killer reclaiming this scope — and only this scope.
if [ "${rc}" -eq 137 ]; then
	cat >&2 <<EOF

*** go-test-limit: $(basename "${1}") exceeded the ${LIMIT} memory cap and was killed.
*** Only this test binary was eligible to die; no other process was affected.
*** This usually means unbounded growth in the test or the code under test.
*** To raise it for one run: GO_TEST_MEMORY_LIMIT=8G go test ...
*** To disable the cap:      GO_TEST_MEMORY_LIMIT=off go test ...

EOF
fi

exit "${rc}"
