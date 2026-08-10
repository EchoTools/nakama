# EchoTools/nakama — Agent Instructions

## Handoffs are ephemeral. Never commit one.

Session handoffs, campaign summaries, status reports and dated test plans are
**artifacts of doing the work, not part of the repository**. Do not add them to
`docs/`, and do not add them anywhere else either.

They duplicate what `git log`, the PR list and the issue tracker already record,
and unlike code they rot without failing: five such files accumulated to ~1,700
lines, and by the time they were removed they described issues as open that had
been closed and cited line numbers that had moved. Nothing catches that.

If something learned in a session deserves to outlive it, put it where it will
be read at the moment it matters:

| the thing | where it goes |
|---|---|
| why a setting is off, or a constant has that value | a comment at the declaration |
| the status of an issue | a comment on that issue |
| a defect class worth recognizing again | this file |
| intended design or behaviour | a `docs/spec-*.md` |
| the reasoning behind a change | the commit message and the PR body |

Design specs are different and are kept — they describe what the system is
meant to do, not what happened on a particular afternoon.

## Standards

This project adopts **`/srv/src/metis-core/GO-ADDENDUM-GENERIC.md`** as the
binding Go code standard. Read it before writing or reviewing code.

Key requirements for any agent (including heisthecat31 or any AI assistant):

### You MUST run before committing

```bash
gofmt -l -w    # format
go vet ./...   # static analysis
golangci-lint run  # comprehensive lint
just test      # tests -- repo-wide scope, see TEST_PKGS in the justfile
go fix ./...   # apply modernizers
go mod tidy    # clean dependencies
govulncheck    # vulnerability check
```

### Pre-push hook (automated gate)

The repo ships a pre-push hook in `.githooks/pre-push` that checks:
0. Destination — refuses a push that **resolves** to `main`
1. Tag format (`v*` tags must include `-evr.<N>`)
2. `gofmt` compliance
3. `go vet ./server/...` (not the changed packages -- a change under `internal/` gets no vet coverage here; CI vets the full tree)
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

### Do not read "gates exist" as "`main` is protected"

Two guards cover pushes to `main`, and neither is complete:

- `.githooks/pre-push` **prevents**, but only once armed. `core.hooksPath` is
  local config and cannot be committed, so a fresh clone is unguarded until it
  runs a `just` recipe. `git push --no-verify` skips it entirely.
- `.github/workflows/main-push-audit.yaml` arms itself, but only **detects** —
  after the fact.

Branch protection requiring a PR is the real answer, and it is a repository
setting, not ours.

### Known defect classes with no mechanical detector

Recorded so a second occurrence is recognized as a second occurrence. Each has
been observed at least once in this repo.

1. **A method added to a shared test double silently disables subclass fault
   injection.** Go embedding does not dispatch virtually: when a base double
   gains a method, an internal call resolves to the *base's* implementation,
   never a subclass override. The failure is silent — the injected fault simply
   does not occur and the test passes. Load-bearing for #394, whose remaining
   work is "convert more call sites to `MultiUpdate`."
2. **A concurrency test that never achieves concurrency.** A `-race` test here
   passed against a deliberately unlocked accessor because the goroutines never
   overlapped. Fixed in the instance with a start barrier; the general case is
   unsolved and there is no lint for it. Run any new `-race` test once against
   a deliberately broken version and confirm it fails.
3. **Installing into an occupied slot without enumerating what is already
   there.** #546 rewrote `.githooks/pre-push` without checking what it held and
   destroyed five gates to install one. Before writing a hook, config, or gate
   at a fixed path, read what occupies that path first.
4. **A guard that reports without preventing.** A check that logs, warns, or
   audits but never blocks — and is then cited as if it enforced. Ask what
   happens when it fires.

The dominant related anti-pattern, and the one most often found here, is
**fail-open on a fail-closed control**: a gate that, when its input is missing
or its dependency errors, admits instead of refusing.

### Common violations to flag on sight

- Removing logging (debug/warn/error) without explanation
- Adding nil checks that mask the real bug instead of fixing it
- Fixes that touch multiple subsystems when the root cause is in one place
- Commits without test changes for logic modifications
- Parallel goroutine joins to the same match (use sequential with early termination)
- Hardcoded Symbol values instead of `ToSymbol()`
- Matchmaker changes without running integration tests
