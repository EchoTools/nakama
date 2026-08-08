# Handoff: detection, CI coverage, and the end of the push-safety campaign

Written for the next seat, 2026-08-08. Covers **both** sessions of this campaign:
the 2026-08-07 concurrency/push-safety work and the 2026-08-08 detection/CI work
that followed it.

Companion documents, both still current for what they cover:

- [`campaign-2026-08-07-concurrency-and-push-safety.md`](campaign-2026-08-07-concurrency-and-push-safety.md)
  — the narrative, facing the repo's history
- [`handoff-push-safety-and-open-candidates.md`](handoff-push-safety-and-open-candidates.md)
  — the previous seat's operational state. **Its §"Do not read 'gates built' as
  'main is protected'" and its CANDIDATE list C1–C4 are still live and worth
  reading.** Its "Retained state" section is now stale; see below.
- [`test-plan-v320-to-main.md`](test-plan-v320-to-main.md) — what to actually
  test before release

---

## The one thing to read if you read nothing else

**CI green is not currently a statement about this codebase.**

Three separate facts, each filed, combine to make that true:

| | fact | issue |
|---|---|---|
| 1 | The `server` package — where all EVR code lives — **does not execute in CI at all**. With a database reachable the test binary aborts in 0.07 s on a missing `DISCORD_BOT_TOKEN`. It normally takes ~148 s. | **#553** |
| 2 | `internal/` is covered by **no routine gate** and has a red test in it right now. Every `just` test recipe scopes to `./server/...`. | **#554** |
| 3 | The compose workflow that would run `./...` is `workflow_dispatch`-only — its `pull_request` trigger is commented out — so **no test gate runs on PRs at all**, and it could not complete anyway because of (1). | — |

The checks that *are* green on a PR — gofmt, exec bits, CodeQL, build, and the
DB-free `./server/...` suite — are real. They are not coverage of the behaviour
that matters. Do not let a green check stand in for a manual pass.

This was found by measuring the suite, not by reading it, and it is why the test
plan treats every manual pass as load-bearing.

---

## What this campaign did

### Session 1 — 2026-08-07, concurrency and push safety (16 PRs merged)

| PR | |
|---|---|
| #527 | `SuspensionProfile` actually serves suspension data |
| #528 | per-test-binary memory cap in a cgroup, for correct attribution |
| #530 | assorted one-off correctness fixes (Tier-1 batch) |
| #531 | moderator entrant claim scoped to the lobby's guild; degraded VPN gate alertable |
| #532 | outage-safe guild pruning, real report-only mode, honest safety valves |
| #534 | match lifecycle — independent idle timers, bounded `MatchStart` retry, idempotent `MatchShutdown` |
| #535 | early-quit penalty-ladder totality, moderator sanctions as a floor, atomic completion credit |
| #542–#544 | `discordgo.State` reads moved under the lock (guild count, application ID, username) |
| #545 | withdrew a deprecation that pointed at the wrong API |
| #546 | pre-push hook refuses pushes resolving to `main` |
| #547 | CI audit flags any commit reaching `main` without a PR |
| #548 | campaign record |
| #549 | **restored the five pre-push checks #546 had silently destroyed** |
| #550 | previous handoff |

(#529, #533 and #536 are adjacent work merged 2026-08-06, before this campaign.)

### Session 2 — 2026-08-08, detection and CI coverage (2 PRs merged, 2 open)

| | |
|---|---|
| **#551 merged** | `fix(alts)`: search the machine fingerprint, not only compare it — closes the linking half of **#516** |
| **#552 merged** | `ci`: bound test-suite memory at the compose layer — closes **#526** |
| **#555 OPEN** | `build`: arm the tracked git hooks from any `just` recipe — closes **#557**. CI CLEAN, **awaiting a merge decision** |
| **#556 OPEN** | `docs`: the test plan. CI CLEAN, **awaiting a merge decision** |

Both open PRs are green and mergeable. Neither was authorised to merge.

---

## Open issues this campaign leaves

| issue | state |
|---|---|
| **#516** | Alt-linker. **Deliberately still open.** #551 closed the *linking* defect; three enforcement-policy items remain: first-sight reject on a banned machine, subnet/ASN reuse as a link signal, and the Nakama- vs Discord-account-age gate. The commit says `Refs #516`, not `Closes`. |
| **#553** | The `server` package does not run in CI. The most consequential of the three. |
| **#554** | `internal/` ungated, with a red test in it. Sibling of #553 — **neither fix catches the other.** |
| **#557** | Pre-push hook does not arm itself on a fresh clone. PR #555 attached. |

### One judgement recorded on #516, so it is not "fixed" by a later seat

`"N/A"` stays in `IgnoredLoginValues`. The issue lists its presence as evidence,
and removing it looks like an easy win. It is not: a nulled HMD serial is shared
by *everyone* who nulls it, so as a **linking key** it mass-links strangers.
Nulling the serial may well be an evasion signal — but that is a detection rule,
not a linking key, and it does not belong in that list.

---

## #541 is closed

The incident record for the second direct-push-to-`main`. Closed as completed,
with the measured state and an honest assessment rather than a claim of
resolution. Its mechanism gap was split out to **#557** so the record could close
without the gap going with it.

Two things are recorded there as **structurally impossible**, so nobody
re-attempts them:

1. **The hook cannot arm itself.** Git refuses to let a repository activate its
   own hooks — a self-arming clone would be arbitrary code execution on
   `git clone`. Security boundary, not oversight.
2. **The hook cannot complain when it is not armed.** An unarmed hook does not
   run, so there is no code at push time to notice its own absence. `git push`
   with no `core.hooksPath` is indistinguishable, from every observable position,
   from a push a hook approved. Anything that could detect the unarmed state
   would have to be something *other* than the hook running at push time — which
   is the same thing as being the hook.

The silence is not weak error handling. It is the shape of the problem.

Branch protection remains the only unbypassable prevention, remains a repository
setting rather than a file, and remains **the owner's unresolved decision**. Not
enabled, not requested, not worked around. Same for `push.default = simple`.

---

## Read this before your first push

**This seat arrived unarmed.** Not hypothetically — measured, on arrival:

```
$ git config --get core.hooksPath
/home/andrew/src/nakama/.git/hooks      ← not .githooks; the guard was OFF
$ git config --get push.default
tracking                                 ← #541's trap, live
```

A fresh clone, a newly created worktree, and an agent starting cold are all
unarmed by default — and that is exactly the population the destination guard
exists to protect. The guard was off for the class of user most likely to trip
the thing it guards against. **This is #541's thesis made physical rather than
argued**, which is why it is the last section here.

I armed it before doing anything else, and every push this session used an
explicit refspec.

**So, first, before you touch anything:**

```bash
git config --get core.hooksPath     # must print .githooks
just hooks                          # if it does not
```

Once #555 lands, any `just` recipe arms the clone. Until then it is manual.
`just --list` does **not** arm it even after #555 — just evaluates variables
lazily and `--list` does not trigger them.

And regardless of any of the above:

```bash
git push origin HEAD:refs/heads/<branch>     # explicit refspec, every time
```

`git push -u origin <branch>` names the branch twice and can still land on
`main`. That has happened **twice** in this repository. The hook is a backstop
for when you forget, not a licence to.

---

## Method notes that earned their place this session

The previous handoff's method note — *absence of a complaint read as a pass* —
held up three more times. Recording the new shapes rather than repeating it.

### A measurement can succeed and measure nothing

The first suite memory measurement returned a clean `2.98 GiB` after 57 seconds.
The number was worthless: the `server` package had died in 0.077 s against a
database with no schema, so the largest package was never measured. Exit code and
elapsed time were both plausible. The tell was that 57 s is not what a `-race`
suite costs.

**Check that a measurement measured the thing, not just that it returned.**
Duration and exit status are not evidence of coverage. This is how #553 was
found.

### The proposed verification can be the wrong instrument

#526 proposed confirming the compose memory key with `docker compose config`.
That check cannot work — it prints both candidate keys happily, so it cannot
distinguish an honoured key from an ignored one. The runtime had to be asked
instead (`HostConfig.Memory` on the container that actually ran).

**Verify-first applies to the issue as well as to the code.** Two of #526's
stated premises did not survive checking, including a "schema trap" that does not
apply at this Compose version.

### A fix can ship its own landmine

Adding the machine fingerprint to alt *discovery* (#516) was correct and
insufficient. Every account that logs in without `SystemInfo` emits the identical
string `Unknown::::::::0::0::0::0` — not a rare fingerprint but a **bucket**.
Unguarded, the fix would have linked every profile-less account to every other
one on its first run. That string was already in production data on every such
account, inert only because nothing queried for it.

The guard was proven load-bearing by disabling it and watching two unrelated
accounts link. **When a change widens a matching rule, ask what the most common
value of the new key is** — if the answer is "the empty case, and lots of
accounts have it", that is a bucket, not a key.

---

## Retained state

**Nothing is retained.** The previous handoff retained `wt535` (the #535 rebase)
until 2026-08-14. That worktree's directory is gone — the host cleaned the
previous session's scratchpad, which that handoff explicitly anticipated as
acceptable. `git worktree list` still shows it as `prunable`; run
`git worktree prune` if the noise bothers you.

Nothing is lost: `rebase/535` and every other campaign branch remain on the
remote.

This session's worktrees live under
`/var/tmp/claude-1000/.../scratchpad/wt{516,526,541,-main,-testplan}` and are
scratch — every branch in them is pushed.

**No branch created in either session has been deleted.** Branch deletion was
never authorised, and worktree removal and branch deletion are separate
operations with different reversibility. Do not read authorisation for one as
covering the other.

**#475 is untouched**, in both sessions, as instructed.
