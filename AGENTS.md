# EchoTools/nakama — Agent Instructions

## Standards

This project adopts **`/srv/src/metis-core/GO-ADDENDUM-GENERIC.md`** as the
binding Go code standard. Read it before writing or reviewing code.

Key requirements for any agent (including heisthecat31 or any AI assistant):

### You MUST run before committing

```bash
gofmt -l -w    # format
go vet ./...   # static analysis
golangci-lint run  # comprehensive lint
go test -race ./server/...  # tests with race detector
go fix ./...   # apply modernizers
go mod tidy    # clean dependencies
govulncheck    # vulnerability check
```

### Pre-push hook (automated gate)

The repo ships a pre-push hook in `.githooks/pre-push` that checks:
0. Destination — refuses a push that **resolves** to `main`
1. Tag format (`v*` tags must include `-evr.<N>`)
2. `gofmt` compliance
3. `go vet` on changed packages
4. `gopls` diagnostics on changed files
5. `go mod tidy` hasn't drifted

Install: automatic — running any `just` recipe arms the clone. Explicitly:
`just hooks` (or `git config core.hooksPath .githooks`).

**The hook is off until installed, and that failure mode is silent** —
`core.hooksPath` is local config and cannot be committed, so nothing in the
repository can activate it on its own. Git refuses that deliberately: a clone
that armed its own hooks would be arbitrary code execution on `git clone`.

So the auto-arm shrinks the window rather than closing it. **If you clone and
push without running a single `just` recipe, you are unguarded.** `just --list`
does not count — just evaluates variables lazily and `--list` does not trigger
them. Check with `git config --get core.hooksPath`; it must print `.githooks`.
`git push --no-verify` also skips the hook entirely.

The backstop for everything the hook cannot cover is
`.github/workflows/main-push-audit.yaml`, which is server-side and arms itself —
but detects after the push has landed rather than preventing it.

Check 0 exists because a worktree branch created from `origin/main` *tracks*
`origin/main`, and with `push.default=tracking` git resolves the destination
from the upstream rather than the branch name — so `git push -u origin
my-branch` lands on `main`. That has happened twice here. Push with an explicit
refspec (`git push origin HEAD:refs/heads/<branch>`); to push to `main`
deliberately, say so: `ALLOW_MAIN_PUSH=1 git push ...`.

The server-side backstop for an uninstalled hook is
`.github/workflows/main-push-audit.yaml`, which flags a commit that reached
`main` without a PR. It is detection after the fact, not prevention.

### Architecture rules

- **`server/evr_*.go`** is custom EchoVR code. Upstream Nakama code is rarely modified.
- **Symbol hash** is CSymbol64 (see `server/evr/core_hash.go`), NOT any other hash.
- **Matchmaker** is the most complex subsystem — changes need integration tests.
- **Party follow** path (`TryFollowPartyLeader` / `pollFollowPartyLeader`) has 30+ unit tests.
- **Display names** have three systems (Nakama, EchoVR in-game, Discord) that interact in
  non-obvious ways.
- **Log discipline**: expected behavior is `info` or `debug`. `warn` = someone should look.
  `error` = something broke. Never downgrade `warn` to `debug` without explicit review.

### Hard invariants (DO NOT CHANGE)

- **Guild isolation is absolute.** All matchmaking streams, tickets, queries, and lobby
  searches are scoped to `GroupID`. Players in different guilds NEVER match together,
  even in public modes. Do NOT normalize, nil-out, or bypass GroupID for "cross-guild"
  matching. This has been incorrectly "fixed" multiple times. It is not a bug.

### Before you touch push safety or test doubles

Read [`docs/handoff-push-safety-and-open-candidates.md`](docs/handoff-push-safety-and-open-candidates.md).

It records the exact status of the two `main` guards and **what each does not
cover** — the pre-push hook does not arm itself, and the CI audit is detection
after the fact, not prevention. Do not read "gates exist" as "`main` is
protected."

It also carries four open CANDIDATEs: known defect classes with no mechanical
detector, recorded so a second occurrence is recognized as one. Two are load-
bearing for work already queued — a test-double trap that blocks #394, and a
`-race` failure mode where a passing test proves nothing.

### Common violations to flag on sight

- Removing logging (debug/warn/error) without explanation
- Adding nil checks that mask the real bug instead of fixing it
- Fixes that touch multiple subsystems when the root cause is in one place
- Commits without test changes for logic modifications
- Parallel goroutine joins to the same match (use sequential with early termination)
- Hardcoded Symbol values instead of `ToSymbol()`
- Matchmaker changes without running integration tests
