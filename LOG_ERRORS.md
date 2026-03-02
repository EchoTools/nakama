# Log Error Investigation Notes

Date: 2026-02-26

## Scope

- Requested file: `errors-247.log`
- Found/analyzed file: `errors-v248.log`
- Investigation only; no code changes applied.

## Important Context

- `errors-247.log` was not found in this repo or nearby scanned paths.
- Analysis below is based on `errors-v248.log`.

## Added Concerns (Validated)

### 1) MatchHistory objects clogging SystemUser storage

Confirmed. `MatchHistory` objects are stored under nil/system user (`uuid.Nil`):

- Write: `server/evr_match_label.go:584` (`UserID: uuid.Nil.String()`)
- Read: `server/evr_match_label.go:600`
- Delete: `server/evr_match_label.go:619`

Writers and cleanup flow:

- Writes on shutdown/terminate: `server/evr_match.go:1166`, `server/evr_match.go:1223`
- Fallback read in post-match processing: `server/evr_runtime_event_remotelogset.go:538`
- Delete only after post-match processing: `server/evr_runtime_event_remotelogset.go:790`

Risk note:

- If post-match processing fails or is delayed, cleanup can lag and objects accumulate under system-owned storage.

### 2) TOT tests should store with their own user

Current behavior is global storage (empty `UserID`), not per-user.

- List: `server/evr_runtime_rpc_tot.go:106` (`StorageList(..., "", "", "tot_tests", ...)`)
- Create: `server/evr_runtime_rpc_tot.go:166` (`UserID: ""`)
- Update: `server/evr_runtime_rpc_tot.go:214` (`UserID: ""`)
- Delete: `server/evr_runtime_rpc_tot.go:258` (`UserID: ""`)
- Upload: `server/evr_runtime_rpc_tot.go:308` (`UserID: ""`)

## Log Signatures and Triage

## High-Signal / Actionable to investigate further

1. Unknown/unhandled event type `*server.EventMatchSummary`
   - `server/evr_runtime_events.go:194`
   - `server/evr_runtime_events.go:223`
   - Impact: Event pipeline not recognizing a produced event type; can break expected post-event behavior.

2. Post-match social lobby allocation failure
   - `server/evr_match.go:1127` (log)
   - Root error from `server/evr_match.go:1744`: `service settings not initialized or query addon not configured`
   - Impact: private-match to social transition can fail.

3. Leaderboard record error: `leaderboard not found`
   - `server/evr_runtime_rpc_leaderboard.go:92`
   - Upstream error constant exists: `server/core_leaderboard.go:42`
   - Impact: calls against non-existent/mismatched leaderboard IDs.

4. Prometheus metric registration mismatch
   - `server/metrics.go:146`
   - Logged text indicates duplicate metric name with different labels/help.
   - External reference summary: this is descriptor mismatch (commonly duplicate init or schema drift between registrations).

## Likely expected / operational noise (monitor volume)

1. Expired console token
   - `server/console_authenticate.go:280`, `server/console_authenticate.go:284`
   - Typical session expiry behavior.

2. Discord rate limit warnings
   - `server/evr_discord_appbot.go:158`
   - Expected under high API activity; investigate only if sustained and user-impacting.

3. Remote disconnect due to timeout
   - `server/evr_runtime_event_remotelogset.go:147`
   - Often network/client conditions.

4. Early quit history missing on first completion path
   - `server/evr_earlyquit_detailed.go:303`
   - Code explicitly proceeds with new history; often benign bootstrap path.

## Config/Policy-driven warnings

- `Intercepted a disabled resource` (including `WriteStorageObjects`) is emitted by runtime/API gate logic:
  - `server/api_storage.go:167`
  - `server/pipeline.go:167`
- This is commonly expected when resources are intentionally disabled by hook/config policy.

## External Guidance (Storage Ownership)

Nakama docs indicate system-owned storage is acceptable for truly global objects, but user-scoped/high-cardinality data under system user can create listing/index pressure as it grows.

- Storage permissions/docs: Heroic Labs storage permissions/search docs (reviewed during investigation)
- Practical implication here:
  - `MatchHistory` and `tot_tests` currently use shared/global ownership semantics.
  - If cardinality grows significantly, operational overhead tends to concentrate in those shared partitions.
