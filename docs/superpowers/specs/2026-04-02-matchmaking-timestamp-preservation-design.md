# Matchmaking Timestamp Preservation

## Problem

When a player re-queues for matchmaking (after a crash, cancel, or party follower failure), `MatchmakingTimestamp` resets to `time.Now()`. This has three compounding effects:

1. **Lost priority** — `submission_time` in the matchmaker ticket resets, putting them at the back of the queue.
2. **SBMM range reset** — `calculateExpandedRatingRange` uses time-since-submission to widen the acceptable skill range. A fresh timestamp snaps the range back to narrow, making it harder to find matches for high-rated players.
3. **Backfill age filter reset** — Backfill queries filter matches relative to `MatchmakingTimestamp`, so reset players may exclude matches they previously would have accepted.

Observed impact: A player with rating_mu=37.56 (top ~5%) in a 3-person party queued for 269 seconds, crashed, and on re-queue got a fresh timestamp — losing all accumulated SBMM range expansion. Their perceived wait was ~8 minutes across multiple re-queues.

## Design

### Matchmaking Credit

A **matchmaking credit** is a record that a user was recently matchmaking for a specific mode. It preserves the original submission timestamp so re-queues don't restart the clock.

```go
type matchmakingCredit struct {
    Mode      evr.Symbol
    Timestamp time.Time
    Expiry    time.Time
}
```

### Storage

Process-global `sync.Map` keyed by user ID string → `*matchmakingCredit`.

Not persisted to Nakama storage. The server process outlives individual sessions, so the map entry survives crashes (websocket reconnect) and manual cancel+re-queue. Full server restarts clear it, which is acceptable — nobody has accumulated meaningful queue time through a restart.

### Lifecycle

**Creation**: When `lobbySessionRequest` builds `LobbySessionParameters` via `NewLobbyParametersFromRequest`, after setting `MatchmakingTimestamp`:

- Check the credit map for this user ID.
- If a credit exists, is not expired, and matches the requested mode, use the credit's timestamp instead of `time.Now()`.
- If no valid credit exists, store a new credit with the current timestamp.

**Expiry**: Set to `now + MatchmakingTimeout + 2 minutes` buffer. This covers the matchmaking window plus crash recovery time. Checked lazily on read.

**Deletion**: When a player successfully joins a match (the `Joined entrant` path in `evr_lobby_joinentrant.go`), delete their credit. This ensures the next queue after a completed match starts fresh.

**Mode change**: If a player switches modes (e.g. echo_arena → social_2.0), the mode won't match the credit, so they get a fresh timestamp. This is correct — mode changes are intentional re-queues.

### Files to Modify

1. **`server/evr_lobby_parameters.go`**
   - Add the `matchmakingCredit` type and package-level `sync.Map`.
   - Add unexported functions: `getMatchmakingCredit(userID string, mode evr.Symbol) *matchmakingCredit`, `setMatchmakingCredit(userID string, credit *matchmakingCredit)`, `clearMatchmakingCredit(userID string)`. All callers are in the `server` package.
   - In `NewLobbyParametersFromRequest`, after line 403 where `MatchmakingTimestamp: time.Now().UTC()` is set: check for existing credit and override if valid.

2. **`server/evr_lobby_joinentrant.go`**
   - In `LobbyJoinEntrants` (the function-level one at ~line 48), after the successful join path completes, loop over all `entrants` and call `clearMatchmakingCredit(e.UserID.String())` for each. Note: only `entrants[0]` is processed through the full join flow to the `Joined entrant` log at ~line 280; `entrants[1:]` are passed as reservation metadata. The credit clear must cover all entrants, not just the primary.

### What Changes for the Player

- Re-queuing after a cancel or crash preserves their original submission time in the matchmaker.
- SBMM rating range continues expanding from the original queue start, not from re-queue time.
- Backfill age filters remain consistent with the original queue time.
- No behavioral change for first-time queues or mode changes.

### What Doesn't Change

- The matchmaker's own ticket ordering and matching logic is untouched.
- `priority_threshold` continues to work as before (it's computed from the ticket timeout, not submission_time).
- Party mechanics, follower polling, and reconnect reservations are unaffected.

### Edge Cases

- **Party size changes between queues**: The timestamp is preserved but the party size in the ticket changes. This is fine — the matchmaker matches on current party composition, and the preserved timestamp just maintains priority/SBMM expansion.
- **Player queues, waits 5 minutes, plays a full match, queues again**: The credit is cleared on match join, so the second queue starts fresh. Correct behavior.
- **Server restart during matchmaking**: Credits are lost. Player gets a fresh timestamp. Acceptable — server restarts are rare and queue times are short relative to restart frequency.
- **Multi-node**: Credits are per-process. Players are sticky to a node for their session, so this works. If a player somehow lands on a different node after crash, they lose the credit — acceptable edge case.

### Guardrails

1. **Expiry is mandatory** — Every credit must have a finite expiry. No code path may create a credit without one. The expiry formula is `now + MatchmakingTimeout + 2min` (the timeout comes from `MatchmakingTimeoutSecs` in global settings, default 360s/6min, so default expiry is ~8min). This prevents unbounded queue-time inflation where a player who queued hours ago gets treated as having waited hours.

2. **Mode must match exactly** — A credit for `echo_arena` is not valid for `social_2.0` or `echo_combat`. Mode mismatch → fresh timestamp. This prevents cross-mode queue time leaking (e.g., sitting in a social lobby counting as arena queue time).

3. **Clear on match join, not on match start** — The credit is cleared when the entrant is confirmed joined (`Joined entrant` log path), not when the match is merely found. This handles the case where a match is found but join fails (server full race condition) — the player keeps their credit for the retry.

4. **No credit for social lobbies** — Social lobby finds (`ModeSocialPublic`) complete in 1-2 seconds and don't use the matchmaker. Credits should only be created for modes that enter the matchmaker: `ModeArenaPublic`, `ModeCombatPublic`, `ModeArenaPublicAI`. Check `lobbyParams.Mode` before creating a credit.

5. **Credit timestamp must not be in the future** — On read, if the stored timestamp is after `time.Now()` (clock skew, NTP jump), discard the credit and use a fresh timestamp.

6. **No credit stacking** — Writing a new credit for the same user overwrites the previous one. There is no accumulation across modes.

### Methodology

**Implementation order**: Tests first, then implementation, then verify.

1. Write `server/evr_matchmaking_credit_test.go` with all unit tests (map operations, expiry, mode filtering).
2. Implement `matchmakingCredit` type and map functions in `server/evr_lobby_parameters.go`.
3. Run unit tests, verify pass.
4. Wire into `NewLobbyParametersFromRequest` (credit read/create) and `LobbyJoinEntrants` (credit clear).
5. Verify `MatchmakingTimestamp` preservation through the `NewLobbyParametersFromRequest` wiring — as an integration test if session/NakamaModule test helpers exist in the codebase, otherwise via manual code inspection of the wiring.
6. Run full matchmaker test suite (`go test ./server/ -run TestMatchmak -count=1`).

**Verification**: After implementation, grep all consumers of `MatchmakingTimestamp` and `submission_time` to confirm none assume the timestamp is always "recent" (within seconds of now). Known consumers and their expected behavior with a preserved timestamp:

- `calculateExpandedRatingRange` (line 419) — Correctly produces a wider range. Desired.
- `BackfillSearchQuery` (line 450) — `minStartTime` shifts earlier, allowing older matches. Acceptable; the player was already waiting that long.
- `MatchmakingParameters` (line 577) — `submission_time` string property reflects original time. Desired.
- Fallback failsafe reduction (line 230 in `evr_lobby_matchmake.go`) — `elapsed` reflects total wait time, so `remaining` failsafe is smaller. This means the failsafe kicks in sooner for re-queued players. Correct — they've been waiting longer.
- `priority_threshold` (line 280 in `evr_lobby_matchmake.go`) — Computed from `time.Now()` + ticket timeout fraction, independent of submission_time. Unaffected.

### Tests

All tests go in `server/evr_matchmaking_credit_test.go`.

#### Unit Tests

**`TestMatchmakingCredit_StoreAndRetrieve`**

- Call `setMatchmakingCredit("user-1", credit{Mode: echo_arena, Timestamp: t0, Expiry: t0+8m})`.
- Call `getMatchmakingCredit("user-1", echo_arena)`.
- Assert returned credit has `Timestamp == t0`.

**`TestMatchmakingCredit_ExpiredCredit`**

- Store a credit with `Expiry` in the past.
- Call `getMatchmakingCredit` for the same user/mode.
- Assert returns nil (expired credits are not returned).
- Assert the expired entry is removed from the map (lazy cleanup).

**`TestMatchmakingCredit_ModeMismatch`**

- Store a credit for `echo_arena`.
- Call `getMatchmakingCredit` with `social_2.0`.
- Assert returns nil (mode mismatch).
- Assert the original `echo_arena` credit is still in the map (not deleted by mismatch).

**`TestMatchmakingCredit_Clear`**

- Store a credit for `echo_arena`.
- Call `clearMatchmakingCredit("user-1")`.
- Call `getMatchmakingCredit("user-1", echo_arena)`.
- Assert returns nil.

**`TestMatchmakingCredit_Overwrite`**

- Store credit with `Timestamp: t0`.
- Store credit with `Timestamp: t1` for the same user (different or same mode).
- Assert `getMatchmakingCredit` returns `t1`, not `t0`.

**`TestMatchmakingCredit_FutureTimestamp`**

- Store credit with `Timestamp` 5 minutes in the future.
- Call `getMatchmakingCredit`.
- Assert returns nil (future timestamps are rejected).

**`TestMatchmakingCredit_DifferentUsers`**

- Store credits for `user-1` and `user-2` with different timestamps.
- Assert each user gets their own credit back, not the other's.

#### Integration Tests

Note: `NewLobbyParametersFromRequest` requires a `*sessionWS`, `runtime.NakamaModule`, and an `evr.LobbySessionRequest` — non-trivial to construct. These tests should exercise the credit logic at the `get`/`set`/`clear` function level, verifying that the wiring in `NewLobbyParametersFromRequest` calls them correctly. If existing test helpers for session/NakamaModule construction exist in the codebase, use them; otherwise keep these as unit tests on the credit functions with a separate manual verification that the wiring is correct.

**`TestMatchmakingCredit_PreservedOnRequeue`**

- Call `setMatchmakingCredit("user-1", credit{Mode: echo_arena, Timestamp: t0, Expiry: t0+8m})`.
- Call `getMatchmakingCredit("user-1", echo_arena)`.
- Assert returned timestamp is `t0` (preserved, not reset to a later time).

**`TestMatchmakingCredit_ClearedAfterJoin`**

- Call `setMatchmakingCredit("user-1", credit{Mode: echo_arena, Timestamp: t0, Expiry: t0+8m})`.
- Call `clearMatchmakingCredit("user-1")`.
- Call `getMatchmakingCredit("user-1", echo_arena)`.
- Assert returns nil.

**`TestMatchmakingCredit_FreshOnModeChange`**

- Call `setMatchmakingCredit("user-1", credit{Mode: echo_arena, Timestamp: t0, Expiry: t0+8m})`.
- Call `getMatchmakingCredit("user-1", combat_public)`.
- Assert returns nil (mode mismatch, so caller should create fresh timestamp).

**`TestMatchmakingCredit_NotCreatedForSocialMode`**

- Verify at the call site: when `lobbyParams.Mode` is `ModeSocialPublic`, the credit creation code path is skipped. This is a code review verification, not an automated test — the guardrail is enforced by a mode check before `setMatchmakingCredit`.
