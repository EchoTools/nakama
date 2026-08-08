# Handoff: the enforcement wave, and a gate that states its own scope

Written for the next seat, 2026-08-08, at the end of the third session of this
campaign. Continues
[`handoff-2026-08-08-detection-and-ci-coverage.md`](handoff-2026-08-08-detection-and-ci-coverage.md),
which is still current for everything it covers except its open-issue table.

---

## The one thing to read if you read nothing else

**Nothing mechanical is left on #516 or #553. Everything remaining is an
operator decision, and one of those decisions is a question nobody has answered
in three attempts:**

> Is `KickPlayersWithDisabledAlternates` actually enabled in production?

It matters more than any code in this campaign. It is the switch on the
delayed-kick path — the only enforcement #516 has ever had. If it is **off**,
then the alt-linker has been detecting correctly and doing nothing about it the
entire time, every detection fix in this campaign has landed in front of an
inert path, and the right next move is to turn one setting on rather than to
write anything.

Check it before building anything else on #516.

---

## What this session changed

| PR | | closes |
|---|---|---|
| #555 | arm the tracked git hooks from any `just` recipe | **#557** |
| #559 | correct the intents wire token; widen the test gate 2 → 22 packages | **#554** |
| #561 | stop the EVR module fatal from killing the `server` test binary | refs #553 |
| #562 | optional minimum EchoVR account age | refs #516 |
| #563 | refuse a green test run that covered almost nothing | refs #553 |
| #564 | optional first-sight login reject on a machine-fingerprint match | refs #516 |
| #565 | surface the datacenter classification both providers already fetch | refs #516 |

Filed: **#560** (the vendored `internal/gopher-lua` exclusion, split out of #554
so it was recorded rather than dropped).

---

## Three settings are built, tested, and OFF

None of them changes anything until someone sets it. That is deliberate in all
three cases, and the reasoning is not caution — it is that each is a **trade**
rather than an upgrade, and the trade belongs to whoever runs the service.

| setting | what it does | why it is off |
|---|---|---|
| `MinimumNakamaAccountAgeDays` (per guild) | gates on the EchoVR account's age, which the existing gate cannot see — it reads the Discord snowflake, so a fresh burner on an aged Discord clears it | making Nakama age authoritative rejects **genuinely new EchoVR players with long-standing Discord accounts** — a real population, and not the one it aims at |
| `RejectDisabledAlternatesOnMachineMatch` (service) | refuses the login on an exact machine-fingerprint match to a disabled account, instead of admitting the session and kicking it 1–4 minutes later | the delayed kick buys **ambiguity about which signal caught them**; rejecting at login tells an evader exactly what to change, on the login that carried it |
| — | `ipapiData.IsVPN()` ignoring `Response.Hosting` is **left as-is** | widening it blocks players in every guild with `BlockVPNUsers` on. The current behaviour is now pinned by test, so changing it is a deliberate edit rather than a silent shift |

---

## #516: where it actually stands

| item | state |
|---|---|
| linking on machine fingerprint | **closed** by #551 |
| 1 — first-sight reject | **built** (#564), off |
| 2 — subnet/ASN reuse | **open** — and the blocker is narrower than it was |
| 3 — Nakama account age | **built** (#562), off |
| `"N/A"` in `IgnoredLoginValues` | deliberately unchanged; the prior handoff's reasoning holds |

### Item 1 was smaller than the issue implies — record this so it is not rebuilt

Detection already worked after #551. A specific system profile is a **strong**
signal (`CommodityProfilePrefixes` covers only Quest headsets), so
`filterStrongAlts` does not suppress it and the delayed kick fires. The gap was
**only timing**. #564 adds the first-sight path and leaves the delayed one
intact behind it.

### Item 2's blocker changed, and my first answer on it was wrong

I escalated item 2 saying it might need a new data source. **It never did.** Both
providers already fetch a datacenter classification and, before #565, nothing
read it:

- ip-api `Hosting bool` — and the client's field mask explicitly names `hosting`
- IPQS `ConnectionType` — `"Data Center"` is one of its values

`grep` returned exactly one line for each: the struct field. **Same defect shape
as #516 itself** — captured, stored, never used.

So item 2 is not blocked on data. It is blocked on one question that no amount
of code answers:

> Is a `/24` within a datacenter narrow enough to be a **key** rather than a
> **bucket**?

An ASN plainly is not — it is shared by every customer of that provider. A `/24`
inside a datacenter is much narrower but still shared between tenants. Per the
rule #551 established, *ask what the most common value of the new key is*, and
that needs production data on how many unrelated players share a datacenter
`/24`. Do not implement item 2 by reasoning about it.

---

## #553: fixed, and still open, for two different reasons

**Fixed:** the `server` package died 0.07 s into a run that takes ~130 s whenever
a database was reachable. One unguarded call site — `disableEvrRuntimeModules`
was already applied to the two other test `NewRuntime` calls, both passing a
**nil** database; `runtime_test.go:107` was the third and the only one passing a
real one. That asymmetry is why the failure needed a database to appear and why
`just test` never saw it. Measured: `FAIL 0.072s` → `ok 132.191s`.

**Answered, so it is not reopened by inference:** `InitModule` should **not**
degrade on a missing `DISCORD_BOT_TOKEN`. A missing required credential should
stop startup, and degrading to `dg = nil` needs a nil check at every use of
`dg`, several of which would panic today.

**Still open:**

1. **The EVR `InitModule` is exercised by no test.** #561 stops it killing the
   binary; it does not test it. All three harnesses disable the EVR modules by
   construction. This is now the interesting half of #553.
2. **No test gate runs on pull requests.** `docker-compose-tests.yml` is the only
   thing that would run this suite and its `pull_request` trigger is commented
   out. It *could not have completed* before #561. It can now. Enabling it means
   a DB service and an image build on every PR — a cost call.

---

## The coverage floor, and what it is really for

`just test-audit` is what CI runs now. Same suite, plus a per-package floor on
**passing test count** and a printed count of what skipped.

**The instrument was changed from what #553 asked for, and the reason matters
more than the tool.** #553 asked for a duration check. Duration tracks how fast
the runner is; it would drift on every machine change and say nothing about
coverage. And the failure the issue names was **loud** — the 0.07 s death
reported `FAIL`. What made it invisible was that no workflow ran it.

The hazard worth a gate is the quiet cousin: **a package that skips its way to
empty**. A skipped test is indistinguishable from a passing one at the exit
code. Proven on a run `go test` itself calls a success:

```
go test -run TestParse ./server/evr/   →  ok, exit 0     (14 of 252 tests)
scripts/test-audit.sh -run TestParse   →  exit 1, "14 passing test(s), floor is 190"
```

Floors are at roughly three quarters of current counts. **A
catastrophic-collapse detector, not a drift detector** — it must never fire
because someone deleted a test. If a drop is intentional, change the floor in
the same commit, where it gets reviewed.

### The number the audit exists to keep honest

```
note: 130 test(s) skipped -- green here does not mean these ran.
```

129 of those are in `server`: the DB-backed tests, which CI has no database for.
**That was already true and already invisible.** It is now on every run's log.
This is the same lesson as the previous handoff's unarmed clone — the failure
mode of a silent control is that it looks exactly like a working one.

---

## Method notes that earned their place

### An escalation can be wrong in the direction of "too hard"

I escalated #516 item 2 as needing a data source. Two greps disproved it. The
check that would have caught it earlier is the same one that found the original
bug: **before concluding that data is missing, grep for the field name and read
the request the client actually sends.** ip-api's field mask had `hosting` in it
the whole time.

Escalating is not free. An escalation that overstates the blocker parks work
that could have moved.

### A guard on a rejecting gate has to be proven, not reasoned about

#564 rejects **logins**. A bucket there does not mis-tag someone — it locks out
real players en masse. Both exclusions (the degenerate
`Unknown::::::::0::0::0::0` profile, and commodity headset prefixes) were proven
by removing them and watching the suite go red.

Also pinned: **an unavailable classifier must narrow what an enforcement rule
matches, never widen it.** A nil CGNAT detector skips the commodity check; the
wrong reading — "nothing is commodity" — would turn every Quest profile into a
machine match the moment settings failed to load.

### A check that only sees tracked files cannot see your new file

CI rejected #563 twice for one cause: `scripts/test-audit.sh` was `0755` on disk
and `100644` in the index, because `core.fileMode=false` here and a plain
`chmod` records nothing. `git update-index --chmod=+x` is the operation that
does.

**`just exec-bit-check` passed locally** — because it was run while the file was
still *untracked*, and the check globs tracked files only. That is the same
tracked-only property the justfile already documents for `FMT_FILES`, and it
means the guard has a blind spot for new files, which is exactly when the bit is
most likely to be wrong. CI checks out a commit, where everything is tracked.

Run `git add` **before** the checks, not after.

---

## Operational state

- **Disk:** root was **100% full** (239 MB free of 1.8 TB) mid-session and killed
  a link step. `go clean -cache` freed 117 GB. Currently ~116 GB free. Docker
  still holds ~34 GB reclaimable (14 GB build cache, 19.8 GB unused images) if
  it recurs.
- **Repro environment:** the Postgres container used to reproduce #553 was
  removed. The exact commands to recreate it are on #553.
- **Worktrees** under this session's scratchpad are scratch; every branch in them
  is pushed. **No branch has been deleted** — that was never authorised, and
  worktree removal and branch deletion remain separate operations.
- **#475 is untouched**, as in both prior sessions.
- **Branch protection** and `push.default = simple` remain the repository owner's
  unresolved decisions, exactly as the previous handoff left them.

---

## If you pick this up

In order:

1. **Answer the `KickPlayersWithDisabledAlternates` question.** Everything else
   on #516 is downstream of it.
2. Decide whether to enable the two new gates. Both are built and off.
3. #553's `pull_request` trigger — a cost call, and the suite can now complete.
4. Only then, item 2, and only with production data on datacenter `/24` sharing.

Do not implement item 2 by reasoning about it. That is how a bucket ships.
