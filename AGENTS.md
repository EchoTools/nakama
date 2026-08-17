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

This project adopts **`/home/andrew/src/metis-core/GO-ADDENDUM-GENERIC.md`** as
the binding Go code standard. Read it before writing or reviewing code.

> Use the explicit absolute path — not `~`, and not `/srv`. This line previously
> read `/srv/src/metis-core/...`, which is the **retired tethys-era convention**:
> development no longer happens on tethys, and on this host the local trees are
> `/home/andrew/src/<repo>`. `/srv/src` survives here only as a decayed
> root-owned symlink farm holding a single entry, so the old path resolves for
> nothing this repo needs.
>
> Corrected 2026-08-16. An earlier version of this note claimed the file "does
> not exist on this host" — that was **false** and is retracted: the file exists,
> and `/srv/src` is a real directory. The path was stale, not imaginary. Recorded
> because a mandatory pre-read nobody can open is an unenforced gate — every
> agent that "adopted" the standard read nothing, and nothing failed.

Key requirements for any agent (including heisthecat31 or any AI assistant):

### You MUST run before committing

```bash
gofmt -l -w    # format
go vet ./...   # static analysis
golangci-lint run --max-issues-per-linter 0 --max-same-issues 0   # see note
just test      # tests -- repo-wide scope, see TEST_PKGS in the justfile
go fix ./...   # apply modernizers
go mod tidy    # clean dependencies
govulncheck    # vulnerability check
```

**On the `golangci-lint` flags, and its baseline.** Bare `golangci-lint run`
applies the defaults `max-issues-per-linter=50` and `max-same-issues=3`, which
report **153** of the **377** findings actually present — an under-report of 60%
that looks exactly like a cleaner tree. The flags above turn the truncation off.

The config was v1-format against a v2 binary from 2023 until 2026-08-16, so this
gate did **not run at all** for roughly three years — it failed at config load
with `unsupported version of the configuration: ""`, and the `deep-security-audit`
workflow that also invokes it had been failing the same way. Migrated in
`a0c12bae8`; the effective linter set was held identical and verified by full
set-difference against v1.64.8 rather than assumed.

Consequence to plan around: **377 pre-existing findings** (errcheck 193,
staticcheck 95, unused 47, ineffassign 21, govet 18, gofmt 3). That is a report,
not yet a gate. Enforcing it on new work only — `--new-from-rev=origin/main` —
is the obvious path and is an owner decision, not taken here.

Four files are `gofmt`-non-compliant on `main` and predate this note. Two are
trailing whitespace (`server/evr_lobby_builder_team_assignment_test.go`,
`server/evr_team_composition_test.go`); two are mis-indentation that reads like a
real bug — an `i := 0; i++` immediately before a `for i := 0; ...` that shadows it
— in `internal/skiplist/skiplist_test.go` and `internal/cronexpr/cronexpr_test.go`.
The `cronexpr` one is invisible to `golangci-lint` (skipped dir) but **visible to
`gofmt -l`, and therefore to pre-push check 2**.

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
   happens when it fires. **Worst form, observed 2026-08-16:** `golangci-lint`
   was listed above as mandatory while failing at config load, so it neither
   reported *nor* prevented, for about three years. A gate you have never seen
   produce output is not a gate. Run it once and look.

5. **A test double that embeds a nil `runtime.NakamaModule`, so adding any
   interface method call to production code panics an unknown subset of the
   suite.** **38 distinct types across 31 `*_test.go` files** embed the interface
   and implement only what they need; every other method is a nil dereference at
   call time, not a compile error. Worse, a Go test panic aborts the whole test
   binary, so one run reveals exactly **one** offender — you fix it, re-run, find
   the next. There is no way to enumerate the true break set in advance.
   Observed three times in one day (2026-08-16), by three independent changes
   adding `MetricsCounterAdd` and `StorageWrite` calls.
   **Method:** iterate to green and report the count you reached; do not
   pre-emptively stub all 38. **Where to put the stub:** on the shared base
   (`occTestNakamaModule`) when many embedders need it — one edit fixed five —
   or on the *leaf* double when only one does. A leaf method shadows a promoted
   one and compiles; two definitions on the same receiver do not. Defect class 1
   does **not** apply to stubs added this way, but say why in a comment at the
   method: no embedder defines it, and the call arrives through the interface,
   which dispatches to the outermost type.

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
