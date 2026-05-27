# Party System Guard Clause Audit

Base commit: `ba5ee8f12b3b66c347eb2e1081414c4fa2b82be2`
Audit date: 2026-05-27
Scope: Every new early return, mode check, short-circuit, or nil guard added to the party/matchmaking path since the base commit.

---

## Guard Clause Inventory

### GC-01: Social mode follower bypass (shouldFollowerFindOrCreateSocial)

- **Location**: `server/evr_lobby_find.go:26-28` (function def), invoked at lines 97, 127, 171, 920
- **Condition**: `mode == evr.ModeSocialPublic || mode == evr.ModeSocialNPE`
- **What it bypasses**: When true for a non-leader follower, skips the pollFollowPartyLeader polling loop entirely (line 97-104). The follower goes straight to lobbyFindOrCreateSocial instead of waiting for the leader to settle.
- **Commit**: `d9d0bec63` fix(party): resolve infinite matchmaking and silent failure cascade (#438)
- **Assessment**: **[CORRECT]** — This is the primary fix for the infinite matchmaking bug. Social lobbies use find-or-create with party reservations, so polling for the leader to settle is unnecessary and was the root cause of the timeout/infinite-loop. The follower will naturally converge to the leader's lobby via the reservation mechanism.

### GC-02: Leader heading to social — early mode override

- **Location**: `server/evr_lobby_find.go:49-53`
- **Condition**: `!isLeader && p.isLeaderHeadingToSocial(ctx, logger, session, lobbyParams, lobbyGroup)`
- **What it bypasses**: Forces `lobbyParams.Mode = evr.ModeSocialPublic` and `Level = LevelUnspecified` BEFORE authorization and the main follow/matchmake decision tree. This means the follower never enters the arena/combat matchmaking path even if their own request was for arena.
- **Commit**: `d9d0bec63` fix(party): resolve infinite matchmaking and silent failure cascade (#438)
- **Assessment**: **[CORRECT] [CREATES-NEW-PATH]** — Prevents the follower from submitting an arena ticket while the leader is heading to social (which would desync them). However, this creates a new path: if isLeaderHeadingToSocial reads stale tracker state (leader just left social), the follower could be incorrectly forced to social. The TOCTOU window is acknowledged in comments but not mitigated.

### GC-03: isLeaderHeadingToSocial — matchmaking intent check

- **Location**: `server/evr_lobby_find.go:899-946` (full function), key guards at lines 901, 914-925, 728-735
- **Condition**: Multi-step: (a) leader == nil or self -> false; (b) check matchmaking stream for leader's params; (c) check match service stream for leader's current match label
- **What it bypasses**: Returns true to force the follower into social mode. Returns false if leader is matchmaking for a non-social mode (line 922-924), which is an explicit short-circuit that prevents social redirect when leader is queueing for arena.
- **Commit**: `d9d0bec63` fix(party): resolve infinite matchmaking and silent failure cascade (#438)
- **Assessment**: **[CORRECT] [NEEDS-TEST]** — The two-phase check (matchmaking intent > current match) is sound. The short-circuit at line 922-924 ("leader is matchmaking for arena, return false") is critical: without it, a leader briefly passing through social en route to arena would drag followers to social. No unit test covers the case where the matchmaking stream has malformed JSON (line 916-919 falls through silently).

### GC-04: Leader early matchmaking stream tracking + deferred untrack

- **Location**: `server/evr_lobby_find.go:335-355` (early Track in configureParty), deferred Untrack not present in current HEAD (was in earlier revisions of d9d0bec63 but appears removed/moved)
- **Condition**: `if isLeader` — always tracks leader on matchmaking stream immediately after party join
- **What it bypasses**: Before this change, followers had no way to detect the leader's matchmaking intent until the leader actually submitted a ticket. Now followers can read the leader's LobbySessionParameters from the matchmaking stream presence to determine if the leader is heading to social or arena.
- **Commit**: `052be7432` Party Matchmaking Race Condition Fix (Early Tracking)
- **Assessment**: **[CORRECT] [CREATES-NEW-PATH]** — Solves the race where follower starts matchmaking before leader's intent is visible. Creates a new path: if configureParty fails after the Track but before returning, the matchmaking presence is leaked. The Track failure is logged but not fatal (line 350-351), which is the right choice for a non-critical hint.

### GC-05: Fresh-start grace period for party followers

- **Location**: `server/evr_lobby_find.go:395-421`
- **Condition**: `lobbyParams.CurrentMatchID.IsNil() && lobbyGroup.Size() <= 1 && lobbyParams.Mode != evr.ModeSocialPublic`
- **What it bypasses**: Delays the leader's matchmaking ticket submission by up to MatchmakingStartGracePeriod (3s), polling every 200ms for a follower to join the party. Without this, the leader submits a solo ticket and gets backfilled into a full match before the follower even connects.
- **Commit**: `03a249078` fix(matchmaker): wait for party followers before fresh-start ticket
- **Assessment**: **[CORRECT] [RISKY]** — Correctly prevents the party-split-on-fresh-start scenario. The risk is the 3s hardcoded timeout: if the follower takes >3s (e.g., slow connection, ping validation delay), the leader proceeds solo anyway. No adaptive retry. The `!= evr.ModeSocialPublic` exclusion is correct (social uses find-or-create, not matchmaker tickets).

### GC-06: Released follower queues as solo (lobbyGroup = nil)

- **Location**: `server/evr_lobby_find.go:127` (SetPartySize(1)), also the removed `lobbyGroup = nil` that was in the diff but does NOT appear in current HEAD at this location
- **Condition**: Follower cannot join leader after poll timeout, in non-social mode
- **What it bypasses**: Sets party size to 1 so the follower submits a solo matchmaker ticket instead of a party ticket. In the PR #438 diff, this also set `lobbyGroup = nil` to prevent addTicket from enforcing leader-only party submission.
- **Commit**: `f6e6e127c` fix(party): released followers must queue as solo
- **Assessment**: **[CORRECT]** — Without this, the released follower would submit a party-sized ticket that the matchmaker rejects (non-leader can't submit party tickets). The `lobbyGroup = nil` in f6e6e127c was the critical fix. Current HEAD at line 127 still has `SetPartySize(1)` but the `lobbyGroup = nil` appears to have been refactored out — the current code path at line 127 does NOT nil out lobbyGroup. **This may be a regression if the code path at line 127 is still reached for non-social modes.**

### GC-07: ModeSocialPrivate blocked in party follow path (KC-1)

- **Location**: `server/evr_lobby_find.go:1149-1157` (TryFollowPartyLeader), `server/evr_lobby_find.go:1183-1200` (pollFollowPartyLeader)
- **Condition**: `switch label.Mode { case evr.ModeSocialPublic, evr.ModeCombatPublic, evr.ModeArenaPublic: ... default: return false }`
- **What it bypasses**: Removes ModeSocialPrivate from the list of joinable modes in the party follow path. If the leader is in a private social lobby, the follower falls through to normal matchmaking instead of auto-joining. This is a security gate: private lobbies require explicit invitation.
- **Commit**: `c1c1347cb` fix(security): block ModeSocialPrivate in party follow path (KC-1)
- **Assessment**: **[CORRECT]** — Critical security fix. Private lobbies must not be joinable via party follow. Note: current HEAD at line 950 still shows `case evr.ModeSocialPrivate, evr.ModeSocialPublic, ...` in TryFollowPartyLeader. **Wait** — re-reading line 1149 shows ModeSocialPrivate IS excluded from TryFollowPartyLeader. However, at line 950 (which was the OLD line numbering from an earlier read) — let me verify. The actual TryFollowPartyLeader switch at current HEAD line 1149-1157 correctly excludes ModeSocialPrivate. The pollFollowPartyLeader switch at line 1183-1199 STILL INCLUDES ModeSocialPrivate (`case evr.ModeSocialPrivate, evr.ModeSocialPublic, ...`). **This is an inconsistency: TryFollowPartyLeader blocks SocialPrivate but pollFollowPartyLeader allows it.**

### GC-08: Leader's match full — force follower to Social mode

- **Location**: `server/evr_lobby_find.go:1134-1142`
- **Condition**: `params.CurrentMatchID.IsNil()` (follower is at main menu) AND leader's match is full
- **What it bypasses**: Mutates `params.Mode = evr.ModeSocialPublic` and `params.Level = evr.LevelUnspecified` as a side-effect, then returns false. The caller (lobbyFind) sees the updated mode and routes the follower to lobbyFindOrCreateSocial instead of leaving them stuck.
- **Commit**: `4804177df` fix(matchmaker): release party follower to matchmaking when leader's match is full (#408)
- **Assessment**: **[CORRECT] [CREATES-NEW-PATH]** — Solves the "follower stuck at loading screen" bug. The side-effect mutation of params is architecturally questionable (function returns false but secretly changes the caller's state), but it works because lobbyFind checks shouldFollowerFindOrCreateSocial after TryFollowPartyLeader returns false.

### GC-09: Stale match guard in isFollowerInLeaderMatch

- **Location**: `server/evr_lobby_find.go:1013-1015`
- **Condition**: `!params.CurrentMatchID.IsNil() && leaderMatchID == params.CurrentMatchID`
- **What it bypasses**: Returns false when the leader's "current match" in the tracker is the same match the follower was in when they started lobby find. This prevents false positives from stale service streams during matchmaking timeout.
- **Commit**: `8c9cb219f` fix: prevent party followers from being falsely marked as placed
- **Assessment**: **[CORRECT]** — Without this, a matchmaking timeout produces a false positive (both players still tracked in the old social lobby), causing pollFollowPartyLeader to declare success and leaving the follower stuck in transition.

### GC-10: MatchLabelByID verification in isFollowerInLeaderMatch

- **Location**: `server/evr_lobby_find.go:1030-1037`
- **Condition**: After confirming tracker convergence (follower and leader in same match via stream), additionally verifies `label.GetPlayerByUserID(session.userID.String()) != nil`
- **What it bypasses**: Returns false if the follower appears in the tracker stream but NOT in the actual match player list. This catches the race where the tracker converges before the client completes the join.
- **Commit**: `6f7fcb163` fix(party): resolve Duo Desync with tracker-based convergence fallback
- **Assessment**: **[CORRECT]** — Tightens the convergence check. If MatchLabelByID fails (err or nil), returns false (pessimistic). This is safe — the poll loop will retry.

### GC-11: Non-joinable cycle limit in pollFollowPartyLeader

- **Location**: `server/evr_lobby_find.go:1044-1045, 1168-1178`
- **Condition**: `nonJoinableCycles >= maxNonJoinableCycles` (maxNonJoinableCycles=1 at HEAD)
- **What it bypasses**: After 1 cycle of the leader being in a non-social, non-joinable match (arena/combat that is full or closed), returns false so the follower can be released. Social lobbies continue polling (they may open up as players leave).
- **Commit**: `d9d0bec63` fix(party): resolve infinite matchmaking and silent failure cascade (#438)
- **Assessment**: **[RISKY]** — maxNonJoinableCycles=1 is very aggressive. A transient "full" state on an arena match (e.g., during a backfill race) will immediately release the follower after just one 3-second poll cycle. The follower then goes to independent matchmaking (or social redirect), potentially ending up in a different match. A value of 2-3 would be safer. Note: a parallel branch (4160721e3) has already bumped this to 3.

### GC-12: Matchmaker disable_matchmaker global guard

- **Location**: `server/pipeline_matchmaker.go:24-30` (Nakama core matchmaker), `server/evr_lobby_group.go:60-62` (EVR party matchmaker)
- **Condition**: `ServiceSettings().Matchmaking.DisableMatchmaker`
- **What it bypasses**: Rejects ALL matchmaker ticket additions immediately with an error. Both the core Nakama matchmakerAdd and the EVR LobbyGroup.MatchmakerAdd are gated.
- **Commit**: `55b0be750` feat: add disable_matchmaker global setting to reject all matchmaker tickets
- **Assessment**: **[CORRECT]** — Clean kill switch. Both paths are gated consistently. Does not affect social lobby find-or-create (which doesn't use the matchmaker).

### GC-13: Malformed matchmaker entry filter

- **Location**: `server/evr_matchmaker.go:159-176`
- **Condition**: `e == nil || isNilPresence(e.GetPresence()) || e.GetProperties() == nil`
- **What it bypasses**: Silently drops malformed entries from the matchmaker candidate list before processing. Without this, a single nil entry can panic the matchmaker goroutine and stall the entire queue.
- **Commit**: `4978b38bd` fix(matchmaking): crack down on social-lobby race, party-splitting, nil panics
- **Assessment**: **[CORRECT] [MASKS-BUG]** — The filter is correct defensive code, but it masks the root cause of nil entries entering the matchmaker. The upstream producer should never create nil entries. The warning log helps diagnosis, but there is no alerting threshold.

### GC-14: Nil presence guards in backfill ExtractUnmatchedCandidates

- **Location**: `server/evr_lobby_backfill.go:413-419, 430-442`
- **Condition**: `if entry == nil { continue }` / `if presence == nil { continue }` / `if mp == nil { mp = &MatchmakerPresence{} }`
- **What it bypasses**: Skips nil entries/presences in the matched and unmatched candidate extraction. Falls back to empty MatchmakerPresence struct if type assertion fails.
- **Commit**: `4978b38bd` fix(matchmaking): crack down on social-lobby race, party-splitting, nil panics
- **Assessment**: **[CORRECT] [MASKS-BUG]** — Same as GC-13. Defensive code that prevents panics but doesn't address why nil entries exist. The empty MatchmakerPresence fallback (line 442) means a player with unknown identity could be backfilled — though downstream joins would likely fail on the empty session ID.

### GC-15: latencyHistory nil guard in newLobby

- **Location**: `server/evr_lobby_find.go:524-529`
- **Condition**: `if lobbyParams.latencyHistory != nil { if lh := lobbyParams.latencyHistory.Load(); lh != nil { ... } }`
- **What it bypasses**: Passes nil latestRTTs to LobbyGameServerAllocate if latency history is unavailable. The allocator then selects a server without latency-based sorting.
- **Commit**: `87bc4b850` fix: address QA findings from prediction/process/parameters review
- **Assessment**: **[CORRECT]** — Prevents nil pointer panic. LobbyGameServerAllocate handles nil RTT maps by falling back to arbitrary server selection.

### GC-16: ErrPreJoinPingFailed skip in lobbyFindOrCreateSocial

- **Location**: `server/evr_lobby_find.go:640-644` (in the social lobby join loop, lines referenced from diff)
- **Condition**: `errors.Is(err, ErrPreJoinPingFailed)`
- **What it bypasses**: When joining an existing social lobby fails due to ping validation, continues to the next candidate instead of returning a fatal error.
- **Commit**: `4a680fe4c` fix: skip server on pre-join ping failure instead of erroring the player
- **Assessment**: **[CORRECT]** — Without this, a single unreachable server would fail the entire social lobby search. The continue lets the player try other servers.

### GC-17: Relaxed RTT for spectators and private lobbies

- **Location**: `server/evr_lobby_prejoin_ping.go:200-213, 262, 332`
- **Condition**: `relaxRTT := isPrivateMatch(label.Mode)` OR entrant is spectator
- **What it bypasses**: Disables RTT threshold enforcement for pre-join ping. Only reachability matters (timeout = fail, any RTT = pass). This means spectators and private-lobby players can join high-latency servers.
- **Commit**: `201f3dbb1` fix(ping): exempt spectators and private lobbies from RTT limit
- **Assessment**: **[CORRECT]** — Spectators don't need low latency (they're observing). Private lobbies are player-chosen, so the player accepts the latency.

### GC-18: LeavePartyStream removed from LobbyJoinSessionRequest and LobbyCreateSessionRequest

- **Location**: `server/evr_lobby_session.go:81-85` (join), `server/evr_lobby_session.go:95-102` (create) — LeavePartyStream calls REMOVED
- **Condition**: N/A (removal of guard, not addition)
- **What it bypasses**: Previously, joining a specific session or creating a session would leave the party stream. Now the party stream is preserved across these transitions.
- **Commit**: `2b2acb961` fix: preserve party stream across lobby transitions
- **Assessment**: **[CORRECT] [NEEDS-TEST]** — Preserving party membership across transitions is necessary for party follow to work. However, if a player creates a private lobby while in a party, they remain in the party — followers may try to follow them into the private lobby (mitigated by GC-07 ModeSocialPrivate block, but not mitigated for ArenaPrivate/CombatPrivate). LeavePartyStream is still called for spectators (line 58).

### GC-19: handleMatchmakingError also removed LeavePartyStream on error

- **Location**: `server/evr_lobby_session.go:108-135`
- **Condition**: N/A (the old code called LeavePartyStream on matchmaking error; the new handleMatchmakingError does not)
- **What it bypasses**: Previously, any matchmaking error would eject the player from their party. Now they stay in the party after errors.
- **Commit**: `1c28f2f0b` fix: don't destroy party on matchmaking error
- **Assessment**: **[CORRECT]** — Destroying the party on a transient error (timeout, server unavailable) is destructive. Players should stay in their party and be able to retry.

### GC-20: Sequential backfill with early break on lobby full

- **Location**: `server/evr_lobby_backfill.go:354-356`
- **Condition**: `if isLobbyFullError(err) { break }`
- **What it bypasses**: When backfilling multiple entrants sequentially into a match, stops immediately if the match is full instead of trying remaining entrants.
- **Commit**: `66160727f` fix(backfill): join entrants sequentially to prevent lobby-full race (#416)
- **Assessment**: **[CORRECT]** — The old code joined entrants concurrently (goroutines), which caused a race where multiple entrants would attempt to claim the last slot simultaneously, resulting in lobby-full errors and wasted cycles. Sequential + early break is the correct fix.

### GC-21: Combat mode team balance guards in backfill

- **Location**: `server/evr_lobby_backfill.go:252-265` (getPossibleTeams), `server/evr_lobby_backfill.go:290-300` (isCombat exclusion from both-teams)
- **Condition**: `isCombat := candidate.Mode == evr.ModeCombatPublic` — if combat AND adding to preferred team would create imbalance, return nil (no valid teams)
- **What it bypasses**: Prevents combat backfill from placing a player on a team that would be larger than the other team. Also prevents "add to both teams" fallback for combat (arena allows this for faster filling).
- **Commit**: `ac29ea751` / `c292af7f6` Combat matchmaking updates
- **Assessment**: **[CORRECT]** — Combat requires strict team balance. Without this, a 3v2 imbalance could occur during backfill.

### GC-22: flushMatchRegistryLabelUpdates after newLobby

- **Location**: `server/evr_lobby_find.go:558`
- **Condition**: Always called after successful LobbyGameServerAllocate. No-op if registry is not LocalMatchRegistry.
- **What it bypasses**: Eliminates the Bluge index update delay (up to 1s). Without this flush, concurrent lobbyFindOrCreateSocial callers race on stale index state and each create their own lobby instead of converging on the first one.
- **Commit**: `4978b38bd` fix(matchmaking): crack down on social-lobby race, party-splitting, nil panics
- **Assessment**: **[CORRECT]** — Directly addresses the duplicate social lobby creation race. The no-op fallback for non-LocalMatchRegistry is safe.

### GC-23: Expired ticket cleanup in matchmaker Process

- **Location**: `server/matchmaker.go:427-468`
- **Condition**: `index.Intervals >= m.config.GetMatchmaker().MaxIntervals` — only fully expired tickets get cleaned up
- **What it bypasses**: Previously, expired active tickets were only moved to the passive pool. Now, tickets that have reached MaxIntervals are fully deleted from the index, session/party maps, and Bluge index. This prevents matchmaker queue buildup.
- **Commit**: `cd1a0fd98` fix: OOM crash prevention — VRML startup race, Discord goroutine pileup, matchmaker leak
- **Assessment**: **[CORRECT]** — Fixes a memory leak. Tickets that expire but never get cleaned up accumulate in the Bluge index and in-memory maps, eventually causing OOM. The MinCount == MaxCount check (line 434) correctly preserves passive-only tickets that haven't truly expired.

### GC-24: Combat mode per-player ticket splitting in groupEntriesSequentially

- **Location**: `server/evr_matchmaker_process.go:185-192`
- **Condition**: `if isCombat { ticket = entry.GetPresence().GetUserId() }`
- **What it bypasses**: For combat mode, treats each player as a separate "ticket" even if they were submitted together. This allows the matchmaker to split parties across teams for combat balance.
- **Commit**: `ac29ea751` / `c292af7f6` Combat matchmaking updates
- **Assessment**: **[CORRECT]** — Combat matchmaking requires the ability to put party members on different teams. Without this, a 3-player party in combat mode would always be on the same team, making balanced matches impossible.

### GC-25: isNilPresence reflect-based typed-nil check

- **Location**: `server/evr_matchmaker.go:33-47`
- **Condition**: Checks for both nil interface and typed-nil (interface holds a nil pointer to a concrete type)
- **What it bypasses**: Used in GC-13's malformed entry filter. Prevents the classic Go "typed nil" panic where `var p *MatchmakerPresence = nil; var rp runtime.Presence = p` makes `rp != nil` true but method calls panic.
- **Commit**: `4978b38bd` fix(matchmaking): crack down on social-lobby race, party-splitting, nil panics
- **Assessment**: **[CORRECT]** — Uses reflect which has a performance cost per call, but this runs once per matchmaker cycle per entry, not in a hot loop.

---

## Critical Finding: ModeSocialPrivate Inconsistency (GC-07)

TryFollowPartyLeader (line 1149) correctly EXCLUDES ModeSocialPrivate from joinable modes after KC-1 security fix.

pollFollowPartyLeader (line 1183) STILL INCLUDES ModeSocialPrivate:
```go
case evr.ModeSocialPrivate, evr.ModeSocialPublic, evr.ModeCombatPublic, evr.ModeArenaPublic:
```

This means: if a follower enters the poll loop (already in a lobby, waiting for leader), and the leader moves to a private social lobby, pollFollowPartyLeader will attempt to join it. This bypasses the security gate that KC-1 was designed to enforce.

**Severity**: Security — the private lobby invitation requirement can be bypassed via the poll path.

---

## Critical Finding: Released Follower lobbyGroup Not Nilled (GC-06)

The diff for `f6e6e127c` added `lobbyGroup = nil` at the release point to prevent addTicket from enforcing leader-only party submission. Current HEAD at line 127 has `SetPartySize(1)` but does NOT nil lobbyGroup. The code path for non-social followers who can't join the leader (lines 122-128) now looks like:

```go
lobbyParams.SetPartySize(1)
// lobbyGroup is NOT nil here
// Falls through to matchmaking with lobbyGroup still set
```

If `lobbyGroup` is still non-nil when the released follower reaches the `lobbyMatchMakeWithFallback` call (line 315), the matchmaking code may attempt to submit a party ticket (which would be rejected because this session is not the leader).

**Severity**: Functional — released followers in non-social modes may fail to enter matchmaking silently.

---

## Party Follow Flow Diagram (Current HEAD)

```
lobbyFind(session, lobbyParams)
  |
  +-- PartyGroupName set and != "tablet"?
  |   |
  |   YES --> configureParty()
  |   |         |
  |   |         +-- isLeader?
  |   |         |   YES --> Track on matchmaking stream [GC-04]
  |   |         |           |
  |   |         |           +-- CurrentMatchID set && !Social?
  |   |         |           |   YES --> Wait for party members (30s) [waitLoop]
  |   |         |           |
  |   |         |           +-- CurrentMatchID nil && size<=1 && !Social?
  |   |         |               YES --> Grace period wait (3s) [GC-05]
  |   |         |
  |   |         +-- isLeaderHeadingToSocial? [GC-03]
  |   |             YES --> Force mode=SocialPublic [GC-02]
  |   |
  |   +-- lobbyAuthorize() --> mode switch validation
  |   |
  |   +-- lobbyGroup != nil?
  |       |
  |       YES, !isLeader:
  |       |   |
  |       |   +-- TryFollowPartyLeader() [GC-07, GC-08]
  |       |   |   |
  |       |   |   +-- Leader in SocialPrivate? --> return false (BLOCKED) [GC-07]
  |       |   |   +-- Leader's match full, follower at menu?
  |       |   |   |   YES --> Force mode=SocialPublic, return false [GC-08]
  |       |   |   +-- Joined leader's match? --> return true (DONE)
  |       |   |   +-- Failed to join? --> pollFollowPartyLeader [GC-11]
  |       |   |       |
  |       |   |       +-- isFollowerInLeaderMatch? [GC-09, GC-10]
  |       |   |       |   YES --> return true (DONE)
  |       |   |       |
  |       |   |       +-- Poll loop (3s intervals):
  |       |   |           +-- Context canceled? Check convergence [GC-09]
  |       |   |           +-- Leader in non-joinable, cycle limit? [GC-11]
  |       |   |           |   YES --> return false
  |       |   |           +-- Leader in SocialPrivate? --> JOIN ATTEMPTED [BUG]
  |       |   |           +-- Join failed (full)? --> retry with 5s backoff
  |       |   |
  |       |   +-- TryFollow returned false:
  |       |       |
  |       |       +-- Did we become leader? --> proceed as leader
  |       |       |
  |       |       +-- shouldFollowerFindOrCreateSocial? [GC-01]
  |       |       |   YES --> lobbyFindOrCreateSocial (DONE)
  |       |       |
  |       |       +-- pollFollowPartyLeader (non-social follower)
  |       |           |
  |       |           +-- Poll returned true? --> DONE
  |       |           +-- Poll returned false (timeout/non-joinable):
  |       |               |
  |       |               +-- *** RELEASE TO SOLO *** [GC-06]
  |       |               |   SetPartySize(1)
  |       |               |   (lobbyGroup NOT nilled -- POSSIBLE BUG)
  |       |               |
  |       |               +-- Falls through to matchmaking below
  |       |
  |       YES, isLeader:
  |           +-- Add member session IDs to entrants
  |
  |   NO (no party):
  |       +-- SetPartySize(1)
  |
  +-- PrepareEntrantPresences()
  +-- appendPartyReservationPlaceholders()
  +-- CheckServerPing()
  +-- EarlyQuitPenalty check (Arena only)
  +-- vibinatorsGravityCheck
  |
  +-- Mode == SocialPublic?
  |   YES --> lobbyFindOrCreateSocial (DONE)
  |           |
  |           +-- Priority 1: Join leader/follower's specific social lobby
  |           +-- Priority 2: Join any matching social lobby
  |           +-- Priority 3: Create new social lobby + flush index [GC-22]
  |           +-- Pre-join ping skip on failure [GC-16]
  |
  +-- Mode == Arena/Combat?
      YES --> lobbyMatchMakeWithFallback (matchmaker ticket)
              |
              +-- DisableMatchmaker? --> reject [GC-12]
              +-- Malformed entry filter [GC-13]
```

### "Release to Solo" Escape Hatches

There are three paths where a follower is released from party follow to independent play:

1. **Social mode follower** (GC-01, line 97-104): Immediately routes to lobbyFindOrCreateSocial. Party reservations ensure convergence. **This is the cleanest escape.**

2. **Leader's match full, follower at menu** (GC-08, line 1134-1142): Forces mode to SocialPublic via side-effect mutation. Follower enters social lobby. **Works but architecturally questionable (hidden state mutation).**

3. **Poll timeout / non-joinable match** (GC-06, line 122-128): Sets party size to 1 but does NOT nil lobbyGroup. Falls through to normal matchmaking. **Potentially broken for non-social modes if lobbyGroup causes issues downstream.**
