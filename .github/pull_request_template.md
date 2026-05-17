# Summary

<!-- What changed? Keep it concrete. -->

## Why

<!-- What bug/incident/scenario does this address? -->

## Scope

- [ ] Party
- [ ] Matchmaking
- [ ] Lobby-find / backfill
- [ ] Follower/leader convergence
- [ ] Other (describe):

## Replay-first gate (required for party/matchmaking/lobby-find scope)

If you checked any box in **Scope**, this section is mandatory.

### Scenario link

- Scenario name:
- Location (path/command):
- Source evidence (prod log bundle / incident id):

### Red → Green evidence

- Baseline (before) result:
- With this PR result:

### Non-regression

- Scenario catalog run (command) and result:

## Observability

- Logs added/changed (mutation sites):
- Metrics added/changed:

## Risk

- Failure mode if wrong:
- Rollback plan:

## Checklist

- [ ] I did not use “deploy and see” as the acceptance criterion.
- [ ] If this is a bug fix, there is a deterministic replay scenario derived from prod logs.
- [ ] The scenario fails on baseline (red) and passes with this PR (green).
- [ ] No existing scenario regressed.
