# Party Commit Audit: Reverse Chronological Analysis

**Base:** `ba5ee8f12b3b66c347eb2e1081414c4fa2b82be2`
**HEAD at audit time:** `f6e6e127c`
**Files audited:** `evr_lobby_find.go`, `evr_pipeline_party.go`, `party_handler.go`, `evr_lobby_matchmake.go`, `evr_lobby_group.go`, `evr_lobby_backfill.go`, `evr_matchmaker.go`, `evr_matchmaker_process.go`, `matchmaker.go`, `matchmaker_process.go`, `evr_lobby_joinentrant.go`, `evr_lobby_session.go`
**Auditor:** Reverse-then-forward methodology

---

## Phase 1: Backward Read (HEAD to base)

### f6e6e127c — fix(party): released followers must queue as solo
**What changed:** When a follower is released from the party poll loop in non-social mode, sets `lobbyGroup = nil` in addition to the existing `SetPartySize(1)`.
**Flag:** [OK]
**Rationale:** Without this, `addTicket` still sees a non-nil `lobbyGroup` and enforces leader-only party submission, preventing the released follower from actually entering the matchmaker. Clean targeted fix.

---

### 894c2d5a6 — docs: improve comments for clarity in flags and lobby find logic
**What changed:** Comment rewording only. "we" -> passive voice, shorter phrasing.
**Flag:** [OK]
**Rationale:** Pure documentation cleanup.

---

### 5fcf14fa0 — fix: replace publicGroupID with GroupID in lobby parameters and matchmaking streams
**What changed:** Removes the `publicGroupID()` helper introduced in `87bc4b850`. Replaces all uses with raw `p.GroupID`. Changes `MatchmakingStream()` and `GuildGroupStream()` from value receivers to pointer receivers.
**Flag:** [REGRESSIVE] [CONTRADICTS 87bc4b850]
**Rationale:** `publicGroupID()` was the central fix from PR #437 that prevented the "island bug" (players in different guilds trapped in separate matchmaking pools). This commit removes the function entirely and uses raw `p.GroupID` everywhere -- including `MatchmakingStream()` and `GuildGroupStream()`, which means public-mode players are back to guild-specific streams. A fix exists on branch `fix/party-rtt-convergence` (`ad5ed4bbb`) that correctly scopes the nil-GroupID to only the stream subjects, but it has not been merged to HEAD. **This is a live regression for cross-guild public matchmaking.**

---

### 6f7fcb163 — fix(party): resolve Duo Desync with tracker-based convergence fallback
**What changed:** Three changes to `pollFollowPartyLeader`:
1. `isFollowerInLeaderMatch()` falls back to tracker stream comparison when `MatchLabelByID` is unavailable (nil NK or registry miss). Previously required MatchLabelByID to succeed.
2. Early convergence checks at three entry points (before loop, after poll interval, after settle wait).
3. Nil NK guard at settle-wait label lookup.
**Flag:** [SUSPICIOUS]
**Rationale:** The tracker-based fallback `return true` path is less strict than the MatchLabelByID path. When `p.nk != nil` but `MatchLabelByID` returns an error, the code falls through to `return true` -- meaning any transient registry error causes the function to declare convergence without verifying the follower is actually in the match's player list. This partially reverts the safety added in `8c9cb219f` which specifically added MatchLabelByID verification to prevent false positives from tracker convergence alone. The early convergence checks are good, but the fallback weakens the guarantees.

---

### 8b531d342 — Merge PR #437: Matchmaker Not Creating Games
**What changed:** Merge commit for PR #437. Combines `87bc4b850` (QA fixes) with the matchmaking fixes branch.
**Flag:** [OK]
**Rationale:** Clean merge of reviewed work.

---

### 87bc4b850 — fix: address QA findings from prediction/process/parameters review
**What changed:**
1. Replace `nil` check with `len==0` guard on candidate slice (panic fix).
2. Track `foundTimestamp` in `isUndersizedMatch`; treat missing timestamps as expired (stuck-forever fix).
3. Extract `publicGroupID()` helper to centralize the public-mode GroupID->uuid.Nil override.
**Flag:** [OK]
**Rationale:** All three fixes are well-motivated. The `publicGroupID()` extraction eliminated 6 duplicate mode-check blocks. (Later undone by `5fcf14fa0` -- see above.)

---

### c1c1347cb — fix(security): block ModeSocialPrivate in party follow path (KC-1)
**What changed:** Removes `evr.ModeSocialPrivate` from the allowed-modes switch in both `TryFollowPartyLeader` and `pollFollowPartyLeader`. Adds regression tests.
**Flag:** [OK]
**Rationale:** Genuine security fix. Party follow should not bypass the private-lobby invitation gate. Well-scoped and tested.

---

### d9d0bec63 — fix(party): resolve infinite matchmaking and silent failure cascade
**What changed:** Large fix commit addressing multiple issues:
- **P0 (critical):** `lobbyJoin` was always returning nil even when `LobbyJoinEntrants` failed. Added `return fmt.Errorf("lobbyJoin: %w", err)`.
- **P0:** `configureParty` was discarding `json.Marshal` error via `statusBytes, _ := json.Marshal(...)`.
- **Restoration:** 13 logger lines stripped by `20bea2927` restored.
- **H1-H9:** Capture tracker.Track `isNew`; log re-tracking events.
- **H2:** Replace inline social-mode predicates with `shouldFollowerFindOrCreateSocial()`.
- **H3:** Cache `isLeaderHeadingToSocial` result to avoid TOCTOU double-read.
- **H4-H6:** Log previously-silent errors in MatchLabelByID and json.Unmarshal paths.
- **H7:** Document the intentional `params.Mode` mutation side-effect in `TryFollowPartyLeader`.
**Flag:** [OK] — Critical remediation
**Rationale:** The `lobbyJoin` always-nil-return was the root cause of a silent failure cascade: callers assumed the join succeeded, clients hung in matchmaking forever. The error-swallowing `json.Marshal` discard was also dangerous. This commit is the single most important fix in the range.

---

### 9b4e3be1a — Party System Fixes & Improvements
**What changed:** Three features by heisthecat31:
1. Early tracking of leader on `StreamModeMatchmaking` inside `configureParty`.
2. Follower fallback to Social when leader's match is full and follower is at main menu (mutates `params.Mode`).
3. Leader priority join: leader iterates party members looking for followers already in a social lobby, then joins that lobby.
**Flag:** [OK]
**Rationale:** Addresses real UX problems. The leader-priority-join feature adds 50 lines of nested logic inside `lobbyFindOrCreateSocial` which is getting complex, but the behavior is correct.

---

### 052be7432 — Party Matchmaking Race Condition Fix (Early Tracking)
**What changed:** Subset of `9b4e3be1a` -- adds early matchmaking stream tracking in `configureParty` and deferred untrack in `lobbyFind`. Identical code to what appears in 9b4e3be1a.
**Flag:** [SUSPICIOUS]
**Rationale:** This commit and `9b4e3be1a` overlap significantly -- 9b4e3be1a includes all of 052be7432's changes plus more. Both were committed by heisthecat31. Suggests the commits were not properly squashed or one was a partial that was meant to be superseded.

---

### 1aa7211fa — Added test to ensure no infinite social matchmaking
**What changed:** Reorders `isLeaderHeadingToSocial` to check the matchmaking stream first (intent), then the match stream (location). Previously checked match stream first.
**Flag:** [OK]
**Rationale:** The fix is correct -- matchmaking intent should take precedence over current location. A leader sitting in a social lobby while matchmaking for Arena should not cause followers to be forced into Social.

---

### 32aea761b — Merge branch 'Parties' into 'Parties'
**What changed:** Merge of `20bea2927` (Infinite Matchmaking fix) with `d134ecc8f` (party team repair).
**Flag:** [OK]
**Rationale:** Branch convergence merge.

---

### 20bea2927 — Infinite Matchmaking fix
**What changed:** Large refactor of `pollFollowPartyLeader` and `lobbyFind`:
1. Added social-mode bypass to skip polling loop for social followers.
2. Added leader priority convergence via tracker lookup.
3. Added `isLeaderHeadingToSocial` function.
4. **Stripped 13+ logger lines from `pollFollowPartyLeader`** (debug, warn, info).
5. Removed stale-match-ID guard against false positives.
6. Removed "is leader still matchmaking" check from poll loop.
7. Removed comments explaining "why" for multiple safety checks.
**Flag:** [REGRESSIVE] [BAND-AID]
**Rationale:** While the social-mode bypass and leader convergence are real improvements, this commit strip-mines the observability out of `pollFollowPartyLeader`. It removes 13 log lines that were the only way to diagnose party-following failures in production. It also removes the stale-match-ID guard (which prevented false positives when both players point to the old social lobby) and the "is leader still matchmaking" check. These were not bugs -- they were safety nets. Commit `d9d0bec63` later had to restore the log lines. The comment removal makes the code harder to reason about for anyone who did not write it.

---

### 65d73fb00 — Matchmaking fixes
**What changed:** Two major fixes:
1. "Island Bug" -- public modes now use `uuid.Nil` for `GroupID` in `newLobby`. Inline mode check.
2. `isUndersizedMatch` -- timestamp type switch to handle `int64`/`int` in addition to `float64`.
**Flag:** [OK]
**Rationale:** Both fixes are correct and well-documented. The timestamp type-switch fixes the "death loop" where combat matches could never start because the timer was effectively stuck at zero. Note: the inline GroupID fix in `newLobby` was later extracted to `publicGroupID()` in `87bc4b850`, then removed entirely in `5fcf14fa0`.

---

### d134ecc8f — Fix party team repair and social follower guard (#436)
**What changed:** Adds `shouldFollowerFindOrCreateSocial()` helper to replace inline `Mode == ModeSocialPublic || Mode == ModeSocialNPE` checks.
**Flag:** [OK]
**Rationale:** Good extraction. Only replaces one of many inline checks in this commit; `d9d0bec63` later replaces the rest. Clean.

---

### e7ec68b80 — fix: prevent infinite matchmaking when party leader transitions to social
**What changed:** Moves party resolution (`configureParty`) earlier in `lobbyFind` -- before `lobbyAuthorize`. This ensures mode synchronization happens before auth, preventing arena/social desync.
**Flag:** [OK]
**Rationale:** Important structural fix. If auth runs before mode sync, a follower's request to join Arena could be authorized, then their mode gets flipped to Social, resulting in a mismatch. Moving party resolution first ensures the follower's mode is correct before any decisions are made.

---

### 53f6c94a4 — fix: matchmaker MinCount 4->2 for Combat, fix ticket->UserID rating lookup
**What changed:** Combat `MinCount` 4->2 to allow 1v1 matches to form.
**Flag:** [OK]
**Rationale:** Enables combat matchmaking at lower player counts. Duplicate of the same change in `487b04970`.

---

### 50b8ca773 — Update evr_lobby_find.go (removed stray line)
**What changed:** Removes a blank line.
**Flag:** [OK]

---

### 585b78f2d — Update evr_lobby_find.go
**What changed:** Identical to `e7ec68b80` -- moves party resolution early, adds `isLeaderHeadingToSocial` nil-lobbyGroup fallback removal.
**Flag:** [SUSPICIOUS]
**Rationale:** This commit and `e7ec68b80` make the same structural change. Both by heisthecat31, submitted 12 minutes apart. One of these appears to be a duplicate or a partial re-application.

---

### 4d2e15f0a — Changed structure and updated test file
**What changed:** Introduces `isLeaderHeadingToSocial` and moves the mode-sync check to the very start of `lobbyFind` (before party resolution), with a nil-lobbyGroup fallback that calls `configureParty` again.
**Flag:** [BAND-AID]
**Rationale:** The nil-lobbyGroup fallback means `isLeaderHeadingToSocial` could call `configureParty` *before* the main `configureParty` call, creating a redundant party-join. This was fixed 16 minutes later in `585b78f2d` which moved party resolution to happen first.

---

### 727583a00 — Update evr_lobby_find.go
**What changed:** First introduction of `isLeaderHeadingToSocial` and the follower mode-force logic.
**Flag:** [OK]
**Rationale:** The initial correct version -- checks inside the party block after `configureParty`. Was immediately moved around by subsequent commits.

---

### 487b04970 — Matchmaker changes
**What changed:** Combat `MinCount` 4->2 (duplicate of `53f6c94a4`).
**Flag:** [OK]
**Rationale:** The change is correct. Appears on a separate branch that was later merged.

---

### 2cb4213bc — Merge PR #431: Party infinite matchmaking fix
**What changed:** Merges `6fc5c36d5` which adds the social-mode bypass for followers.
**Flag:** [OK]

---

### 6fc5c36d5 — Infinite Matchmaking fix
**What changed:** First version of the social-mode bypass: after `TryFollowPartyLeader` returns false, if mode is social, skip the polling loop and go directly to `lobbyFindOrCreateSocial`.
**Flag:** [OK]
**Rationale:** Correct fix for the root cause. Social lobbies use find-or-create with party reservations, so polling for the leader to settle is unnecessary and was the source of the timeout-induced infinite matchmaking.

---

### 583bf6bc4 — Merge branch 'CombatMatchmaking'
**What changed:** Branch merge.
**Flag:** [OK]

---

### ac29ea751 — Combat matchmaking update
**What changed:** Changes combat party splitting key from `SessionId` to `UserId`.
**Flag:** [OK]
**Rationale:** Fixes `ca2b76dea` which incorrectly used `SessionId` for combat ticket grouping. `UserId` is the correct key for rating lookups.

---

### c292af7f6 — Combat matchmaking update
**What changed:** Major combat backfill overhaul:
1. Combat balance enforcement -- only allow backfill that maintains or restores team balance.
2. Pair-wise joining for balanced matches.
3. Party splitting in backfill for combat.
**Flag:** [OK]
**Rationale:** Well-structured combat-specific backfill logic. The pair-wise joining is a creative solution to the "can't add one player to a balanced match" problem.

---

### ca2b76dea — feat: add combat matchmaking toggle behind EnableCombatMatchmaking
**What changed:** Adds `EnableCombatMatchmaking` setting gate. In `groupEntriesSequentially`, combat mode uses `SessionId` as ticket key to split parties.
**Flag:** [BAND-AID]
**Rationale:** Using `SessionId` for ticket grouping is wrong -- this was fixed 1 day later by `ac29ea751` which switched to `UserId`. The feature flag gating is good practice.

---

### 2b2acb961 — fix: preserve party stream across lobby transitions
**What changed:** Removes `LeavePartyStream` calls from `LobbyJoinSessionRequest` and `LobbyCreateSessionRequest` handlers.
**Flag:** [OK]
**Rationale:** Correct fix -- leaving the party stream on lobby transitions caused the leader to disappear from the party tracker, breaking follower convergence. Identical change to `5672b6c7e` on a parallel branch.

---

### 66160727f — fix(backfill): join entrants sequentially to prevent lobby-full race (#416)
**What changed:** Replaces parallel goroutines with sequential join loop in `executeBackfillResultOptimized`. Removes sync.WaitGroup/Mutex.
**Flag:** [OK]
**Rationale:** Parallel joins to the same match caused race conditions where multiple entrants would pass the "is there room?" check simultaneously, then all try to join, causing lobby-full errors. Sequential joining is the correct fix.

---

### 03a249078 — fix(matchmaker): wait for party followers before fresh-start ticket
**What changed:** When leader starts matchmaking from main menu (no previous match) and party has only 1 member, poll for up to 3s waiting for followers to join before submitting a solo ticket.
**Flag:** [OK]
**Rationale:** Addresses a real race condition where the leader submits a solo ticket, gets placed in a full match, and the follower cannot follow. The 200ms poll interval and 3s timeout are reasonable.

---

### 6b1e27e87 — chore: demote noisy log messages to appropriate levels (#418)
**What changed:** Demotes "duplicate join attempt" from Warn to Debug, and "Failed to load archetype stats" from Warn to Debug.
**Flag:** [OK]
**Rationale:** Both are expected transient conditions, not warning-worthy.

---

### 5672b6c7e — Infinite matchmaking when in party
**What changed:** Removes `LeavePartyStream` calls from lobby session handlers. Identical to `2b2acb961`.
**Flag:** [OK]
**Rationale:** Same fix on a parallel branch. Both get merged.

---

### 14e331701 — Combat Matchmaking update
**What changed:** Adds combat party splitting in `groupEntriesSequentially` using `SessionId` (gated behind mode check but not behind feature flag).
**Flag:** [BAND-AID]
**Rationale:** Uses `SessionId` without the `EnableCombatMatchmaking` feature flag. `ca2b76dea` later adds the feature flag but keeps `SessionId`. `ac29ea751` finally fixes the key to `UserId`.

---

### 59c0d4c4e — fix(lobby): fall back to fresh metadata when entrant presence is missing (#409)
**What changed:** When tracker lookup returns nil for an existing entrant (reconnect/disconnect race), falls back to re-creating fresh metadata instead of returning an error.
**Flag:** [OK]
**Rationale:** Fixes 106 production errors. The fallback is safe -- fresh metadata is equivalent to what was there before.

---

### 4804177df — fix(matchmaker): release party follower to matchmaking when leader's match is full (#408)
**What changed:** When follower can't join leader's full match, only redirect to social if original mode was social. For non-social modes, release to independent matchmaking with party size 1.
**Flag:** [OK]
**Rationale:** Correct behavior -- don't force arena/combat followers into social lobbies. Let them queue independently.

---

### 8c9cb219f — fix: prevent party followers from being falsely marked as placed
**What changed:** Adds `MatchLabelByID` verification to `isFollowerInLeaderMatch()` and the poll loop convergence check. Stream convergence alone is no longer sufficient -- must verify follower appears in match player list.
**Flag:** [OK]
**Rationale:** Critical safety fix. Tracker stream convergence can happen before the client completes the join, causing party splits where the leader enters a match but followers stay in social. **Note: partially weakened by `6f7fcb163` which falls back to tracker-only convergence when MatchLabelByID is unavailable.**

---

### 6c8263042 — Merge PR #388: fix(matchmaking): crack down on social-lobby race, party-splitting, nil panics
**What changed:** Merge of `4978b38bd`.
**Flag:** [OK]

---

### 55b0be750 — feat: add disable_matchmaker global setting
**What changed:** Adds `DisableMatchmaker` bool to global settings. Rejects all matchmaker tickets when enabled.
**Flag:** [OK]
**Rationale:** Useful debugging/maintenance toggle.

---

### cd1a0fd98 — fix: OOM crash prevention
**What changed:** Three OOM fixes:
1. VRML verifier startup race.
2. Discord REST limiter with semaphore.
3. **Matchmaker: clean expired tickets from `m.indexes` (not just `activeIndexes`)** to prevent unbounded memory growth.
**Flag:** [OK]
**Rationale:** The matchmaker leak fix is significant -- without it, `m.indexes` grows indefinitely as tickets expire from `activeIndexes` but persist in the main map. Downgrading "missing index" from Warn to Debug is correct since it's expected during ticket cancellation.

---

### 184fa9267 — Merge fix/party-separation (#392)
**What changed:** Merges `1c28f2f0b`.
**Flag:** [OK]

---

### a5c3bfc89 — Merge chore/lsp-diagnostics (#391)
**What changed:** Merges `8b83bdd40` (LSP diagnostics cleanup).
**Flag:** [OK]

---

### 1c28f2f0b — fix: don't destroy party on matchmaking error
**What changed:** Extracts `handleMatchmakingError` from `handleLobbySessionRequest`. **Removes `LeavePartyStream` from the error path.**
**Flag:** [OK]
**Rationale:** Critical fix -- matchmaking timeout/failure was destroying the player's party. When they re-queued, `GetOrCreateByGroupName` created a new solo party instead of rejoining the original. This was the root cause of party-of-3 separation.

---

### fafd71e96 — test: add configureParty kick regression tests, log PartyGroupName
**What changed:** Adds `PartyGroupName` to "Joined party group" log message. Adds regression tests.
**Flag:** [OK]

---

### 8b83bdd40 — chore: fix LSP diagnostics across codebase
**What changed:** `interface{}` -> `any`, removes unused stream-member-kick code from `configureParty`, modernizes loops.
**Flag:** [SUSPICIOUS]
**Rationale:** The removal of the stream-member-kick code from `configureParty` (the block that kicked members not following the leader) is buried in a "chore: fix LSP diagnostics" commit. This is behavioral, not a diagnostic fix. The removed code called `StreamUserKick` on party members not in the matchmaking stream, which was apparently causing followers to get their contexts canceled during queue transitions. But removing it silently as part of a chore commit means it was not explicitly reviewed as a behavioral change.

---

### 57a75f128 — fix: set LockedAt on arena lock signal, remove dead code
**What changed:** Removes dead `executeBackfillResult` function and unused `ws` variable. Sets `LockedAt` timestamp on arena RoundClosing signal.
**Flag:** [OK]

---

### 5a0685679 — feat: add GameStatusRoundClosing and wire up SignalLockSession
**What changed:** Adds `RoundClosing` game status. Arena lock signal sets status to round_closing.
**Flag:** [OK]

---

### c0d4c807f — fix: replace arena backfill time limit with RoundClosing status filter
**What changed:** Replaces hardcoded 4.5-minute age filter with `game_status:round_closing` query filter.
**Flag:** [OK]
**Rationale:** Better mechanism -- relies on the actual game state signal rather than a time estimate.

---

### 4978b38bd — fix(matchmaking): crack down on social-lobby race, party-splitting, nil panics
**What changed:** Multi-fix commit:
1. `ListMatchStates` minSize parameterized (was hardcoded 1) -- fixes "6 players, 6 separate lobbies" by allowing fresh empty lobbies to be found.
2. Synchronous Bluge index flush after `LobbyGameServerAllocate`.
3. Removed pre-wait delays in `lobbyFindOrCreateSocial`.
4. `groupEntriesSequentially` trim-to-countMultiple no longer splits parties.
5. Nil guards for `latencyHistory.Load().LatestRTTs()`, `GetRating`, presence chains.
6. `EvrMatchmakerFn` rejects nil/typed-nil entries up front.
**Flag:** [OK]
**Rationale:** Comprehensive, well-tested fix batch. The synchronous flush and minSize=0 fix together eliminate the social lobby fragmentation race. The party-splitting fix in groupEntriesSequentially is important for correctness.

---

### 8d4ea6598 — fix: remove broken EscapeIndexValue from match label queries
**What changed:** Removes `EscapeIndexValue` which over-escaped characters like `_` and `.` in Bluge keyword queries, causing all queries to return zero results.
**Flag:** [OK]
**Rationale:** This was the root cause of all players being placed in separate solo social lobbies -- every `ListMatchStates` query returned zero candidates because the escaped query was syntactically wrong for Bluge. Introduced just one day prior.

---

### 01c1493e2 — Revert "fix: skip RTT validation for social lobbies, fix nil backfill slice"
**What changed:** Reverts `1ee4fc852`.
**Flag:** [OK]
**Rationale:** The backfill slice fix in `1ee4fc852` introduced nil holes in the prepared matches slice by switching from `append` to indexed assignment with `continue`. The revert is correct.

---

### 1ee4fc852 — fix: skip RTT validation for social lobbies, fix nil backfill slice
**What changed:** Switches backfill prepareMatches from indexed assignment to append (to avoid nil holes).
**Flag:** [BAND-AID]
**Rationale:** Reverted next commit. The slice fix was correct in isolation but was bundled with an RTT validation skip that had problems.

---

### c221b1457 — fix: pre-warm all social lobby server pings before the join loop
**What changed:** Collects all unique server endpoints from match list and sends ping requests in parallel before iterating candidates.
**Flag:** [OK]
**Rationale:** Eliminates per-lobby ping round-trips that could serialize into chains of 5-second timeouts.

---

### 0d60f971a — fix: skip existing social lobbies on ping failure, not auto-created fallback
**What changed:** Removes the `ErrPreJoinPingFailed` continue for auto-created lobbies, keeping it only for existing-lobby-join path.
**Flag:** [OK]
**Rationale:** Correct -- if we created a new lobby specifically for this player and the ping fails, skipping it and creating another one just fragments players into isolated lobbies. Better to join the slightly-high-latency lobby than have no lobby.

---

### 4a680fe4c — fix: skip server on pre-join ping failure instead of erroring the player
**What changed:** On ping failure in `lobbyFindOrCreateSocial`, continue to next candidate instead of returning error. Adds `ErrPreJoinPingFailed` sentinel.
**Flag:** [OK]
**Rationale:** Correct for existing lobbies. (The auto-created-lobby path was also given a continue, which was wrong -- fixed immediately in `0d60f971a`.)

---

### 0eae10fad — fix: address Copilot review comments on toxic player separation
**What changed:** Loads `ServiceSettings()` once at the top of `processPotentialMatches`. Documents party-ticket leader-property limitation.
**Flag:** [OK]

---

## Phase 2: Forward Read (base to HEAD)

### The Story These Commits Tell

**Act 1: Infrastructure Fixes (Apr 11-15)** -- Commits `0eae10fad` through `184fa9267`

The codebase had fundamental infrastructure problems: `EscapeIndexValue` broke all match queries, `ListMatchStates(minSize=1)` hid freshly-created lobbies, Bluge index flush lag created race windows, and `LeavePartyStream` on matchmaking errors destroyed parties. These were fixed systematically by Andrew Bates and Metis/Claude, with good test coverage. The fixes are clean and well-motivated.

**Act 2: The Combat Matchmaking Branch (Apr 25)** -- Commits `14e331701` through `ca2b76dea`

heisthecat31 introduces combat matchmaking with party splitting. The initial implementation uses `SessionId` as the grouping key (wrong), which is fixed to `UserId` two commits later. The feature flag gating is good but arrived after the initial commit.

**Act 3: The "Infinite Matchmaking" Arc (Apr 25 - May 11)** -- Commits `5672b6c7e` through `9b4e3be1a`

This is where things get messy. The core problem -- followers stuck in infinite matchmaking when transitioning to/from social lobbies -- gets fixed multiple times in overlapping, partially-duplicated commits:

1. `6fc5c36d5` introduces the social-mode bypass (correct fix).
2. `20bea2927` rewrites large portions of `pollFollowPartyLeader` and `lobbyFind`, adding leader priority convergence but **stripping 13 logger lines and several safety checks**. Comments explaining "why" are deleted.
3. `727583a00` -> `4d2e15f0a` -> `585b78f2d` -> `e7ec68b80` -- four commits in ~3 hours moving `isLeaderHeadingToSocial` around, with intermediate states that call `configureParty` redundantly.
4. `052be7432` and `9b4e3be1a` overlap significantly.
5. `d9d0bec63` (the cleanup commit) has to restore the stripped logging and fix the error-swallowing bugs.

**Act 4: The GroupID Saga (May 1-18)** -- Commits `65d73fb00` through `5fcf14fa0`

The "island bug" (players in different guilds isolated into separate matchmaking pools) gets fixed three different ways:
1. `65d73fb00`: Inline `uuid.Nil` override in `newLobby`.
2. `87bc4b850`: Extracted to `publicGroupID()` helper, used in 6 places.
3. `5fcf14fa0`: **Removes `publicGroupID()` entirely**, reverting streams to raw `p.GroupID`.

A correct fix exists on an unmerged branch (`ad5ed4bbb`) that scopes the nil-GroupID to only stream subjects. But on HEAD, the streams use raw `p.GroupID`, meaning **cross-guild public matchmaking is broken again**.

**Act 5: Safety vs. Speed (May 17-21)** -- Commits `c1c1347cb` through `f6e6e127c`

The security fix (`c1c1347cb`) blocking ModeSocialPrivate in party follow is good. The Duo Desync fix (`6f7fcb163`) adds useful early convergence checks but weakens the MatchLabelByID verification from `8c9cb219f`. The final commit (`f6e6e127c`) is a clean targeted fix.

---

## Summary of Findings

### Critical Issues

1. **[LIVE REGRESSION] Cross-guild public matchmaking broken** (`5fcf14fa0` contradicts `87bc4b850`). The `publicGroupID()` helper that nulled GroupID for public modes in matchmaking streams was removed. Players in different guilds are now on separate matchmaking streams for public modes. Fix exists on unmerged branch `fix/party-rtt-convergence`.

2. **[WEAKENED SAFETY] Tracker-only convergence fallback** (`6f7fcb163` weakens `8c9cb219f`). When `MatchLabelByID` fails, `isFollowerInLeaderMatch()` returns `true` based solely on tracker stream convergence. This was the exact failure mode that `8c9cb219f` was created to fix -- tracker convergence before the client completes the join causes false positives and party splits.

### Patterns of Concern

3. **Commit churn on `isLeaderHeadingToSocial`**: Four commits in 3 hours (`727583a00` -> `4d2e15f0a` -> `585b78f2d` -> `e7ec68b80`) moving the same function around, with a bad intermediate state that calls `configureParty` twice. Suggests work was committed incrementally to main without local validation of each step.

4. **Logging stripped then restored**: `20bea2927` removed 13 logger lines from `pollFollowPartyLeader`. `d9d0bec63` restored them. The stripping was not flagged during review, likely because the commit message focused on the feature changes.

5. **The lobbyJoin error-swallowing bug**: `lobbyJoin` returned nil even on failure, present in the codebase throughout the entire audit range. Only fixed in `d9d0bec63` (May 17). Every party-following fix before this date was operating on top of a function that silently discarded join failures.

6. **Duplicate commits**: `5672b6c7e`/`2b2acb961` (LeavePartyStream removal), `487b04970`/`53f6c94a4` (MinCount change), `052be7432`/`9b4e3be1a` (early tracking). Parallel branches with the same fixes, merged independently.

7. **Behavioral changes hidden in chore commits**: `8b83bdd40` ("chore: fix LSP diagnostics") silently removed the `StreamUserKick` logic from `configureParty` -- a behavioral change that affects party member lifecycle, buried in a commit about `interface{}` -> `any`.

### What Went Right

- The infrastructure fixes (Act 1) are excellent: systematic, well-tested, correctly scoped.
- The security fix (`c1c1347cb`) is clean and comes with regression tests.
- The `d9d0bec63` cleanup commit is thorough and addresses the most critical bugs.
- Combat backfill balance enforcement (`c292af7f6`) is well-designed.
- The fresh-start grace period (`03a249078`) addresses a subtle race condition elegantly.

### Where It Went Wrong

The codebase got worse during Act 3 (the "Infinite Matchmaking" arc) despite the intent to fix it. The core problem was that multiple contributors were working on the same code with overlapping scopes, committing partial or duplicate fixes without fully validating intermediate states. The stripped logging and removed safety checks in `20bea2927` created a debugging blind spot that persisted for two weeks. The `lobbyJoin` error-swallowing bug -- present throughout the entire audit range -- meant that every fix was building on top of a function that silently lied about success.

The GroupID saga (Act 4) shows a fix being introduced correctly, extracted into a helper, then removed entirely, with the correct resolution sitting on an unmerged branch. This is a coordination failure -- the person who removed `publicGroupID()` in `5fcf14fa0` either didn't understand its purpose or believed the GroupID was being set to `uuid.Nil` upstream, which it is not.
