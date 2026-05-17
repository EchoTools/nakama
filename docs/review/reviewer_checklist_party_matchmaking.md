# Reviewer checklist: Party / Matchmaking / Lobby-Find PRs

This checklist enforces `artifacts/adr/2026-05-14-party-matchmaking-replay-first-process-gate.md`.

## Gate 1 — Scope classification

- [ ] PR correctly declares whether it touches party/matchmaking/lobby-find/follower paths.

## Gate 2 — Replay-first evidence (mandatory if in scope)

- [ ] A scenario exists and is linked.
- [ ] Scenario is derived from production logs (or equivalent incident evidence).
- [ ] Baseline run is red (fails deterministically).
- [ ] PR run is green (passes deterministically).

## Gate 3 — Scenario catalog non-regression

- [ ] Scenario catalog was run.
- [ ] No scenario regressions.

## Gate 4 — Logging sufficiency

- [ ] Mutation sites emit structured logs sufficient for replay event reconstruction.
- [ ] If logs were insufficient, this PR adds the missing instrumentation.

## Gate 5 — No-theory acceptance

- [ ] Acceptance criteria are executable (scenario-based), not observational.
