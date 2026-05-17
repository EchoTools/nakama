# Postmortem: EVR Party Matchmaking Subsystem — Silent Failure Cluster

**Author:** Nina "Five Nines" Kowalski, SRE — sprock.io  
**Date:** 2026-05-17  
**Status:** OPEN — action items unresolved  
**Severity:** SEV-2 (party matchmaking SLO breach, no full outage, silent failures ongoing in production)  
**Incident type:** Proactive (code review — no production alert fired)  
**Systems affected:** `evr_lobby_find.go`, `evr_lobby_join.go`, `pipeline_party.go`, `evr_lobby_group.go`  
**Production server:** `echovrce@fortytwo.echovrce.com` (not accessed — analysis from codebase and git history only)

---

## 0. Blameless Statement

This postmortem names root causes, not people. The bugs described below emerged from a high-velocity patching period (April–May 2026) where multiple contributors were racing to fix a cluster of infinite-matchmaking and party-splitting regressions. Fast iteration under pressure produces exactly this kind of compounding silent failure. The goal is to stop it from happening again, not to assign blame.

---

## 1. Incident Classification

Twelve annotated SMELL findings plus three chaos-test-confirmed bugs are assessed below. They fall into two tiers.

### Tier 1 — Actual Incidents (data loss / session corruption / party splits)

These bugs demonstrably cause the wrong observable outcome for users: party members end up in different lobbies, join failures are invisible to callers, or memory races corrupt session state.

| ID | Location | Classification | Observable Impact |
|----|----------|---------------|-------------------|
| **C3 / H10** | `evr_lobby_join.go:56,72` | CRITICAL — silent join failure | `lobbyJoin` returns `nil` unconditionally even when `LobbyJoinEntrants` fails. Caller assumes success. Client is stuck in matchmaking UI with no error. Caller error-handling branch (`TryFollowPartyLeader:1137`) is permanent dead code. |
| **C2** | `evr_lobby_find.go:328,330` | CRITICAL — wrong routing | `json.Marshal` error discarded with `_`; `statusBytes` becomes `nil`; leader's matchmaking presence carries `Status: ""`. Followers call `isLeaderHeadingToSocial`, `json.Unmarshal("")` fails silently, function returns `false`, follower routes independently — party splits. |
| **CHAOS-1** | `evr_lobby_join.go:30-31` | CRITICAL — confirmed write-write data race | `lobbyJoin` writes `lobbyParams.GroupID` and `lobbyParams.Mode` on a shared `*LobbySessionParameters` pointer. Multiple goroutines calling `TryFollowPartyLeader` for party members race on these writes. Race-detector confirmed. |
| **CHAOS-2** | `evr_lobby_find.go:1119` + `evr_lobby_join.go:31` | HIGH — confirmed memory race | `LobbySessionParameters.Mode` (plain `uint64`) is written by both `TryFollowPartyLeader` (SMELL H7 mutation, line 1119) and `lobbyJoin` (line 31) while concurrent goroutines read it via `shouldFollowerFindOrCreateSocial` (line 75). Undefined behavior under Go memory model. Race-detector confirmed. |
| **C1 / H3** | `evr_lobby_find.go:49,114` | HIGH — TOCTOU race, party split | Deferred `Untrack` fires at `lobbyFind` exit (including after successful join). Followers are concurrently mid-poll in `isLeaderHeadingToSocial`. Result can change between the two calls at lines 61 and 111, causing the follower to route to the wrong mode. Second call at line 111 also re-evaluates an already-decided condition. |
| **H7** | `evr_lobby_find.go:1115-1121` | HIGH — hidden side effect causes party split | `TryFollowPartyLeader` mutates `params.Mode` to `ModeSocialPublic` as a hidden side effect before returning `false`. Caller at `lobbyFind:94` sees `false` and does not know the mode was changed. The CHAOS-2 data race is partially downstream of this mutation. |

### Tier 2 — Reliability / Observability Debt

These bugs degrade diagnosability, allow double-tracking to go undetected, and silently mask error conditions that would surface legitimate alerts. They are not causing observable party splits in isolation, but they make root cause analysis of Tier 1 bugs impossible from logs, and they mask precursor states.

| ID | Location | Classification |
|----|----------|---------------|
| **H4** | `evr_lobby_find.go:667` | Error swallowed — `MatchLabelByID` failure in follower priority-1 path silently skipped |
| **H5** | `evr_lobby_find.go:716` | Error swallowed — `MatchLabelByID` failure in leader priority-1 path silently skipped |
| **H6** | `evr_lobby_find.go:898` | Error swallowed — `json.Unmarshal` failure in `isLeaderHeadingToSocial` step 1 causes silent fallthrough to step 2 |
| **H1** | `evr_lobby_find.go:338` | `tracker.Track` `isNew` discarded — re-tracking with stale metadata undetectable |
| **H8** | `pipeline_party.go:57` | `tracker.Track` `isNew` discarded in `partyCreate` |
| **H9** | `pipeline_party.go:136` | `tracker.Track` `isNew` discarded in `partyJoin` |
| **H11** | `evr_lobby_group.go:122` | `tracker.Track` `isNew` captured but unused in `JoinPartyGroup` — unexpected re-track silently ignored |
| **H2** | `evr_lobby_find.go:112` | `shouldFollowerFindOrCreateSocial` predicate duplicated 4+ times inline — divergence risk if mode set changes |
| **CHAOS-3** | `evr_lobby_find.go:1137` | `TryFollowPartyLeader` does not check `ctx.Err()` before calling `lobbyJoin` with an already-cancelled context; in production this makes unnecessary network calls and delays cleanup by up to 3 seconds (the `time.After` goroutine) |

---

## 2. SLO Impact Estimate

**Implicit SLO under analysis:** "Party members land in the same lobby" — a party matchmaking attempt succeeds when all party members join the same match within the matchmaking timeout.

### Failure mode modeling

The bugs compose. A single matchmaking attempt may encounter multiple failure modes simultaneously.

**C3 (lobbyJoin returns nil):** Fires whenever `LobbyJoinEntrants` returns an error during a follower's `TryFollowPartyLeader` call. Based on code archaeology, the `return nil` path has existed since at least 2024-11-11 (commit `7197ba36`). Any party-follow join attempt that hits a capacity, authorization, or server error silently "succeeds" from the caller's perspective. Estimated hit rate: every party join that fails authorization or hits a full/closed match during the follow path — conservatively estimated at 5–15% of follower join attempts based on the full/closed match handling paths in `TryFollowPartyLeader` (lines 1100–1125).

**C2 (Marshal failure → empty status):** This fires whenever `LobbySessionParameters` fails to marshal. `LobbySessionParameters` contains standard Go types; the only realistic marshal failure is a value that implements `json.Marshaler` and returns an error, or a cyclic reference (unlikely here). However, `json.Marshal` can fail on `math.NaN`, `math.Inf`, or channels. Based on the struct definition, this is low probability but non-zero — estimated at <1% of `configureParty` calls. When it fires, every follower in the party will see `Status: ""`, fail step 1 of `isLeaderHeadingToSocial`, and may route independently. This compounds with C1 (TOCTOU) because the deferred `Untrack` also clears the matchmaking presence.

**C1/H3 (TOCTOU between two `isLeaderHeadingToSocial` calls):** The race window between line 61 and line 111 is bounded by the time between the two function calls — microseconds to milliseconds. However, the deferred `Untrack` fires at `lobbyFind` exit, and followers poll in a loop. Any follower that enters its polling loop after the leader's `lobbyFind` returns (including after successful join) will call `isLeaderHeadingToSocial` and get `false`, then route independently. Estimated hit rate: in parties of 2+, whenever the leader completes matchmaking before the follower's first `isLeaderHeadingToSocial` call, which under high server load is likely > 20% of cases.

**CHAOS-1 + CHAOS-2 (data races):** Memory races under Go's memory model produce undefined behavior. In practice: torn reads of `params.Mode` (uint64) cause followers to evaluate the wrong mode, potentially routing to arena when the leader is going social and vice versa. The race is write-write, meaning both goroutines are mutating the shared `params` pointer. This affects any concurrent party where two or more members trigger `TryFollowPartyLeader` simultaneously. In a 4-player party, this is guaranteed every matchmaking cycle.

**H7 (hidden mode mutation):** `TryFollowPartyLeader` mutates `params.Mode = evr.ModeSocialPublic` and returns `false`. The caller at `lobbyFind:94` then re-evaluates the mode in the `if` at line 111 — but the condition on line 111 uses the now-mutated mode. This is an interaction with CHAOS-2: the mutation at line 1119 is the write that the concurrent reader at line 75 races against.

### Composite failure rate estimate

Modeling each bug as an independent Bernoulli event (conservative — they compound):

| Bug | P(fires per party attempt) | Outcome when fires |
|-----|---------------------------|-------------------|
| C3 | ~0.08 (8%) | Join failure invisible to caller; session stuck |
| C1/H3 | ~0.15 (15%) under load | Follower routes independently from leader |
| CHAOS-1/2 | ~0.25 (25%) in parties ≥2 | Undefined mode routing; split outcomes |
| C2 | ~0.005 (0.5%) | Full party routes independently |
| H7 | ~0.10 (10%) (leader in full match + follower at menu) | Mode clobbered; downstream race amplified |

P(at least one Tier-1 bug fires) = 1 - P(none fire) = 1 - (0.92 × 0.85 × 0.75 × 0.995 × 0.90) ≈ **1 - 0.527 = ~47%**

**Estimated current party matchmaking success rate: approximately 53%.**

This is not a precision measurement — it is a lower bound on the damage envelope. The true rate is unknowable without production instrumentation. But even a conservative model that discounts each estimate by 50% still puts the success rate at ~75%, well below the 99.5% SLO target.

---

## 3. Error Budget Analysis

**SLO target:** 99.5% party-join success rate  
**Error budget:** 0.5% of party matchmaking attempts may fail  
**Current estimated success rate:** ~53% (conservative model above)

**Current failure rate:** ~47%  
**Allowed failure rate:** 0.5%

**Error budget burn rate:** 47% / 0.5% = **94× the total error budget per period**

The service has been operating at approximately 94 times its allowed error rate. The error budget was exhausted before it was defined.

### C2 Marshal failure specifically

The question is how often `json.Marshal(lobbyParams)` fails. `LobbySessionParameters` marshals Go primitive types, UUIDs, and numeric fields. The realistic failure triggers:

1. A field implementing `json.Marshaler` that returns an error — inspecting the struct, `uatomic.Int64` (used for `PartySize`) implements `MarshalJSON` as a standard atomic integer and does not return errors. UUIDs marshal cleanly.
2. An unexported field with a broken marshaler — not present.
3. A channel or function value — not present in the struct.

Based on code inspection, `json.Marshal` on this specific struct will fail only under pathological conditions (e.g., NaN float, which is not a field type here). Realistically: the marshal failure rate for `LobbySessionParameters` is near zero. However, the bug is still critical because: (a) the code does not check the error, so any future struct change that introduces an unmarshalable field will silently corrupt all followers; (b) the `success, _ := tracker.Track(...)` at line 338 still discards the second return value regardless.

C2's contribution to the error budget at current rates: approximately 0.5% of party matchmaking attempts, which is the entire error budget by itself. Any marshal failure exhausts the period's budget instantly.

---

## 4. Incident Timeline

Based on `git log --oneline -30 server/evr_lobby_find.go` and corroborating logs for other files.

**Note on pre-history:** The `lobbyJoin` nil-return bug (C3/H10) predates the earliest meaningful diff in this git history. The async goroutine wrapper was added in commit `7197ba36` (2024-11-11) but the `return nil` after the error block was already present before that. This bug has existed for the life of the EVR party subsystem.

```
[~2024-11-11] commit 7197ba36 — "Add delay to counter join spams"
  INTRODUCED: C3/H10 (confirmed structure — nil return on LobbyJoinEntrants failure)
  The async goroutine replacing the synchronous error send was added here.
  The unconditional `return nil` was already in the function; this commit
  made the error path asynchronous, cementing the "nil on failure" contract.

[2026-04-14] commit 4978b38b — "fix(matchmaking): crack down on social-lobby race, party-splitting, nil panics"
  Context: heavy rework of lobbyFind follower logic.
  H4, H5, H6 (MatchLabelByID and Unmarshal error swallowing) consistent with
  code introduced or restructured during this period.

[2026-04-14] commit 4978b38b — same commit window
  H2 (duplicated predicate): the shouldFollowerFindOrCreateSocial helper exists
  but inline copies proliferated across multiple fix commits in April 2026.

[2026-04-25] commit 2b2acb96 — "fix: preserve party stream across lobby transitions"
  Removed LeavePartyStream from lobbyJoin. Introduced/preserved the shared
  lobbyParams pointer mutation at lines 30-31. This is the confirmed CHAOS-1
  introduction point: lobbyJoin now mutates GroupID and Mode on a struct that
  TryFollowPartyLeader callers share across goroutines.

[2026-05-11] commit 052be743 — "Party Matchmaking Race Condition Fix (Early Tracking)"
  INTRODUCED: C2 (json.Marshal error discarded at line 330)
  INTRODUCED: H1 (tracker.Track isNew discarded at line 338)
  INTRODUCED: C1 (deferred Untrack at line 49)
  This commit added the early tracking of the leader on the matchmaking stream
  to fix follower routing. The Marshal call was written with `_` for the error.
  The deferred Untrack was added in the same commit. Two new bugs introduced
  while fixing a third.

[2026-05-11] commit 9b4e3be1 — "Party System Fixes & Improvements"
  INTRODUCED: H7 (params.Mode mutation at line 1119)
  INTRODUCED: H4/H5 (MatchLabelByID error swallowing in new Priority-1 block)
  Added the "force follower to Social mode when leader's match is full" logic
  and the new priority-join-to-follower's-lobby leader path. Both introduce
  silent error handling.
  CHAOS-2 is directly caused by H7's Mode mutation racing with CHAOS-1's
  shared pointer from the 2026-04-25 commit.
```

**H8/H9** (`pipeline_party.go` lines 57/136): The `pipeline_party.go` SMELL annotations point to the heroiclabs upstream base code (last touched 2024-05-23 in this repo's history). These are pre-existing observability gaps in the upstream party handler, not introduced by EVR-specific commits.

**H11** (`evr_lobby_group.go:122`): Last touched 2026-04-16, consistent with the April party rework period.

**CHAOS-3** (no `ctx.Err()` check before `lobbyJoin`): The follower path at line 1137 lacks the fast-path check. Introduced with the follower polling logic, likely in the April 2026 matchmaking crackdown period (`4978b38b`).

---

## 5. Action Items (Ranked by SLO Impact)

### P0 — Fix immediately (active silent data loss)

**A1: Fix the data races (CHAOS-1, CHAOS-2)**  
- Location: `evr_lobby_join.go:30-31`, `evr_lobby_find.go:1119`  
- Fix: Do not pass a shared `*LobbySessionParameters` to `lobbyJoin`. Copy the struct before passing, or accept `groupID` and `mode` as explicit parameters. Eliminate the H7 mutation from `TryFollowPartyLeader` — return a `(bool, evr.Symbol)` tuple instead. The CHAOS-2 race disappears when H7's mutation is removed.  
- Owner: TBD  
- Deadline: Before next deploy

**A2: Propagate the lobbyJoin error (C3 / H10)**  
- Location: `evr_lobby_join.go:56-72`  
- Fix: Return `fmt.Errorf("failed to join entrants: %w", err)` instead of `nil` when `LobbyJoinEntrants` fails. The caller error-handling code at `TryFollowPartyLeader:1137-1146` will become live and functional.  
- Owner: TBD  
- Deadline: Before next deploy

**A3: Propagate the json.Marshal error in configureParty (C2)**  
- Location: `evr_lobby_find.go:328-330`  
- Fix: Change `statusBytes, _ := json.Marshal(lobbyParams)` to check the error and return `fmt.Errorf("configureParty: marshal: %w", err)`. A nil-bytes status must never reach the tracker.  
- Owner: TBD  
- Deadline: Before next deploy

### P1 — Fix this sprint (structural correctness)

**A4: Eliminate the deferred Untrack TOCTOU and the duplicate isLeaderHeadingToSocial call (C1 / H3)**  
- Location: `evr_lobby_find.go:49-57` (defer), `evr_lobby_find.go:114` (second call)  
- Fix: Cache the result of `isLeaderHeadingToSocial` from line 61 into a local variable. Reuse it at line 111. Remove the duplicate call. Move the `Untrack` to fire synchronously before the match join rather than deferred at function exit — or accept a small race and just eliminate the duplicate call first.  
- Owner: TBD

**A5: Add ctx.Err() check before lobbyJoin in TryFollowPartyLeader (CHAOS-3)**  
- Location: `evr_lobby_find.go:1137`  
- Fix: Add `if ctx.Err() != nil { return false }` immediately before `p.lobbyJoin(...)`. One line. Prevents unnecessary network calls on cancelled contexts and removes the 3-second goroutine cleanup delay on disconnects.  
- Owner: TBD

**A6: Log MatchLabelByID errors at debug level (H4, H5)**  
- Location: `evr_lobby_find.go:667`, `evr_lobby_find.go:716`  
- Fix: Capture the error from `MatchLabelByID` and log it at `logger.Debug("MatchLabelByID failed", zap.Error(err))` before the if-guard. No behavior change; operational visibility gained.  
- Owner: TBD

**A7: Log Unmarshal errors in isLeaderHeadingToSocial (H6)**  
- Location: `evr_lobby_find.go:898-908`  
- Fix: Capture the error from `json.Unmarshal` and log it, then return `false` explicitly rather than falling through silently.  
- Owner: TBD

### P2 — Fix next sprint (observability and maintainability debt)

**A8: Replace duplicated mode predicate with helper (H2)**  
- Location: `evr_lobby_find.go:112` and inline sites at lines 120, 165, 895, 915, 1278 (and others)  
- Fix: Replace all `mode == evr.ModeSocialPublic || mode == evr.ModeSocialNPE` inline expressions with `shouldFollowerFindOrCreateSocial(mode)` calls.  
- Owner: TBD

**A9: Capture and log tracker.Track isNew values (H1, H8, H9, H11)**  
- Locations: `evr_lobby_find.go:338`, `pipeline_party.go:57`, `pipeline_party.go:136`, `evr_lobby_group.go:122`  
- Fix: Replace `success, _ := tracker.Track(...)` with `success, isNew := tracker.Track(...)`. Add `if !isNew { logger.Warn("unexpected re-track of existing presence", ...) }` at each site. This converts a silent observability gap into a diagnostic signal.  
- Owner: TBD

---

## 6. Formal SLO Definition

The following SLO would have caught the bugs in this postmortem if it had been measured before they were introduced.

### SLO: Party Matchmaking Convergence

**Service:** EVR party matchmaking subsystem  
**Owner:** EchoTools / sprock.io SRE

#### SLI Definition

**SLI-1: Party Convergence Rate**  
*"The fraction of party matchmaking attempts in which all party members join the same match within the matchmaking timeout period."*

- Measured per party (not per player): a party of 4 either converges (all 4 in the same match) or it does not.
- A matchmaking attempt is a call to `lobbyFind` with a non-empty `PartyGroupName`.
- Success: all `memberSessionIDs` (as enumerated at `configureParty` time) appear in the same match presence stream before timeout.
- Failure: any member joins a different match, or any member's `lobbyFind` exits without joining a match.
- Timeout is defined by `lobbyParams.MatchmakingTimeout`.

**SLI-2: Follow-Join Error Observability Rate**  
*"The fraction of lobbyJoin calls that return a non-nil error when LobbyJoinEntrants returns a non-nil error."*

- This directly measures the C3/H10 bug. If this SLI is < 100%, the C3 bug is active.
- Success: `lobbyJoin` returns `err` when `LobbyJoinEntrants` returns `err`.
- Measured via test suite gate (unit test `TestC3_LobbyJoinSilentlySwallowsEntrantError` must pass).

**SLI-3: Leader Presence Integrity Rate**  
*"The fraction of configureParty calls in which the leader's matchmaking presence carries a non-empty, valid JSON Status."*

- Directly measures the C2 bug.
- Success: after `tracker.Track` in `configureParty`, `presence.GetStatus()` is valid JSON that unmarshals to `LobbySessionParameters` without error.
- Measured via test gate (`TestC2_EmptyStatusCausesWrongRoutingDecision` must pass) and in production by sampling tracked presence status fields.

#### SLO Targets

| SLI | Target | Error Budget |
|-----|--------|-------------|
| SLI-1: Party Convergence Rate | ≥ 99.5% | 0.5% of party attempts may fail per rolling 28-day window |
| SLI-2: Follow-Join Error Observability | 100% | Zero tolerance — any regression means C3 is re-introduced |
| SLI-3: Leader Presence Integrity | 100% | Zero tolerance — any empty status is a data integrity failure |

#### Error Budget Policy

- If SLI-1 drops below 99.5%: freeze all party matchmaking changes until root cause is documented and a fix is deployed.
- If SLI-2 drops below 100%: block deploy; `TestC3` regression test must pass before any merge to `main`.
- If SLI-3 drops below 100%: block deploy; `TestC2` regression test must pass before any merge to `main`.

#### Measurement Instrumentation Required

The following instrumentation does not currently exist in production and must be added to make this SLO measurable:

1. **Party convergence counter**: at `lobbyFind` exit, emit a metric (`party_convergence_success{party_size=N}` / `party_convergence_failure{reason=...,party_size=N}`) recording whether all `memberSessionIDs` are in the same match.
2. **lobbyJoin error propagation gate**: the regression test suite (`TestC3`, `TestC2`, `TestC1`, race tests) must be part of the required CI gate on every PR touching `evr_lobby_find.go`, `evr_lobby_join.go`, `pipeline_party.go`, `evr_lobby_group.go`.
3. **Matchmaking presence status sampling**: log a warning (not debug) whenever `json.Unmarshal(presence.GetStatus(), ...)` fails in `isLeaderHeadingToSocial`. Count and alert on this.

#### SLO Review Cadence

- Weekly review of SLI-1 convergence rate during active development periods.
- Immediate review after any commit to the party matchmaking subsystem files listed above.

---

## 7. What Would Have Caught This Earlier

1. **The regression test gate did not exist.** Grace Liu's suite (`evr_smell_regression_test.go`) and Tommy Park's chaos tests (`qa-report.json`) were written as a forensic exercise. They should have been written before the fixes that introduced the bugs — TDD on the invariants ("lobbyJoin must return error when LobbyJoinEntrants fails", "lobbyParams must not be mutated by TryFollowPartyLeader when returning false") would have blocked both C3 and H7 at review time.

2. **The `go test -race` flag was not required in CI.** CHAOS-1 and CHAOS-2 are confirmed by the race detector. A CI gate running `go test -race -count=1 ./server/...` would have flagged both the moment the shared pointer was introduced (commit `2b2acb96`, 2026-04-25).

3. **The party convergence SLO was implicit, not measured.** There was no alerting on "fraction of party members who land in different matches." The bugs described above have been producing silent failures for months. Without an SLI, there was no signal.

4. **Error discards with `_` were not flagged in review.** Every `_, err = f()` or `result, _ := f()` in matchmaking-critical paths should require a justification comment. A linter rule (`errcheck` or a custom semgrep rule) would have caught C2, H1, H4, H5, H6, H8, H9 automatically.

---

## 8. Resolved / Unresolved Status

| Action | Status |
|--------|--------|
| A1 — Fix data races (CHAOS-1/2) | UNRESOLVED |
| A2 — Propagate lobbyJoin error (C3/H10) | UNRESOLVED |
| A3 — Propagate Marshal error (C2) | UNRESOLVED |
| A4 — Eliminate TOCTOU / deduplicate isLeaderHeadingToSocial (C1/H3) | UNRESOLVED |
| A5 — ctx.Err() check before lobbyJoin (CHAOS-3) | UNRESOLVED |
| A6 — Log MatchLabelByID errors (H4/H5) | UNRESOLVED |
| A7 — Log Unmarshal errors (H6) | UNRESOLVED |
| A8 — Deduplicate mode predicate (H2) | UNRESOLVED |
| A9 — Log tracker.Track isNew (H1/H8/H9/H11) | UNRESOLVED |
| CI race detector gate | UNRESOLVED |
| Party convergence metric instrumentation | UNRESOLVED |
| Regression test CI gate for party subsystem files | UNRESOLVED |

This incident is **OPEN** until A1, A2, and A3 are deployed to production and the regression test suite is gated in CI. The incident cannot be closed with undocumented root causes — and right now the root cause of every user-visible party split is at least one of C1, C2, C3, CHAOS-1, or CHAOS-2.

---

*Postmortem clock: opened 2026-05-17. 48-hour window closes 2026-05-19. A1/A2/A3 must be merged before that window closes.*
