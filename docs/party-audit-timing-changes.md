# Party System Timing & Retry Heuristic Audit

Base commit: `ba5ee8f12b3b66c347eb2e1081414c4fa2b82be2`
HEAD commit: `ec976cc18` (refactor: party matchmaking overhaul and conflict resolution)

## Timing Changes

| Parameter | Before | After | Commit(s) | Rationale | Assessment |
|-----------|--------|-------|-----------|-----------|------------|
| `maxNonJoinableCycles` | 1 | 1 (was 3 briefly on side branch) | `1d6c04679` introduced at 1; `4160721e3` raised to 3; `c6309d4be`/`ec976cc18` refactor reverted to 1 | Side branch rationale: give followers ~18s (3 x 6s cycles) instead of ~6s. Revert rationale: refactor replaced polling with social-mode bypass. | **BAND-AID** -- the value is back to 1, meaning a follower whose leader is in a full non-social match gives up after a single ~6s cycle. The side-branch fix (3 cycles, ~18s) was correct for the symptom but was discarded during refactoring without replacing it with a structural solution. The new social-mode bypass helps social-mode followers but non-social followers still get only ~6s. |
| `MatchmakingStartGracePeriod` | 3s (existed at base) | 3s (unchanged, but newly *used* in `configureParty`) | `03a249078` (fix: wait for party followers before fresh-start ticket) | Prevents leader from submitting solo matchmaking ticket before follower joins the party handler. | **ARBITRARY** -- 3s was defined at base but never used in `configureParty`. The commit wired it in but never justified why 3s is correct. If the follower's `JoinPartyGroup` call takes 4s (network lag, slow auth), the leader proceeds solo. No adaptive retry, no telemetry to validate the budget. |
| Grace period poll interval | N/A (not wired) | 200ms | `03a249078` | Poll `lobbyGroup.Size()` every 200ms during the 3s grace window. | **JUSTIFIED** -- 200ms is fast enough to detect a follower joining within a single frame, and the timer is bounded by the 3s cap. Low cost. |
| `lobbyFindOrCreateSocial` first-attempt wait | 1s (always slept before first query) | 0s (immediate first attempt) | `4978b38bd` (fix: crack down on social-lobby race) | Old 1s wait was workaround for Bluge label-flush lag. `newLobby` now calls `FlushPendingLabelUpdates` synchronously, so first query sees fresh state. | **JUSTIFIED** -- root cause (stale index) was fixed structurally. Removing the artificial 1s wait allows concurrent joiners to converge on the first lobby instead of each creating their own. |
| `lobbyFindOrCreateSocial` post-create wait | 1s (`<-time.After(1 * time.Second)`) | 0s (removed) | `4978b38bd` | Same rationale: synchronous flush eliminates the need to wait for the Bluge ticker. | **JUSTIFIED** -- structural fix backs this up. The flush is synchronous and the lobby is searchable before `LobbyJoinEntrants` is called. |
| `lobbyFindOrCreateSocial` retry interval | 1s initial, 2x backoff, 8s max, 30 attempts | Same constants, same backoff | N/A (unchanged) | N/A | Unchanged |
| `pollFollowPartyLeader` poll interval | 3s | 3s | Unchanged | N/A | **BAND-AID** -- 3s is hardcoded with no backoff or jitter. SMELL annotation added at line 1240 acknowledges this. With many party followers polling simultaneously, this creates predictable thundering-herd load on the tracker. |
| `pollFollowPartyLeader` settle wait | 3s | 3s | Unchanged | N/A | **BAND-AID** -- same hardcoded value. The "settle" concept assumes the leader needs exactly 3s to be queryable in a match after placement. No evidence this is the right number; too short and the label fetch fails, too long and the follower wastes time. |
| `pollFollowPartyLeader` convergence checks | 4 calls to `isFollowerInLeaderMatch` | 7 calls to `isFollowerInLeaderMatch` | `6f7fcb163` (fix: resolve Duo Desync with tracker-based convergence fallback) | Check before poll loop, after poll interval, after settle wait, and on context cancellation. Catches matchmaker-placed convergence earlier. | **JUSTIFIED** -- adding early-out checks at three more points reduces unnecessary poll cycles. The convergence check is cheap (tracker lookup). |
| `pollFollowPartyLeader` "leader still matchmaking" check | Present (skip cycle if leader has matchmaking presence) | **Removed** | `d9d0bec63` / refactor | Removed as part of simplifying the poll loop. The check caused cycles to be skipped silently when the leader was still in the matchmaker queue, hiding the true state. | **INSUFFICIENT** -- removing the check means the poll loop now proceeds to fetch the leader's service stream and match label even when the leader has no match yet. This adds unnecessary MatchLabelByID calls per cycle. The old check had its own problems (stale stream), but removing it entirely traded one failure mode for wasted work. |
| `TryFollowPartyLeader` 5s retry on full/locked | Not present | Added in `845d9199b`, then **removed** in refactor `c6309d4be`/`ec976cc18` | `845d9199b` (Full ChangeLog) added; refactor removed | Added: "One retry after a brief wait -- the match may have just freed a slot or finished locking." Removed: refactor reverted to base behavior (delegate to `pollFollowPartyLeader`). | **BAND-AID** -- the 5s was arbitrary (why not 3s or 8s?). It was added and removed within the same development cycle, suggesting the team was unsure whether it helped. The refactor's approach (fall through to pollFollowPartyLeader) is structurally better but still relies on the 3+3s poll cycle. |
| `pollFollowPartyLeader` 5s retry on join failure | 5s (existed at base) | 5s (unchanged) | Unchanged | When `lobbyJoin` returns `ServerIsFull` or `ServerIsLocked`, wait 5s and retry the whole poll cycle. | **BAND-AID** -- 5s is hardcoded with no backoff. SMELL annotation added at line 1363 acknowledges this. If the match is genuinely full, 5s of waiting changes nothing; if it's a transient lock, 5s is unnecessarily long. |
| `configureParty` member-wait deadline | 30s | 30s | Unchanged | Wait for party members to start matchmaking after leaving a match. | Unchanged |
| `configureParty` member-wait poll interval | 500ms | 500ms | Unchanged | Poll `lobbyGroup.Size()` every 500ms during the 30s deadline. | Unchanged |
| `monitorMatchmakingStream` checkInterval | 1s | 1s | Unchanged | Poll interval for matchmaking stream presence. | Unchanged |
| `monitorMatchmakingStream` gracePeriod | 1s | 1s | Unchanged | Grace period before canceling matchmaking when stream disappears. | Unchanged |
| `ReservationLifetime` | 30s | 30s | Unchanged | Lobby reservation TTL. | Unchanged |
| `MadeMatchBackfillDelay` | 15s | 15s | Unchanged | Delay before backfill after a made match. | Unchanged |
| `LatencyCacheRefreshInterval` | 3h | 3h | Unchanged | N/A | Unchanged |
| `LatencyCacheExpiry` | 72h | 72h | Unchanged | N/A | Unchanged |
| Matchmaker expired-ticket cleanup | Removed only from `activeIndexes` | Full cleanup: delete from `indexes`, `revCache`, `sessionTickets`, `partyTickets`, Bluge index; only if `Intervals >= MaxIntervals` | `87bc4b850` (fix: address QA findings) and refactor | Base just removed from `activeIndexes`, leaving zombie entries in the index and maps. New code properly cleans up expired tickets. | **JUSTIFIED** -- the old code leaked matchmaker state. Tickets that expired but were never matched stayed in memory and in the Bluge index, growing monotonically. The `MaxIntervals` guard correctly preserves tickets that just moved to passive (MinCount == MaxCount). |
| Combat `MinCount` | 4 (then changed to 2 pre-base) | 2 | `53f6c94a4` (fix: matchmaker MinCount 4->2 for Combat) | Allow combat matches to start with fewer players. | Value was already 2 at base commit. The commit predates the diff range but is in the ancestry. No net change in this audit window. |
| `isUndersizedMatch` timestamp handling | Direct float64 type assertion | Multi-type switch (float64, int64, int) with missing-timestamp guard | `87bc4b850` (fix: address QA findings) | Properties from the matchmaker can arrive as different numeric types depending on the codec path. Missing timestamps now treated as "cannot compute elapsed" rather than using `now` as default (which would always be zero elapsed, making every match appear expired). | **JUSTIFIED** -- the old code silently used `now` as the oldest timestamp when no entries had a timestamp property, which meant `now - now = 0 < failsafeTimeout` was always true, blocking undersized matches from ever forming. The fix is correct. |

## Timing Values That Exist But Were Never Changed

These constants are load-bearing but no commit in this range touched them:

- **3s poll + 3s settle in `pollFollowPartyLeader`**: Each poll cycle is ~6s minimum. With `maxNonJoinableCycles = 1`, a follower gives up after ~6s when the leader's non-social match is full.
- **30s `ReservationLifetime`**: Reservations in social lobbies expire after 30s. If a follower takes longer than 30s to find and join the leader's lobby, the reservation is gone.
- **15s `MadeMatchBackfillDelay`**: After a matchmaker-made match is created, backfill is suppressed for 15s to let the original players join.

## Analysis

### Are these timing changes solving the problem or just moving the failure window?

**Mixed verdict: structural fixes are sound, but the poll loop timing is still a band-aid.**

The good:
1. The `FlushPendingLabelUpdates` change (`4978b38bd`) is a genuine structural fix. The old 1s sleep-and-pray approach to Bluge index staleness was the root cause of duplicate social lobbies. Replacing it with a synchronous flush eliminates an entire class of race conditions.
2. The `MatchmakingStartGracePeriod` usage (`03a249078`) addresses the real race where the leader submits a solo ticket before the follower joins. The 200ms poll inside a 3s window is reasonable.
3. Expired ticket cleanup in the matchmaker (`87bc4b850`) fixes a genuine memory/state leak.

The bad:
1. **`maxNonJoinableCycles` is back to 1 and nobody addressed why.** The side branch correctly identified that 1 cycle (~6s) is not enough time for a follower to retry when the leader's arena/combat match is transiently full (e.g., the match just started and hasn't opened a slot via player departure). The fix was raised to 3 (~18s) with tests, then the refactor silently reverted it. The refactor's social-mode bypass handles social followers, but **non-social followers still get exactly one ~6s cycle** before being released to independent matchmaking.

2. **The 3s+3s poll/settle cadence in `pollFollowPartyLeader` has zero empirical basis.** The poll interval (3s) was presumably chosen to be "long enough to not spam the tracker" and the settle wait (3s) was presumably chosen to be "long enough for the match handler to register the leader." Neither has telemetry, adaptive behavior, or documented justification. These values have survived every refactor unchanged, which suggests nobody knows the right number and nobody wants to find out.

3. **The 5s retry in the poll loop's join-failure path is pure cargo cult.** It was already there at base, nobody touched it, and the SMELL annotation added in the refactor openly calls it out as having no backoff or jitter. If 5 concurrent followers all hit this path, they all retry at exactly t+5s, creating a predictable thundering herd.

4. **`MatchmakingStartGracePeriod = 3s` remains unjustified.** The comment says "the party handler may have just been created and followers may not have called JoinPartyGroup yet." But the grace period was defined long before the code that uses it. The 3s value appears to be a guess. Too short and the leader goes solo (defeating the party system). Too long and solo players wait for a follower that will never come. There is no telemetry to validate that 3s is sufficient for the P95 follower join time.

### Bottom line

The timing changes fall into two categories:
- **Structural fixes** (flush, cleanup, convergence checks): These solve real problems and their timing values are either bounded by external mechanisms or do not matter because the fix eliminates the race.
- **Heuristic adjustments** (maxNonJoinableCycles, poll intervals, grace periods): These are moving the failure window. The follower used to fail after infinite time (loop forever); then after ~6s; briefly after ~18s; and now after ~6s again. The underlying problem -- that `pollFollowPartyLeader` has no mechanism to *know* when the leader's match will have room -- is unaddressed. It polls, guesses, and gives up on a budget that nobody has validated.

The correct fix for the poll-based heuristics is to replace polling with event-driven convergence: the leader's match handler should notify followers when a slot opens, rather than followers polling every 3s to check. Until that architectural change happens, every timing constant in `pollFollowPartyLeader` is a guess wearing the skin of a constant.
