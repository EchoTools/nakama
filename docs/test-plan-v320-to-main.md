# Test plan: `v3.27.2-evr.320` → `main`

Covers the 137 commits (26 merged PRs) between tag `v3.27.2-evr.320` and `main` at
`a681d7a61`.

```
$ git rev-list --count v3.27.2-evr.320..origin/main
137
```

**Written 2026-08-08.** Regenerate the ranges below against the actual release
commit before using this — the count moves every time `main` does.

---

# Assumptions

## **CI green is not currently a statement about this codebase.**

Read that before planning any of the work below, and do not let a green check
mark on a PR stand in for it.

**Every manual test in this plan is load-bearing, because the automated suite is
a subset of the codebase rather than a gate over it.** Three separate facts
combine to make that true, and each is filed:

| | fact | issue |
|---|---|---|
| 1 | The `server` package — where all EVR code lives — **does not execute in CI at all**. With a database reachable the test binary aborts in 0.07 s on a missing `DISCORD_BOT_TOKEN`. It normally takes ~148 s. | #553 |
| 2 | `internal/` is covered by **no routine gate**, and has a red test sitting in it right now. Every `just` test recipe runs `./server/...` only. | #554 |
| 3 | The compose workflow that would run `./...` is `workflow_dispatch`-only — its `pull_request` trigger is commented out — **so no test gate runs on PRs at all**, and it could not complete anyway because of (1). | — |

The checks that *are* green on a PR — gofmt, exec bits, CodeQL, build, and the
DB-free `./server/...` suite — are real and worth having. They are also not
coverage of the behaviour this plan is about. **A reader who confuses the two
will skip the manual passes and ship untested subsystems.**

Corollary for whoever runs this plan: when a manual step here disagrees with
"but CI is green", the manual step wins. There is no automated result that
contradicts it, because for these subsystems there is no automated result at
all.

## The evidence behind that

Both findings came from measuring the suite while writing this plan, not from
reading it.

**The `server` package does not run in CI at all.** With a database reachable,
the whole test binary aborts in 0.07 seconds:

```
FATAL  Error returned by InitModule function in Go module
       {"name": "evrRuntime", "error": "DISCORD_BOT_TOKEN is required but not set"}
FAIL   github.com/heroiclabs/nakama/v3/server	0.072s
```

`evr_runtime.go:107` hard-fails without a Discord token, `logger.Fatal` exits the
process, and every EVR test in the package dies with it. The package normally
takes ~148 s. `docker-compose-tests.yml` provisions a database and does not set
the token. `just test` does not hit this, because without a database those tests
skip before constructing the runtime — so the gap is invisible from the local
suite. Filed as **#553**.

**`internal/` is not covered by any routine gate,** and has a red test sitting in
it (`TestIntent_MarshalText`, `"storage"` vs `"storage_objects"`). Every `just`
test recipe runs `./server/...` only; `./...` appears solely in the compose
workflow, which is `workflow_dispatch`-only and blocked by the above. Filed as
**#554**.

A green `just test` is the floor, not the verification. Prioritise the
subsystems marked ⚠ — they are player-facing, destructive, or both.

## What this plan is grounded in

Read directly: all 133 commit subjects, the file-change frequency across the
range, per-PR diffstats, and the full diff of `evr_global_settings.go` and
`evr_group_metadata.go` (the operator-facing knobs, which is where manual
verification gets its levers). Individual commit diffs were **not** read
end-to-end. Where a row below rests on a commit subject rather than on read
code, it says so.

## Test environment

```bash
GOWORK=off                    # a gitignored go.work at the repo root
                              # references missing sibling modules
just test                     # DB-free suite (./server/...)
just test-db                  # adds DB-backed tests; needs TEST_DB_URL
go test -race -count=1 ./server/...
```

Baseline at `90b28ba4b`: `./server/...` green under `-race` in **148.7 s**. Full
`./...` peaks at **1.04 GiB** aggregate (cgroup `memory.peak`, `-p 4`, cold
cache).

A local database for `test-db`:

```bash
docker run -d --name nakama-testdb -e POSTGRES_DB=nakama \
  -e POSTGRES_PASSWORD=localdb -p 5433:5432 postgres:16.8-alpine
GOWORK=off ./nakama migrate up \
  --database.address 'postgres:localdb@127.0.0.1:5433/nakama'
export TEST_DB_URL='postgresql://postgres:localdb@127.0.0.1:5433/nakama?sslmode=disable'
```

`TEST_DB_REQUIRED=1` makes an unreachable database a hard failure instead of a
silent skip — set it, or `test-db` can pass vacuously.

---

# 1 ⚠ Early quit and penalty enforcement

**The largest and highest-risk cluster in the range.** 14 commits touch
`evr_earlyquit.go` alone, and the subsystem moved from advisory to *enforcing*:
it now applies real, player-visible lockouts. Nearly every commit in it is a
correctness fix to state that persists across sessions, so a defect does not
show up as a crash — it shows up as a player who is punished wrongly, or not
punished at all, and neither is visible from a log line.

### What changed

| Commit | Change |
|---|---|
| `f00ae1839` | Resolve and enforce penalty lockout server-side |
| `712803c39` | Server-side enforcement, initially gated to test users |
| `f2901629b` | Guild-level gating (#517) — `enforce_early_quit_penalty`, default **false** |
| `9775b57f3` | Wire-format corrections for `FeatureFlags` and `Config` |
| `1aadf78fd` | Penalty-ladder resolution made total; lockout saturates |
| `dd112cac1` | An early quit must not erase a moderator-applied penalty |
| `52a3ebaeb` | A moderator sanction is a floor in **level and expiry**, not just level |
| `182d9ccc3` | A lockout-less sanction must not leave a permanent phantom level |
| `098763725` | Completion dedupe record and its credit committed **atomically** |
| `468522d37` | An unreadable history row must not roll back the credit |
| `1a7293daf` | Lockout re-resolved when a quit is forgiven on logout |
| `1906d72ea` | `TrackMatchCompletion` failure logged at Warn, not Debug |
| `77abfc58c` | Validation claims corrected; newly-live path covered |
| `23d33acff` | A deprecation withdrawn after measurement showed it pointed at the wrong API |
| `cdf9c8c25` | Three dead functions removed |

### What needs testing

- **The gate itself.** `enforce_early_quit_penalty` defaults to `false`. The
  single most important negative test in this whole plan is that a guild which
  has *not* opted in sees **no** behaviour change. A regression here punishes
  players in guilds that never asked for it.
- **The penalty ladder is total.** Every level resolves to a lockout; no input
  produces an unhandled level. Saturation at the top must not wrap or reset.
- **Moderator sanction is a floor in both dimensions.** An early quit occurring
  during an active moderator penalty must not lower the level *or* shorten the
  expiry.
- **Atomic completion credit.** Dedupe record and credit commit together. This is
  the one to test under induced storage failure — partial commit is exactly the
  bug `098763725` fixes, and it is invisible until a player is double-charged or
  never charged.
- **Forgiveness on logout** re-resolves the lockout rather than leaving stale
  state.
- **Wire compatibility** of `EarlyQuitConfig` and `EarlyQuitFeatureFlags` against
  a real client. `9775b57f3` corrected the formats; a mismatch here is a client
  that mis-renders or ignores its own penalty state.

### Manual verification

1. **Opt-out is inert.** On a guild with `enforce_early_quit_penalty` unset:
   join a match, quit early, confirm no lockout is applied and no penalty DM
   arrives. Repeat with the value explicitly `false`.
2. **Opt-in ladder.** Set `enforce_early_quit_penalty: true` on a test guild.
   Quit early repeatedly and record level and lockout expiry after each. Confirm
   the ladder climbs monotonically and saturates rather than wrapping.
3. **Moderator floor.** Apply a moderator penalty, then quit early during it.
   Confirm neither level nor expiry decreases. Then let the moderator penalty
   expire and confirm the early-quit level resumes from the correct rung.
4. **Completion credit.** Complete a match to the end. Confirm exactly one
   completion credit is recorded — check for a second run through the same match
   ID producing no additional credit.
5. **Client rendering.** With enforcement on and a lockout active, confirm the
   in-game client shows the correct remaining lockout. This is the wire-format
   check and it cannot be done from the server side.
6. **Cross-guild isolation.** A lockout earned in guild A must not apply in
   guild B. Matchmaking is per-guild by invariant; confirm enforcement respects
   the same boundary.

---

# 2 ⚠ Suspensions and the enforcement journal

Player-facing bans. `evr_suspension_profile.go` changed in 11 commits. Several
fixes are to the **storage index**, which means a defect presents as a
suspension that silently fails to apply — the worst failure mode for this
subsystem, because nothing complains.

### What changed

| Commit | Change |
|---|---|
| `7295e1e65` | **Index `Fields` discarded the entire suspensions array** |
| `5287b864f` | `MaxEntries` raised; index capacity made observable |
| `41246b869` | Voided records dropped from the projection, not annotated |
| `2636c436c` | Suspensions record which game modes they apply to |
| `ee6a6c47f` | Journal and profile written in one transaction |
| `f9d3732f0` | Journal and profile via `nk.MultiUpdate`; stop retrying blindly |
| `a271a6957` | Enforcement merges on retry instead of writing a stale journal |
| `0b9de7fdf` | Community-values gating hole in the journal merge closed |
| `c6b042b9b` | Corrupt-record recovery kept self-healing; `NotFound` merge base must not clear a pending community-values requirement |
| `b5dd10aa6`, `f452194fa` | Scope contract and index rationale documented |

### What needs testing

- **The index actually indexes.** `7295e1e65` means suspensions were being
  dropped from the index wholesale. Verify a written suspension is *findable*,
  not merely stored.
- **Index capacity.** `MaxEntries` was raised. Confirm the new ceiling is not
  reached in production volumes and that the capacity gauge reports truthfully.
- **Game-mode scoping.** A suspension scoped to arena must not block combat, and
  vice versa.
- **Void semantics.** A voided suspension disappears from the projection
  entirely. Confirm a voided record does not block, and does not reappear.
- **Transactional write.** Journal and profile land together or not at all.
  Induce a storage failure between them and confirm no half-state.
- **Community values.** A pending community-values requirement must survive a
  `NotFound` merge base. This is subtle and worth a dedicated pass: the bug was
  that recovery *cleared* the requirement.

### Manual verification

1. Issue a suspension via the moderator path. Confirm the target cannot join,
   and that the suspension is returned by the lookup RPC.
2. Void it. Confirm the target can join immediately and the record is gone from
   the projection rather than annotated.
3. Issue a suspension scoped to one game mode. Confirm the other mode is
   unaffected.
4. Set a community-values requirement, then trigger a merge against a missing
   base. Confirm the requirement still stands afterwards.
5. Check the index capacity gauge against the live suspension count and confirm
   headroom.
6. Confirm the suspension DM arrives, and that `suspension_dm_footer` on the
   guild is honoured when set and falls back to the default when empty.

---

# 3 ⚠ Matchmaker

### What changed

- `d3e7e5549` (**#523**) — solve the team partition in prediction rather than
  only testing for it. `evr_matchmaker_prediction.go` +93,
  `evr_matchmaker_process.go` reworked, ~490 lines of new tests.
- **Ambassador program** — new `evr_global_settings.go` knobs:
  `enable_ambassador_program` (default false), `ambassador_mu_reduction` (10.0),
  `ambassador_cooldown_matches` (1), `ambassador_min_games_played` (200),
  `ambassador_min_mu` (30.0).
- `disable_matchmaker` — a global kill switch; tickets rejected immediately.
### The guild invariant now has exactly one enforcement point — test it directly

`CLAUDE.md` states cross-guild matchmaking must never exist. Worth stating
precisely where that is enforced today, because #523 changed the answer and the
commit message alone is misleading.

#523 *added* a guild-scoping block inside `groupEntriesSequentially` plus a test
named `TestGroupEntriesSequentiallyNeverMixesGuilds` — and then **removed both
again in the same PR** (last line of its commit message). Neither is on `main`.
Do not go looking for that test; it was deliberately deleted, not lost.

The removal is correct, and the reasoning is worth having in hand rather than
re-deriving. Separation is enforced *upstream*, at the ticket query:

```go
// server/evr_lobby_parameters.go:804
fmt.Sprintf("+properties.group_id:%s", Query.QuoteStringValue(p.GroupID.String()))
```

Every ticket carries a required `group_id` term, and Nakama's matchmaker
requires tickets to satisfy each other's queries *mutually*, so two tickets from
different guilds cannot match in the first place. The in-packer guard was
genuinely redundant.

**But it is now the only guard.** A redundant second check was removed, so a
regression in that one query line is no longer caught downstream. That makes it
worth a direct, deliberate test rather than an assumed one.

### What needs testing

- **The guild invariant, empirically** — not by reading the packer. Confirm no
  path sets `GroupID = uuid.Nil` in `MatchmakingStream`, `GuildGroupStream`,
  `MatchmakingParameters`, or `BackfillSearchQuery`, and that
  `evr_lobby_parameters.go:804` still emits the `group_id` term.
- **Team balance.** The partition solver is the change; team quality is the
  observable. Compare predicted outcomes before and after against the saved
  benchmark baseline.
- **Ambassador defaults are off.** `enable_ambassador_program` is a `*bool`
  defaulting to false. Confirm the unset case behaves exactly as before.
- **Ambassador eligibility.** All four thresholds gate correctly at their
  boundaries — a player at exactly `min_games_played` or exactly `min_mu`.
- **`disable_matchmaker`** rejects tickets immediately and cleanly, and that
  players get a usable message rather than a hang.

### Manual verification

1. `just bench-check` — the benchmark regression gate on `BenchmarkPredictOutcomes`.
2. Queue enough players in a single guild to form a match. Confirm teams are
   balanced and that every player in the match belongs to that guild.
3. **The invariant test.** Queue players in guild A and guild B *simultaneously,
   in the same game mode, in the same matchmaking cycle* — that combination is
   the only one that can expose it. Confirm two separate matches form and no
   player crosses. Do this every release: since #523 the ticket query is the
   sole enforcement point.
4. Toggle `disable_matchmaker: true`. Confirm tickets are rejected promptly and
   the client shows something sensible. Toggle back and confirm recovery without
   a restart.
5. With the ambassador program off, confirm mu is unmodified for a player who
   would otherwise qualify.

---

# 4 Match lifecycle

### What changed

| Commit | Change |
|---|---|
| `0e9d7017b` | `MatchShutdown` made idempotent |
| `2561e3941` | `MatchShutdown` keeps draining when the label update fails |
| `d9a65148c` | `MatchStart` retry bounded; a failed start must not forge `Started()` |
| `a16d97f7c` | `MatchStart` retries throttled on elapsed ticks, not tick boundaries |
| `306100e4d` | Each `MatchLoop` idle timer gets its own counter |
| `4a3f64520` | Match callbacks stop returning `nil` on transient failures |

`evr_match.go` is the single most-touched file in the range (15 commits).

### What needs testing

- **Idempotent shutdown** — calling twice is safe; the second is a no-op, not a
  double-drain.
- **A failed start must not report success.** `Started()` forging is the
  dangerous one: downstream logic trusts it.
- **Retry throttling on elapsed ticks.** The distinction from tick boundaries
  matters under load, when ticks bunch. Test under a slow/loaded server, not an
  idle one.
- **Per-loop idle counters** — one match timing out must not affect another's
  countdown. Requires ≥2 concurrent matches to observe at all.
- **Transient failure handling** — a callback returning non-nil must terminate
  the match cleanly rather than wedging it.

### Manual verification

1. Start a match, force a shutdown, then force it again. Confirm no panic, no
   double-drain, and players are moved out once.
2. Point a lobby at an unreachable game server. Confirm the retry bounds out and
   the match does not report itself started.
3. Run two matches concurrently, let one go idle to timeout. Confirm the other's
   timer is unaffected.
4. Under load, confirm `MatchStart` retries are spaced by wall time rather than
   bunching with tick delivery.

---

# 5 ⚠⚠ Discord guild sync and pruning — DESTRUCTIVE

**The highest blast radius in the range.** This code *deletes Nakama groups* and
*leaves Discord guilds*. A false positive destroys a live guild's data. The
commits are overwhelmingly about not doing that.

### What changed

| Commit | Change |
|---|---|
| `0fb82347e` | **Never prune a guild's group during a Discord outage** |
| `d478a9cd2` | Prune writes gated behind `do*` flags and the safety valve |
| `aa26ac967` | Report-only mode; honest safety valves; wiring tests |
| `2d94856b3` | Reconciliation is a repair, not a prune action |
| `e5b7a9f68` | Guild ID cache purged when prune deletes a group |
| `e8e1ed829` | Do not dereference a nil `*GuildGroup` on `GUILD_DELETE` |
| `dc82fcaf7` | Truncate group name/description by **characters, not bytes** |
| `054510f78` | Truncate for group writes; reconcile orphan guilds before prune-leave |
| `198fa1ac1` | Guild Create log field typed; prune wire keys pinned |
| `70178b31e` | Forensic delete log covered |

New settings: `leave_orphan_guilds`, `leave_orphan_groups`, `safety_limit`,
`report_only`.

### What needs testing

**Test `report_only: true` exhaustively before ever setting it false.**

- **Report-only actually reports and does not act.** The whole safety story
  rests on this one behaviour.
- **Safety limit aborts.** Exceeding `safety_limit` aborts the operation
  entirely — not "prunes up to the limit". Confirm which, because those are very
  different and the abort is the safe one.
- **Outage suppression.** During a Discord outage, prune must decline. This is
  the four-state guild model: an unreachable guild is *unknown*, not *absent*.
  Simulate by revoking the bot's access or blocking the Discord API.
- **Character truncation.** A guild name with multi-byte characters (emoji, CJK)
  must truncate on a character boundary. Byte truncation produces invalid UTF-8
  and a corrupt group name. Test with an emoji-heavy name at exactly the limit.
- **Cache coherence.** After a prune deletes a group, the guild ID cache must not
  still resolve it.
- **`GUILD_DELETE` with no matching group** must not panic.

### Manual verification

1. With `report_only: true`, run reconciliation against production data. Read
   every line of the report. Confirm each proposed action is one you would
   endorse. **This is the gate for everything else in this section.**
2. Set `safety_limit` below the reported count. Confirm the operation aborts and
   changes nothing.
3. Block Discord API access. Confirm prune declines and says why, rather than
   treating every guild as orphaned.
4. Create a test guild named with emoji at the length boundary. Confirm the
   stored group name is valid UTF-8 and visually sensible.
5. Remove the bot from a test guild. Confirm `GUILD_DELETE` is handled without a
   panic and the forensic delete log records it.
6. Only after 1–5: enable one `do*` flag, on one guild, and verify the outcome
   before widening.

---

# 6 Discord state concurrency

### What changed

Five commits converting unlocked `discordgo.State` reads to locked accessors —
`cd26981ff` (guild state snapshot under read lock), `b776abafe` (bot's own ID),
`465dc8cab` (application ID), `d5af563f8` (username), `d0c160016` (guild count).
Issues #537 and #538.

### What needs testing

Data races are not reproducible on demand, so **testing is `-race` plus load**,
not a functional pass.

- `go test -race -count=1 ./server/...` green — necessary, nowhere near
  sufficient.
- **The barrier caveat from the campaign handoff applies directly here.** A
  `-race` test in this repo previously passed against a deliberately unlocked
  accessor because the reader finished before the writer was ever scheduled. If
  you add a concurrency test in this area, run it once against an unlocked
  version and confirm it goes **red**. A green `-race` run on a test that never
  achieved concurrency proves nothing.

### Manual verification

1. Run the server against a bot in many guilds, under `-race` if a race-enabled
   build is available, through a reconnect cycle (Discord gateway resume).
2. Trigger simultaneous guild join/leave activity while the bot serves commands.
3. Watch for the race detector, and for the symptom class: a guild count or bot
   identity that reads as zero/empty for one request and correct for the next.

---

# 7 ⚠ Security gates

### What changed

| Commit | Change |
|---|---|
| `dea9a8110` | SEC-5: unauthorized moderator role claims downgraded |
| `5b5b2890e` | Moderator entrant claim scoped to the lobby's own guild |
| `2809c7ede` | Guild scoping pinned in `isModeratorOfGroup` unit cases |
| `5ffc2e3aa` | Unreachable moderator role grant removed from setnextmatch |
| `6a6a61c31` | SEC-6: warn when VPN blocking is degraded by IPQS outage |
| `92feb178f` | Degraded-VPN alert must not fire forever without a provider |
| `68d72ebae` | Degraded VPN gate made alertable; stop it burying itself |
| `4ce4ef527` | SEC-6 observability paths hardened against their own edges |

### What needs testing

- **Moderator claims do not cross guilds.** A moderator in guild A must have no
  elevated rights in guild B's lobby. This is the SEC-5 fix and it is the one
  with real abuse potential.
- **The removed role grant was genuinely unreachable.** `5ffc2e3aa` removed code
  on the basis that nothing could reach it. Confirm no legitimate moderator
  workflow regressed — this is the kind of removal that is correct 95% of the
  time and silently breaks one admin path the other 5%.
- **VPN gate fail-state.** The repo's named anti-pattern is *fail-open on a
  fail-closed control*. When IPQS is down, decide and verify explicitly which way
  the gate fails, and that the alert fires **once and recovers** rather than
  either spamming forever or going silent.

### Manual verification

1. As a moderator of guild A only, attempt every moderator action against a
   guild B lobby. All must be refused. Try the entrant claim specifically.
2. Walk each moderator workflow that touches setnextmatch and confirm none
   depended on the removed grant.
3. Block IPQS. Confirm: the degraded warning fires, joins behave as the
   documented policy says they should, the alert does not repeat indefinitely.
   Restore IPQS and confirm the alert clears and normal gating resumes without a
   restart.
4. Confirm the degraded state is visible to an operator without reading logs.

---

# 8 Storage, OCC and `Storable`

Cross-cutting; a defect here surfaces in whichever subsystem happens to write
next, which makes it hard to attribute.

### What changed

| Commit | Change |
|---|---|
| `99176baee` | `StorableRead`'s "create only" made actually create-only |
| `5136eae04` | Version-conflict sentinel preserved through storable errors |
| `3f079a154` | Index the object's **write** permission, not a copy of its read |
| `e5d000afd` | Index acks by object identity, not by position |
| `16fa85f89` | Storage-index entry gauge re-emitted after eviction |
| `3d450e4b7` | A failed batch write stops blaming an object it cannot know |
| `d275b39f3` | `storableCreate` exhaustion left to fail honestly, and pinned |
| `f5d0b2f3e` | `indexOnly` mislabel fixed; field filter covered |

### What needs testing

- **`3f079a154` is a permission bug.** Indexing a read permission where a write
  permission belongs can expose or block the wrong thing. Verify index entries
  carry the write permission and that permission-scoped queries return the right
  set.
- **`e5d000afd` — acks by identity, not position.** Under batched writes with
  reordering, positional acks attribute results to the wrong object. Test a batch
  where results return out of order.
- **Version-conflict sentinel** survives wrapping, so retry logic can still
  recognise a conflict.
- **Create-only** genuinely refuses to overwrite.
- **Gauge after eviction** — the entry gauge must not drift permanently high
  after an eviction, or capacity monitoring lies.

### Manual verification

1. Write a storable with create-only semantics twice. The second must fail.
2. Force a version conflict from two concurrent writers. Confirm one retries and
   succeeds and neither silently drops a mutation.
3. Query a permission-scoped storage index and confirm the returned set matches
   write permissions.
4. Drive the index past its eviction threshold and confirm the gauge returns to
   truth afterwards.

---

# 9 Latency history

### What changed

`ac4d3031f` (#519) moved retry-on-conflict into `LatencyHistory` and removed
`StorableWriteWithRetry`. Then: `477f460bd` locks the marshal/unmarshal path,
`49c5b8e30` adopts the winner under `h`'s write lock, `43075042b` adopts the
winner on retry and drops a wasted final read, `536ea6f39` stops a null map
allocating on a nil receiver, `f2e30b36c` covers the `StorageMeta`/`SetStorageMeta`
locking contract.

### What needs testing

- Concurrent latency writes for the same user converge without losing samples.
- The nil-receiver path does not allocate (and does not panic).
- Retry adopts the winning value rather than re-reading or overwriting it.
- `-race` across `evr_latencyhistory_race_test.go` and the OCC tests. Same
  barrier caveat as §6: a concurrency test here is evidence only if it has been
  seen to fail against a broken version.

### Manual verification

1. Have several clients ping the same set of servers simultaneously; confirm
   recorded latencies are plausible and none are lost.
2. Confirm matchmaking still selects servers sensibly — latency history feeds
   server selection, so a silent regression shows up as bad server choice rather
   than an error.

---

# 10 Account and profile

### What changed

`3bfe729f8` (#520) made `EVRProfileUpdate` honest — `MultiUpdate`, no silent
mutation drops. `9129693f4` added retry on version conflict at three unretried
sites. `9ee14efbe` made the profile retry safe at its call sites. `7d8d10837`
added backoff between attempts. `3115f1d96` stopped `EVRProfile.MarshalMap`
truncating **int64** values. `f6401ba88` prunes with `EqualFold` on retry,
matching the non-retry path.

### What needs testing

- **`3115f1d96` — int64 truncation.** Anything large enough to exceed the
  truncated range was being silently corrupted. Identify which fields are int64
  (timestamps, IDs, counters) and verify large values round-trip exactly.
- **No silent mutation drops.** Two concurrent profile updates: both mutations
  must survive, or the loser must retry.
- **`EqualFold` on retry.** The retry path previously used different casing
  semantics from the first attempt, so a retry could behave differently from the
  original. Verify with a mixed-case display name.
- **Backoff** exists and does not turn a conflict into a stall.

### Manual verification

1. Set a profile field to a large int64 (a far-future timestamp works). Read it
   back and compare exactly.
2. Change display name and another profile field simultaneously from two
   sessions. Confirm both land.
3. Use a mixed-case display name and force a conflict retry. Confirm the outcome
   matches the non-retry path.

---

# 11 Login pipeline and lobby entry

### What changed

`f6401ba88` (prune with `EqualFold` on retry), `189f0fc44` and `9ee14efbe`
(loadout retry call sites), `3a0c8e760` (why the profile retry does not re-apply
in-game names — read this before testing display names), `92feb178f` (degraded
VPN alert), `18db0a25e` / `89fd90ed3` / `3529f2a37` (testability refactors —
`NewLobbyParametersFromRequest` takes its own `nk`; `LoadEarlyQuitServiceConfig`
takes a `runtime.Logger`; registries reached through narrow interfaces).

New settings: `ping_server_before_join`, `use_quest_encoder_flags`,
`enable_vibinators_gravity`.

### What needs testing

- **`use_quest_encoder_flags`** sends a Quest-shifted encoder bit layout in
  `LobbySessionSuccessv5` for standalone clients. This is a **wire format change
  discriminated by client type** — the highest-risk item in this section.
  Standalone Quest and PC clients must both be tested, with the flag on and off.
- **`ping_server_before_join`** — confirm the added ping does not extend join
  time unacceptably, and that a server failing the ping is handled rather than
  hanging the join.
- **Display names.** Three interacting systems (Nakama, in-game, Discord).
  `3a0c8e760` documents that the profile retry deliberately does not re-apply
  in-game names — verify that is still true and that names do not drift.
- The refactors in `#524` are behaviour-preserving by intent. Confirm login and
  lobby-parameter construction are unchanged.

### Manual verification

1. Log in from a **standalone Quest** client with `use_quest_encoder_flags` on,
   then off. Then the same from a **PC** client. All four combinations must join
   and render correctly. A wrong encoder layout may present as visual corruption
   rather than a failed join.
2. Enable `ping_server_before_join` and time a join against a healthy server and
   an unresponsive one.
3. Change a display name in Discord and confirm propagation matches documented
   behaviour in all three systems.
4. `enable_vibinators_gravity` is flagged as a novelty — confirm it is **off** in
   production config.

---

# 12 Build, CI and tooling

Not player-facing; verify by running, not by inspection.

### What changed

`26402b797` migrated make → just. `e89021e86` (#522) gofmt'd all non-generated
sources and added a CI formatting gate. `178075b30` (#521) made the suite
runnable with no CockroachDB and no Discord token. `214108e16` caps per-test-binary
memory in a cgroup (`scripts/go-test-limit.sh`). `fef8adcf2` re-checks
`systemd-run` before trusting the cached probe. `0d5fc48e7` tracks shell scripts
as executable with a regression guard (`just exec-bit-check`). `26114fd5e` + `ab3c98c01`
the pre-push hook (destination guard restored alongside its five inherited
checks). `21f37f4eb` the main-push audit workflow. `c2825b587` explicit
permissions on the build workflow. `684c895af` / `87924552e` `.gitignore` for
`tools/` build outputs.

**Added after this plan was first drafted:** PR #552 (`4b6543139`, merged as
`a681d7a61`) bounds test-suite memory at the compose layer — `test` 4g, `db` 2g,
`nakama` 2g, each with `memswap_limit` equal to `mem_limit`. `tests.yaml` gained
a pre-flight check that every service declares a limit, and a post-run step that
reads `HostConfig.Memory` off the containers that ran and **fails the job if any
ran with no limit applied**. That step also names an OOM kill rather than
leaving exit 137 to read as a mystery.

Note the interaction with the section above: this bounds the workflow that
**cannot currently complete** (#553). The limits are correct and verified —
planting a memory hog produces `oom_killed=true exit=137` and the reporting step
names it — but they will not have been exercised by a real suite run until #553
is fixed. First green run of that workflow, check the reported limits are
non-zero for all three services.

### Verification

```bash
just fmt-check        # formatting gate
just exec-bit-check   # tracked exec bits
just test             # DB-free suite
just nakama           # builds
just bench-check      # no benchmark regression
just act-lint         # workflow syntax
```

- Confirm `core.hooksPath` is `.githooks` in every clone and worktree you push
  from. As of PR #555 any `just` recipe arms it; before that it is manual.
- **Push with an explicit refspec**: `git push origin HEAD:refs/heads/<branch>`.
  `push.default=tracking` plus a worktree branch tracking `origin/main` has
  landed a commit on `main` **twice** in this repository (#541).
- `GO_TEST_MEMORY_LIMIT` defaults to `4G` per test binary; `off` disables.

---

---

# 13 ⚠ Alt detection and account linking

**Added to the range after this plan was first drafted** — PR #551 (`56e9a9c2d`)
merged as `5dc351220`, closing the linking half of #516. It is here because it
changes *who the server decides is the same person*, which is as player-facing
as anything in §1 or §2, and a false positive links an innocent account to a
banned one.

### What changed

`AltSearchPatterns` now includes the machine fingerprint (`SystemProfile`) among
the keys used to **discover** candidate alt accounts. Previously the fingerprint
was captured, indexed, and compared — but never searched on. Since
`loginHistoryCompare` only ever runs against candidates the index query already
returned, an account whose only overlap with a banned account was the machine
itself produced **zero edges**. Confirmed on account #11 (2026-07-13): an exact
`system_profile` match to three accounts banned 26 minutes earlier formed no
link.

A second change guards the first. `isDegenerateSystemProfile`, in
`matchIgnoredAltPattern`, drops any profile whose four descriptive fields
(headset type, network type, video card, CPU model) are all empty or the
`Unknown` placeholder.

### What needs testing

**The false-positive direction is the risk here, not the false-negative one.**
The fix widens who gets linked; that is its purpose, and it is also how it could
do harm.

- **Degenerate profiles must not link.** Every account that logs in without
  `SystemInfo` emits the byte-identical string `Unknown::::::::0::0::0::0`. That
  is not a rare fingerprint, it is a bucket. Unguarded, this change would have
  linked every profile-less account to every other one on its first run. **This
  string already exists in production data** — it has been written into the
  indexed `cache` of every profile-less account all along, inert only because
  nothing queried for it. Confirm that after deploy, no account acquires alt
  edges purely on a degenerate profile.
- **Commodity headsets must not link.** Quest profiles are low-entropy and are
  filtered by the pre-existing `IsWeakSignal` prefix check — **but that filter
  depends on `CommodityProfilePrefixes` being configured.** Verify the live
  config actually lists the current Quest prefixes. Unconfigured, a popular
  headset becomes a wide match, and this change moves that from the comparison
  side (bounded) to the discovery side (unbounded).
- **Query volume.** Every login now searches on one more key. Each returned
  candidate triggers a `StorableRead`. Watch alt-search latency and storage read
  volume after deploy — a high-population shared fingerprint would show up here
  first.
- **The true-positive direction**: a known cheater on a fresh account with a
  rotated IP, HMD serial and XPID, on the same hardware, should now link.

### Manual verification

1. **Before deploy**, sample the live index for the degenerate string and count
   how many accounts carry it. That number is the blast radius if the guard
   regresses, and it is worth knowing rather than assuming small.
2. Confirm `CommodityProfilePrefixes` in the live config covers every Quest
   variant currently in use, including Quest 3S.
3. After deploy, review newly-formed alt edges for a day. Any edge whose only
   matching item is a system profile deserves a manual look before action is
   taken on it.
4. Confirm a legitimate household — two players, same model of PC, different
   accounts — does **not** link. Different machines with identical specs produce
   identical profile strings; the fingerprint is hardware *class*, not hardware
   *identity*. This is the most likely real-world false positive and it is worth
   a deliberate test.
5. Confirm the true positive: a test account on known hardware, with IP, serial
   and XPID all changed, links to its prior identity.

### Not changed by that PR — still open on #516

Do not test for these; they were deliberately not implemented. First-sight
reject on a banned machine, subnet/ASN reuse as a link signal, and the
Nakama-vs-Discord account-age gate are enforcement policy rather than linking.
`"N/A"` remains in `IgnoredLoginValues` on purpose: a nulled HMD serial is
shared by everyone who nulls it, so as a *linking key* it would mass-link
strangers.

---

# Suggested order

Run in this order — each stage's failures make the next stage's results
uninterpretable.

| # | Stage | Why here |
|---|---|---|
| 1 | `just test`, `just fmt-check`, `just exec-bit-check`, `just bench-check` | Cheap. A red floor invalidates everything below. |
| 2 | `go test -race -count=1 ./server/...` | Catches §6 and §9 regressions before anyone spends a day on manual passes. |
| 3 | §12 build/tooling | You need a trustworthy build to test with. |
| 4 | §8 storage, §10 account, §9 latency | Foundations. A storage defect surfaces as a bug in §1–3 and wastes the investigation. |
| 5 | §5 prune in `report_only` **only** | Read-only, and it tells you whether production data is in the state you assume. |
| 6 | §7 security gates | Independent, high value, cheap to check. |
| 7 | §3 matchmaker, §4 match lifecycle | Need real players; gate the rest of gameplay testing. |
| 8 | §1 early quit, §2 suspensions | The most player-visible. Test with enforcement **off** first, then a single opt-in test guild. |
| 9 | §11 client wire formats | Needs real Quest and PC hardware. |
| 10 | §13 alt detection | Its risk is false positives, which only appear against real login volume. Sample the index **before** deploy (step 1); review edges **after**. |
| 11 | §5 prune with `do*` flags | Destructive. Last, one guild at a time, only after §5 report-only was clean. |

# Known-red before you start

Do not spend time diagnosing these; they are filed.

- `internal/intents` `TestIntent_MarshalText` — red on `main` (**#554**)
- The `server` package aborts in 0.07 s when a database is reachable (**#553**)
- The compose test workflow is `workflow_dispatch`-only, with `pull_request`
  commented out — no test gate runs on PRs at all

# Open risks this plan cannot close

- **No PR-triggered test gate.** Everything above is manual or locally run. Until
  #553 and #554 are fixed and the `pull_request` trigger is restored, "CI is
  green" is not a statement about this codebase.
- **Concurrency tests may be vacuous.** Documented in the campaign handoff: a
  `-race` test here passed against a deliberately unlocked accessor because the
  goroutines never overlapped. There is no lint for it. Treat every green
  `-race` result in §6 and §9 as unproven unless that specific test has been
  seen to fail against a broken version.
- **Prune is tested against a snapshot.** A report-only run that looked correct
  yesterday does not authorise a destructive run today. Re-run report-only
  immediately before any run with `do*` flags set.
