# Campaign record: 2026-08-07

Twelve pull requests merged to `main` in one afternoon. This note exists so that it
is legible later, and so the parts that were **not** fixed are as findable as the
parts that were.

Work was done by an automated agent (`nakama-fixer <nakama-fixer@metis.agents>`)
under supervision. Every merge was gated on green CI, re-verified immediately
before merging because each merge moved the base for the next.

## What merged

**Backlog that was already open and reviewed** — merged sequentially, each
re-verified against the base the previous merge had moved:

| PR | what it did |
|----|---|
| #528 | cap per-test-binary memory in a cgroup |
| #530 | assorted one-off correctness fixes (Tier-1 batch) |
| #531 | scope the moderator entrant claim to the lobby's guild; make the degraded VPN gate alertable |
| #532 | outage-safe guild pruning, real report-only mode |
| #534 | match lifecycle: independent idle timers, bounded MatchStart retry, idempotent MatchShutdown |
| #535 | early-quit penalty-ladder totality, moderator sanctions as a floor, atomic completion credit |

#535 was blocked mid-campaign: merging #534 turned it `CONFLICTING`. It was
rebased onto the new `main`, which surfaced two collisions git had auto-merged
silently — a duplicate `StorableWriteMany` and a caller of a function `main` had
deleted as dead. Both resolutions are recorded in that PR.

**Defects found during the campaign, then fixed** — see "What was found" below:

| PR | Issue | what it did |
|----|-------|---|
| #542 | #537 | read the guild count under the discordgo state lock |
| #543 | #538 | take the bot application ID from the locked accessor |
| #544 | #538 | read the bot's username under the state lock |
| #545 | #540 | withdraw a deprecation that pointed at the wrong API |
| #546 | #541 | pre-push hook refusing pushes that resolve to `main` |
| #547 | #541 | server-side audit flagging a commit that reached `main` without a PR |

`684c895af` (the `.gitignore` fix for #539) reached `main` **without a PR**. That
was the incident recorded in #541, not a shortcut — see below.

## What was found

**Unlocked reads of discordgo-owned state (#537, #538).** `Session.State` is
mutated by the gateway goroutine under `State.Lock()`; fourteen production reads
took it with no lock, from handler and session goroutines. #536 had already
established the mechanism and built the first accessor; it was applied to the one
call site that PR was about. The remaining sites were converted in three units,
split **by accessor** rather than by area so each had one clean red/green.

The widest exposure was not the one that looked worst. Six of the reads are in
`evr_pipeline_login.go` and run **once per player login** — the life of the
process, not just startup.

Impact was stated honestly rather than inflated: on amd64 these yield stale or
wrong values, not crashes. One exception mattered more — `AuthenticateCustom`
persisted a possibly-stale username onto the bot's own Nakama account, which
outlives the moment.

**A deprecation note pointing at an API that cannot answer the question (#540).**
`GetLockoutDuration(level)` was marked deprecated in favour of
`ResolvePenaltyLevel(numQuits, cfg)`. The two are keyed on different things, and
following the note is what produced a bug #535 had to fix. There is no
level-keyed replacement, so the deprecation was **withdrawn rather than
redirected**, and a test now pins that the two are not interchangeable.

**A `.gitignore` that named the hazard it failed to match (#539).** The comment
block cited `tools/purge-docker-ips/purge-docker-ips` — an 87MB ELF removed in
`87924552e` — while the rules below it listed the other two tool binaries and not
that one. Replaced enumeration with shape matching: build outputs under `tools/`
are extensionless, sources are not.

## What was found and NOT fixed

- **#394 (consolidate storage writes to use `MultiUpdate`)** is blocked, with the
  reason recorded on the issue. Adding a method to a shared test double silently
  bypasses subclass fault injection, because Go embedding is not virtual: the
  base's method calls the base's own callee, the injected fault never fires, and
  the test passes for the wrong reason. #394's remaining tasks are all "convert
  more call sites", so each one drags its tests onto that path. The trigger to
  build the guard rather than patch around it is on the issue.

- **GitHub branch protection** requiring a PR to land on `main` is the mechanism
  that would actually *prevent* a direct push. It is not enabled. It constrains
  every human with push access, so it is the repository owner's decision and was
  deliberately left to them.

- **`push.default = simple`** for this repo would remove the trap at its source.
  Same reasoning: it changes behaviour for humans pushing from this clone.

## What remains gated

`.githooks/pre-push` **is off until armed.** `core.hooksPath` is local config and
cannot be committed, so the hook ships with the repo and does not activate
itself; `just hooks` is the per-clone step. A fresh clone, a new worktree, or an
agent starting cold is unguarded — which is the population it was built for.
`.github/workflows/main-push-audit.yaml` is the server-side backstop for that
gap, and it is detection, not prevention.

## The incident

An agent push landed on `main` (#541). Cause: a worktree branch created from
`origin/main` tracks `origin/main`, and with `push.default=tracking` git resolves
the destination from the **upstream**, not the branch name — so
`git push -u origin my-feature` pushes to `refs/heads/main`.

**This was the second occurrence of the same mechanism in this repository.** The
rule that prevents it — always use an explicit refspec — had been written down a
month earlier, in prose, after the first occurrence. Prose did not stop it. That
is the finding, and it is why #546 and #547 exist: the control now lives in a
hook and a workflow instead of in a document someone has to be reading at the
right moment.

The commit was **not** reverted and **not** force-pushed away. Its content was
correct and independently verified; the defect was the path it took. Rewriting
shared history would have erased the evidence that it happened, and a revert
would have been a second unreviewed write to `main` for a functionally identical
repo.

## Method notes worth keeping

Three times a **green signal was lying**, and each was caught by deliberately
breaking the thing and checking the alarm sounded:

- A `-race` test passed against a knowingly-unlocked accessor. The reader loop
  finished before the writer goroutine was ever scheduled, so the two never
  overlapped and the detector had nothing to report. `-race` only witnesses what
  actually races; concurrency tests need a barrier proving both sides ran.
- `exec-bit-check` globbed only `*.sh`, so it would never have covered the new
  hook — and a hook that is not executable does not run *and does not complain*.
- An impact claim written into #540 turned out not to be reachable
  (`MaxEarlyQuitPenaltyLevel` bounds every path); the issue was corrected before
  the fix shipped.

The pattern is the same each time: **absence of a complaint read as a pass.** For
anything whose success condition is "nothing happened", the test is only evidence
once you have watched it fail.
