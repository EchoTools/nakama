# Party System Inter-PR Contradiction Audit

Generated: 2026-05-27

Base commit: `ba5ee8f12b3b66c347eb2e1081414c4fa2b82be2`
HEAD: `013f42e55` (main, also mid-rebase of `pr-440-rebase-clean`)

---

## 1. Touched-By Matrix

### `server/evr_lobby_find.go` (1376 lines)

| Function / Region | PRs That Modified It |
|---|---|
| `lobbyFind()` (party resolution, follower dispatch) | #392, #408, #410, #422, #431, #435, #436, #438 |
| `shouldFollowerFindOrCreateSocial()` | #436, #438 |
| `configureParty()` (leader wait loop) | #410, #438 |
| `lobbyFindOrCreateSocial()` (social lobby find-or-create) | #408, #438 |
| `isLeaderHeadingToSocial()` | #435, #438 |
| `TryFollowPartyLeader()` | #408, #438 |
| `pollFollowPartyLeader()` | #408, #438, #443, #446 |
| `isFollowerInLeaderMatch()` (closure inside pollFollowPartyLeader) | #408, #443, #446 |
| `newLobby()` / `flushMatchRegistryLabelUpdates()` | #408 |

### `server/evr_lobby_join.go`

| Function / Region | PRs That Modified It |
|---|---|
| `lobbyJoin()` — `LeavePartyStream` removal | #422, #431 |

### `server/evr_lobby_session.go`

| Function / Region | PRs That Modified It |
|---|---|
| `handleLobbySessionRequest()` — `LeavePartyStream` removal | #422, #392 |

### `server/evr_lobby_backfill.go`

| Function / Region | PRs That Modified It |
|---|---|
| Entrant join loop (parallel vs sequential) | #425 |

### `server/evr_lobby_builder.go`

| Function / Region | PRs That Modified It |
|---|---|
| `repairSplitTicketTeams()` | #436 |
| `rankEndpointsByAverageLatency()` | #452 |

### `server/evr_lobby_matchmake.go`

| Function / Region | PRs That Modified It |
|---|---|
| `MinCount` for Combat | #432 |

### `server/evr_matchmaker_prediction.go`

| Function / Region | PRs That Modified It |
|---|---|
| Rating lookup (UserID vs ticket ID) | #432 |

### `server/evr_matchmaker_divisions.go`

| Function / Region | PRs That Modified It |
|---|---|
| `FilterEntriesByDivision()` | #454 |

### `server/evr_match.go` / `evr_match_label.go` / `evr_lobby_joinentrant.go`

| Function / Region | PRs That Modified It |
|---|---|
| `MatchJoinAttempt()` — reservation handling | #450 |
| `LobbyJoinEntrants()` — reservation-violated error path | #450 |
| `LoadAndDeleteReservationRaw()` | #450 |

### `server/pipeline_party.go`

| Function / Region | PRs That Modified It |
|---|---|
| `partyCreate()` — tracker.Track isNew logging | #438 |
| `partyJoin()` — tracker.Track isNew logging | #438 |

### `server/evr_pipeline_party.go`

| Function / Region | PRs That Modified It |
|---|---|
| `snsPartyTrackAndJoin()` — isNew logging | #438 |

### `server/evr_lobby_parameters.go`

| Function / Region | PRs That Modified It |
|---|---|
| `MatchmakingStream()` | refactor branch (c6309d4be), merged alongside #438 |
| `GuildGroupStream()` | refactor branch (c6309d4be) |

### `server/evr_matchmaker_process.go`

| Function / Region | PRs That Modified It |
|---|---|
| Failsafe timeout tracking (`foundTimestamp`) | refactor branch (c6309d4be) |

---

## 2. Contradictions Found

### CRITICAL-1: Unresolved Merge Conflicts in HEAD (4 files)

**Source**: Partially-merged rebase of `party-matchmaking-refactor` branch (commit `c6309d4be`) against HEAD.

**Files with conflict markers**:
- `server/evr_lobby_find.go` lines 117-136, 144-150, 172-185
- `server/evr_lobby_parameters.go` lines 887-909
- `server/evr_matchmaker_process.go` lines 410-413
- `server/pipeline_party.go` lines 57-62, 142-147

**What this means**: The codebase at HEAD **does not compile**. The `<<<<<<< HEAD` / `=======` / `>>>>>>>` markers are present in production source files. This is the single most severe finding.

**Specific contradictions inside the merge conflicts**:

#### CRITICAL-1a: `lobbyFind()` follower dispatch (lines 117-136)

- **HEAD side**: `shouldFollowerFindOrCreateSocial(lobbyParams.Mode) || headingToSocial` -- Uses the `headingToSocial` cache from PR #438 to also redirect followers whose mode is arena but whose leader is heading to social. This prevents the "rubber-banding" bug.
- **Refactor side**: `shouldFollowerFindOrCreateSocial(lobbyParams.Mode)` alone -- Does NOT check `headingToSocial`, dropping the social-bypass from #438. Followers in arena mode whose leader is heading to social would enter the poll loop and get stuck.
- **Winner**: Neither. Both code paths exist separated by conflict markers.

#### CRITICAL-1b: Released follower party size (lines 172-185)

- **HEAD side** (from #438 + post-#438 fix `f6e6e127c`): Conditional `SetPartySize(1)` only for non-social modes; sets `lobbyGroup = nil` to prevent `addTicket` from enforcing leader-only party submission.
- **Refactor side**: Unconditional `SetPartySize(1)`, no `lobbyGroup = nil`.
- **Impact**: The refactor undoes the deliberate fix in `f6e6e127c` ("released followers must queue as solo"). If the refactor side wins, released followers in social modes would get party size 1 (incorrect -- they should keep party size to converge via reservations), and lobbyGroup stays non-nil (incorrect -- released solo followers would fail the leader-only gate in `addTicket`).

#### CRITICAL-1c: `MatchmakingStream()` GroupID semantics (evr_lobby_parameters.go lines 887-909)

- **HEAD side**: `PresenceStream{Mode: StreamModeMatchmaking, Subject: p.GroupID}` -- Uses the player's guild group ID directly as the matchmaking stream subject.
- **Refactor side**: For arena/combat/social public modes, overrides `groupID` to `uuid.Nil`. This puts ALL public matchmakers on a single stream regardless of guild group.
- **Impact**: This changes the matchmaking stream identity. The leader's `isLeaderHeadingToSocial()` lookups, the `monitorMatchmakingStream()` function, and the `TryFollowPartyLeader()` matchmaking-check all use `params.MatchmakingStream()`. If the stream subject is `uuid.Nil` vs the actual GroupID, followers and leaders in the same party but different guild groups would be on different streams -- or conversely, unrelated players would share a stream. This is a fundamental semantic change that both sides handle differently, and neither is clearly correct.

#### CRITICAL-1d: `pipeline_party.go` tracker.Track return handling (lines 57-62, 142-147)

- **HEAD side** (from #438): Captures `isNew` return, logs a warning when the party creator/joiner was already tracked.
- **Refactor side**: Discards `isNew` with `_`, adds a SMELL annotation.
- **Impact**: The conflict does NOT just lose the logging. On the HEAD side, line 67-68 reads `if !isNew { p.logger.Warn(...) }`. On the refactor side, `isNew` is `_`, so this line would fail to compile. With the conflict markers, neither compiles.

#### CRITICAL-1e: `evr_matchmaker_process.go` foundTimestamp tracking (lines 410-413)

- **HEAD side**: Sets `foundTimestamp = true` when a valid timestamp is found. This flag is used at line 419 to detect when no entries had valid timestamps, treating the condition as expired so players are not stuck.
- **Refactor side**: Omits the `foundTimestamp = true` assignment.
- **Impact**: Without the flag, the failsafe check at line 419 (`if !foundTimestamp`) would always evaluate as true (since `foundTimestamp` is never set), treating ALL matchmaking as expired. This could cause premature ticket expiration for every party.

---

### CONTRADICTION-2: `maxNonJoinableCycles` -- PR #446 vs PR #438

**PR #446 intent**: Increase `maxNonJoinableCycles` from 1 to 3 so followers tolerate up to 3 consecutive full-match poll cycles (~18 seconds) before giving up.

**PR #438 intent**: Kept `maxNonJoinableCycles = 1` but restructured the poll loop to add social bypass and priority join.

**What is in HEAD (line 1241)**: `const maxNonJoinableCycles = 1`

**Status**: PR #446 (commit `4160721e3`) set it to 3. But #446 was applied to the branch that became HEAD. Looking at line 1241, the value is **1**, not 3. The post-#443 fix was the last change to touch `pollFollowPartyLeader`. The sequence was: #443 added early convergence (keeping cycles=1), then #446 changed it to 3, then subsequent commits on the same branch did NOT touch it. However, the current value at HEAD is 1 because the `party-matchmaking-refactor` branch (c6309d4be) was rebased ON TOP of these changes and it has `const maxNonJoinableCycles = 1`. The unresolved merge conflict means the PR #438 version (cycles=1) co-exists with the post-#446 code.

**Winner**: PR #438's value of 1 is what appears at the actual `const` declaration in the non-conflicted portion (line 1241). PR #446's fix to 3 was overwritten.

**Impact**: Followers give up after 1 non-joinable cycle (~6 seconds) instead of the intended 3 cycles (~18 seconds). This directly re-introduces the premature release bug that #446 was designed to fix.

---

### CONTRADICTION-3: `isFollowerInLeaderMatch()` Convergence Fallback -- PR #443 vs PR #446

**PR #443 intent**: When `MatchLabelByID` is unavailable (nil NK, transient registry miss), fall back to tracker-based convergence. The logic was:
```go
if p.nk != nil {
    label, err := MatchLabelByID(...)
    if err == nil && label != nil {
        return label.GetPlayerByUserID(...)
    }
}
return true // tracker says same match → trust it
```

**PR #446 intent**: Tighten this to prevent premature success declarations. The logic became:
```go
if p.nk == nil {
    return true // tests only
}
label, err := MatchLabelByID(...)
if err != nil || label == nil {
    return false // keep polling rather than premature success
}
return label.GetPlayerByUserID(...)
```

**What is in HEAD (lines 1222-1231)**: PR #443's version -- the permissive fallback that returns `true` when `MatchLabelByID` fails. PR #446's tightened version was NOT preserved.

**Winner**: PR #443 (permissive).

**Impact**: The premature convergence declarations that #446 was designed to fix (20 per day in production) are still possible. When `MatchLabelByID` transiently fails, the function returns `true` even though the follower may not actually be in the leader's match.

---

### CONTRADICTION-4: Follower Release Strategy -- "Release to Independent Matchmaking" vs "Force to Social"

Three PRs express contradictory philosophies about what happens when a follower cannot join the leader's match:

**PR #408** (Release path): When leader's match is full and mode is non-social, release follower to **independent matchmaking** with `partySize=1`. Follower finds their own pub match.

**PR #438** (Force to social): In `TryFollowPartyLeader`, when leader's match is full and follower is at main menu, **force follower's mode to `ModeSocialPublic`** and return false. The caller (`lobbyFind`) then sees the social mode and calls `lobbyFindOrCreateSocial`.

**What is in HEAD**: Both patterns exist simultaneously:
- Lines 1139-1142 (`TryFollowPartyLeader`): Forces `params.Mode = evr.ModeSocialPublic` before returning false
- Lines 170-181 (`lobbyFind` after poll): Checks `shouldFollowerFindOrCreateSocial(lobbyParams.Mode)` which is now true because TryFollow mutated the mode

**Status**: These are **consistent in effect** but the design is fragile. The `TryFollowPartyLeader` mutates the input parameters as a side-effect, and the caller depends on observing this mutation. If any future PR restructures `lobbyFind()` to not re-check `lobbyParams.Mode` after `TryFollowPartyLeader`, the social redirect silently breaks. The comment in #438 even says "Intentional side-effect" -- acknowledging this is coupling through mutation rather than explicit return values.

---

### CONTRADICTION-5: `LeavePartyStream` Removal -- PR #392 vs #422 vs HEAD

**PR #392** (Apr 15): Removed `LeavePartyStream` from `handleMatchmakingError`. Party stream survives matchmaking failures.

**PR #422** (Apr 25): Removed `LeavePartyStream` from `lobbyJoin()` (for non-arena/combat modes) and from `LobbyJoinSessionRequest` / `LobbyCreateSessionRequest` handlers. Party stream survives lobby transitions.

**What is in HEAD** (`evr_lobby_session.go:58`): A `LeavePartyStream(session)` call remains for **spectators** (`Role == evr.TeamSpectator`). This is correct -- spectators should leave the party.

**What is in HEAD** (`evr_lobby_join.go`): No `LeavePartyStream` calls. PR #422's change stuck.

**Status**: Resolved. The removals are consistent. Spectator path correctly still leaves party.

---

### CONTRADICTION-6: Social Mode Bypass in `pollFollowPartyLeader` -- Contradictory Return Semantics

**PR #438 addition** (line 1350-1353 in HEAD):
```go
if shouldFollowerFindOrCreateSocial(params.Mode) {
    return false
}
```

This code is reached **after** the match is confirmed to have open slots (line 1340 gate passed). The poll found an open, joinable social lobby the leader is in -- then immediately returns `false` without joining.

**Intent**: Force the follower to use `lobbyFindOrCreateSocial` instead of joining via the poll path. The idea is that social lobbies use reservations and the find-or-create path handles them better.

**Contradiction**: The poll loop has already confirmed the leader's match is open and has room. Returning `false` at this point means the follower takes the fallback path in `lobbyFind()`, which may release them to independent matchmaking (with `partySize=1` and `lobbyGroup=nil` per the HEAD+conflict region). The follower could end up in a **different** social lobby than the leader.

This contradicts the entire purpose of `pollFollowPartyLeader`, which is to get the follower into the leader's specific match. The social mode check after the openSlots gate creates a path where the poll succeeds in finding a joinable match but deliberately avoids joining it.

---

### CONTRADICTION-7: `isLeaderHeadingToSocial` -- Check Order Reversal Between #435 and #438

**PR #435 original order**:
1. Check if leader is in a social lobby (match check first)
2. Check if leader is matchmaking for social (matchmaking check second)

**PR #438 reversal**:
1. Check if leader is matchmaking (matchmaking intent first)
2. Check if leader is in a social lobby (match check second)

**PR #438 added a critical early return**: If the leader IS matchmaking but for a **non-social** mode (e.g., Arena), return `false` immediately without checking the match. This means if a leader is simultaneously in a social lobby AND matchmaking for arena, #435 would return `true` (leader is in social) but #438 returns `false` (leader's matchmaking intent is non-social).

**Winner**: PR #438's version is in HEAD.

**Impact**: This is an improvement -- matchmaking intent is more current than match location. However, if any code downstream assumes `isLeaderHeadingToSocial` returns true when the leader is in a social lobby, that assumption is now wrong during the arena-matchmaking-from-social-lobby transition.

---

### CONTRADICTION-8: Leader Matchmaking Stream Check in `pollFollowPartyLeader` -- Removed by #438

**Pre-#438** (added by #408): Inside the poll loop, the follower checked if the leader was still matchmaking:
```go
if pr := session.pipeline.tracker.GetLocalBySessionIDStreamUserID(
    leaderSessionID, params.MatchmakingStream(), leaderUserID); pr != nil {
    continue
}
```
If the leader was still matchmaking, the follower would skip the rest of the loop iteration and wait.

**PR #438**: Removed this check entirely.

**Impact**: Without this check, the follower in the poll loop will read the leader's service stream, find their **old** match (not the one the matchmaker is about to place them in), and attempt to join it. This is the TOCTOU race annotated at line 1040 with `SMELL(critical)`. PR #438 moved the equivalent check to `TryFollowPartyLeader` (line 1041) but not into `pollFollowPartyLeader`, creating an asymmetry: the initial follow attempt checks, but the retry poll loop does not.

---

## 3. Overall Assessment

### The codebase does not compile.

Four files contain unresolved merge conflict markers from a partially-applied rebase of `party-matchmaking-refactor` (c6309d4be). This alone makes the current HEAD unshippable.

### Design direction is pulling in two directions.

Two distinct strategies compete:

1. **Convergence through polling** (PRs #408, #443, #446): Followers poll the leader's service stream, wait for them to settle, detect convergence via tracker + match label, and join directly. This requires retries, grace periods, and convergence checks.

2. **Convergence through social lobby find-or-create** (PRs #431, #435, #438): Followers bypass polling entirely for social modes, using reservations and `lobbyFindOrCreateSocial` to naturally land in the same lobby. The leader's match is found via priority join inside `lobbyFindOrCreateSocial`.

These are not contradictory in theory -- polling handles arena/combat, find-or-create handles social. But in practice:
- PR #438 forces followers to social mode even when they were in arena (via `TryFollowPartyLeader` mutation), blurring the boundary
- The `shouldFollowerFindOrCreateSocial` check inside `pollFollowPartyLeader` (line 1350) creates a dead path where the poll finds a joinable match but refuses to join it
- The merge conflict at lines 117-136 shows the two strategies literally fighting over the same code region

### Three specific regressions are live in HEAD:

1. `maxNonJoinableCycles = 1` (PR #446's fix to 3 was overwritten)
2. `isFollowerInLeaderMatch` uses permissive fallback (PR #446's tightening was overwritten by #443's version persisting)
3. `foundTimestamp` tracking missing in `evr_matchmaker_process.go` (conflict marker omits the assignment)

### Recommendations:

1. **Resolve the merge conflicts immediately.** The `party-matchmaking-refactor` rebase must be completed or abandoned. Every decision deferred in those conflict markers is a production bug.
2. **Pick one convergence strategy per mode and enforce it.** Document: "Arena/Combat followers use poll-based convergence. Social followers use find-or-create with reservations." Remove the social-mode bypass from `pollFollowPartyLeader` (line 1350) -- if the poll path is never supposed to handle social, don't enter it for social.
3. **Restore PR #446's two specific fixes:** `maxNonJoinableCycles = 3` and the tightened `isFollowerInLeaderMatch` that returns `false` (not `true`) when `MatchLabelByID` fails.
4. **Replace the side-effect mutation pattern.** `TryFollowPartyLeader` should return a typed result (`FollowResult{Joined, FallbackToSocial, FallbackToMatchmaking, Failed}`) instead of mutating `params.Mode` and returning a bare bool.
5. **Add the matchmaking-stream check back to `pollFollowPartyLeader`.** The TOCTOU race annotated at line 1040 also exists in the poll loop, but the mitigation was only applied to `TryFollowPartyLeader`.
