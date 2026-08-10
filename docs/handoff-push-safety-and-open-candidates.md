# Handoff: push safety, open candidates, retained state

Written for the next agent or engineer picking this up, so it does not have to be
re-derived. The campaign narrative is in
[`campaign-2026-08-07-concurrency-and-push-safety.md`](campaign-2026-08-07-concurrency-and-push-safety.md);
this file is the operational state.

Last updated 2026-08-07.

> **A second session followed this one.** See
> [`handoff-2026-08-08-detection-and-ci-coverage.md`](handoff-2026-08-08-detection-and-ci-coverage.md)
> for current state.
>
> Still current here, and worth reading in full: the section immediately below,
> and the CANDIDATE list C1–C4.
>
> **Stale here:** "Retained state" — `wt535` is gone (the host cleaned that
> session's scratchpad, which this file anticipated as acceptable; nothing was
> lost, `rebase/535` is on the remote). And the pre-push hook's arming gap named
> below now has a fix in flight — issue #557, PR #555 — though the *residual*
> gap it describes is permanent and correctly stated.

## Do not read "gates built" as "main is protected"

There are two mechanisms guarding `main`. **Neither is prevention that arms
itself**, and the difference matters more than their existence.

### 1. `.githooks/pre-push` — prevents, but only once installed

**Status: restored and extended as of #549.** It was NOT added in #546. It
already existed (blob `70294f159`, from `df5949b3a`), #546 replaced it wholesale
and silently dropped its five checks, and #549 restored them and re-added the
destination guard as a phase of that hook rather than instead of it. If you are
reading commit history, `#546` alone will mislead you.

Phases, in order:

| phase | check |
|---|---|
| 0 | **Destination** — refuses a push that *resolves* to `main` (added #546, re-integrated #549) |
| 1 | Tag format — `v*` tags must carry `-evr.<N>` (inherited) |
| 2 | `gofmt` on changed files (inherited) |
| 3 | `go vet ./server/...` (inherited) |
| 4 | `gopls` diagnostics (inherited) |
| 5 | `go mod tidy` drift (inherited) |

**What it does not cover:**

- **It does not arm itself.** `core.hooksPath` is local config and cannot be
  committed. Until `just hooks` runs in a given clone, git never looks at
  `.githooks/` and every push is unguarded — with no warning, because an unwired
  hook is indistinguishable from one that approved. A fresh clone, a newly
  created worktree, and an agent starting cold are all unarmed by default, which
  is exactly the population phase 0 exists for. Check with
  `git config --get core.hooksPath`.
- **`git push --no-verify` skips the whole hook**, phase 0 included.

### 2. `.github/workflows/main-push-audit.yaml` — arms itself, but only detects

Flags any commit that reached `main` without an associated PR. Server-side, so a
clone that never ran setup cannot skip it. **It is post-hoc**: when it goes red
the commit is already on `main` and already fetchable. It converts a silent
direct push into a dated, red one. It cannot stop one.

An API error is reported as UNKNOWN and does not fail the run — a flaky call must
not read as a policy violation, or the check trains people to ignore its red.

### 3. Branch protection — the real answer, and not ours

GitHub branch protection requiring a PR to land on `main` is the only
unbypassable prevention. **It is not enabled.** It is a repository setting rather
than a file, and it constrains every human with push access — including the
owner pushing to his own `main`. It was surfaced to the repository owner and is
unresolved. Do not enable it, do not request it, and do not work around its
absence.

Same reasoning applies to setting `push.default = simple` for this repo, which
would remove the trap at its source. Owner's call.

## Why phase 0 exists

A branch created with `git worktree add <dir> -b <branch> origin/main` **tracks
`origin/main`**. With `push.default=tracking`, git resolves a push destination
from the branch's *upstream*, not its name — so

```
git push -u origin my-feature-branch
```

names the feature branch twice and pushes it to `refs/heads/main`. It does not
look wrong at the call site.

**This has happened twice in this repository.** The rule that prevents it —
always use an explicit refspec, `git push origin HEAD:refs/heads/<branch>` — was
written in prose after the first occurrence and did not stop the second, a month
later. That is why the control now lives in a hook.

`#541` holds the incident record and both gates. It is open on purpose.

## Open CANDIDATEs — known defect classes with no mechanical detector

These are recorded so a second occurrence is recognized as a *second* occurrence
rather than met as a fresh surprise. None of them is a suggestion to go build
something; each is an honest statement that the general case is unsolved.

### C1 — Adding a method to a shared test double silently disables subclass fault injection (n=1)

Go embedding does not dispatch virtually. When a base test double gains a method
that internally calls another of its own methods, that call resolves to the
**base's** implementation — not to a subclass override. A double that overrides
`StorageWrite` specifically to inject a fault therefore stops injecting on any
path that reaches it through the new method, and the test passes for the wrong
reason.

It does not fail cleanly. In the observed instance the write succeeding sent the
code down a branch the test had not set up, and it died as a nil-pointer panic in
unrelated state — a symptom pointing away from the cause.

**Predicate:** any method added to a shared test double that internally calls
another method of that double, where subclasses override the callee to inject
faults.

**Current guard:** a `CAUTION` comment on `occTestNakamaModule.MultiUpdate`
(`server/evr_latencyhistory_test.go`) and explicit `MultiUpdate` overrides on the
three doubles that needed them. That is documentation, found only by someone
already editing that file.

**Trigger, recorded on #394:** #394's remaining tasks are all "convert more call
sites to `MultiUpdate`", so each one drags its call site's tests onto the batch
path. **If #394 produces a second instance, build the meta-test** — each
fault-injecting double must be asserted to actually fault on the batch path —
rather than adding a fourth override and another comment.

### C2 — A concurrency test that never achieves concurrency (n=1, general case UNSOLVED)

A `-race` test in this repo passed against a deliberately unlocked accessor. The
reader loop ran to completion before the writer goroutine was ever scheduled, so
the two accesses never overlapped and the detector had nothing to report. A green
`-race` run proved nothing.

The instance was fixed by adding a start barrier — the reader waits for the
writer's first write — and the fix is witnessed: red against the unlocked
accessor, green against the locked one.

**The general form is unsolved. There is no lint for "this test spawns a
goroutine and never synchronizes with it."** Nothing prevents the next
concurrency test in this repo from being written without a barrier and passing
for the same empty reason. I fixed the instance; I did not solve the class.

Any new `-race` test here should be run once against a deliberately broken
version of the thing it guards. If it does not go red, it is not evidence.

### C3 — Installing into an occupied slot without enumerating what is already there (n=1)

**Before installing a hook, config, or gate that occupies a fixed path:
enumerate what already occupies that path and what it does. A gate that replaces
a gate must state what it inherited and what it dropped.**

This is verify-first applied to the *slot* rather than to a claim. It is the one
place verify-first was skipped during this campaign, because a hook path does not
look like a premise. It is one.

The observed instance: `#546` wrote `.githooks/pre-push` without checking whether
it existed. It did, with five checks, documented in `AGENTS.md` the whole time.
Five gates were deleted to install one. The resulting report of what was covered
was careful, itemized, and wrong at the root — because it never asked what was
already there. Fixed in #549.

The tell was in hand and unread: `mkdir -p .githooks` assumes the directory is
yours to create.

### C4 — A guard that reports without preventing (n=1 confirmed, class is live)

Phase 1 of the pre-push hook — the tag-format gate — **never worked, from the day
it was written**. It piped refs into `while read`, which runs the loop in a
subshell, so every `fail_count` increment landed in a copy and the summary always
saw zero:

```
$ printf 'refs/tags/v3.27.2 ...' | orig-hook
  ✗ Tag 'v3.27.2' rejected — EVR releases use v3.27.2-evr.<N>
EXIT=0        ← push allowed anyway
```

It printed a refusal and allowed the push. Nobody was careless: whoever wrote it
watched it print a rejection and reasonably concluded it worked. Fixed in #549
with here-strings, and verified red-green across all five phases.

**A guard is only proven by watching it block.** Printing "refusing" is not
evidence. Test every gate against the real tree, in both directions — the
violation must be refused *and* the legitimate case must pass, because a gate
that blocks everything is equally broken.

## Method note that generalizes

Three times during this campaign a **green signal was lying**, and each was
caught only by deliberately breaking the thing and confirming the alarm sounded:
the `-race` test above; `exec-bit-check`, whose glob missed the very file it had
just been extended to cover; and an impact claim in an issue that turned out not
to be reachable.

The shape is always the same: **absence of a complaint read as a pass.** For
anything whose success condition is "nothing happened", the test is evidence only
after you have watched it fail.

## Retained state

**`wt535`** — a git worktree at
`/var/tmp/claude-1000/.../scratchpad/wt535` on branch `rebase/535`, holding the
rebase of #535 onto `main`. Retained deliberately: #535 was the most invasive
merge of the campaign (eleven commits, two conflict resolutions taken from the
record rather than guessed), and the worktree is the fastest way to inspect what
was resolved if something surfaces.

*"Boring for a while"* concretely: it can be removed once #535's changes have
been running in production long enough to have seen real traffic through the
early-quit completion path — the atomic completion credit and the penalty-ladder
resolution — without a related report. **A week of production with no early-quit
anomaly is a reasonable bar.** After 2026-08-14, absent such a report, remove it.

Note the scratchpad is session-scoped temporary storage and may be cleaned by the
host before then. That is acceptable: everything in it is pushed. `rebase/535` and
every other campaign branch remain on the remote.

**Branches**: no branch created during this campaign was deleted. Branch deletion
was never authorized, and worktree removal and branch deletion are separate
operations with different reversibility — do not read authorization for one as
covering the other.
