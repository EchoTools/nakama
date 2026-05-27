# Party System Strategic Analysis: PR #440 vs Main

Generated: 2026-05-27
Analyst: Claude (Opus 4.6)
Inputs: PR #440 diff, 5 audit reports, rebase decision log, main branch source

---

## 1. What PR #440 Actually Does

### Blast Radius

7 files changed, +54 / -19 lines. The changes fall into two categories:

**Category A: SMELL annotations (comment-only, ~30 lines)**

20+ `SMELL(high)` and `SMELL(critical)` comments added across 7 files. These document:
- TOCTOU windows in tracker reads
- Hardcoded timing constants with no adaptive logic
- Silently swallowed errors (`_ = p.matchmaker.RemovePartyAll(...)`)
- Synchronous tracker assumptions
- Internal pointer exposure without defensive copies

No behavioral change. These are audit annotations.

**Category B: Cross-guild public matchmaking fix (~20 lines of actual code)**

The `publicGroupID()` helper (introduced in `87bc4b850`, then removed in `5fcf14fa0`) is re-implemented inline. In 5 locations across 2 files, this pattern is repeated:

```go
groupID := p.GroupID
if p.Mode == evr.ModeArenaPublic || p.Mode == evr.ModeCombatPublic || p.Mode == evr.ModeSocialPublic {
    groupID = uuid.Nil
}
```

Applied to: `BackfillSearchQuery`, `MatchmakingParameters` (properties + query), `MatchmakingStream`, `GuildGroupStream`, and `newLobby` (match settings).

**Category C: Minor cleanup (~4 lines)**

- Variable shadowing fix: `for _, memberSessionIDs := range memberSessionIDs` renamed to `for _, sid := range memberSessionIDs`
- 3 comment deletions (existing comments removed, no new behavioral comments)

### What It Does NOT Do

- No new types, structs, or interfaces
- No state machine, enum, or transition model
- No changes to `pollFollowPartyLeader`, `TryFollowPartyLeader`, `isFollowerInLeaderMatch`, `configureParty`, or `isLeaderHeadingToSocial`
- No changes to timing constants, retry logic, or convergence strategies
- No changes to error handling (except documenting existing suppression)
- No new tests

---

## 2. Bug-by-Bug Assessment

### Bug 1: `maxNonJoinableCycles = 1` (silently reverted from 3)

**Status on main:** Confirmed. Line 1227: `const maxNonJoinableCycles = 1`. PR #446's fix to 3 was overwritten during a refactor.

**Does #440 fix it?** NO. The value remains 1. The PR does not touch this constant. The SMELL comment at line 1240 about the 3s poll interval is adjacent but does not address the cycle count.

### Bug 2: Matchmaking stream guard removed from poll loop

**Status on main:** Confirmed. `pollFollowPartyLeader` does not check whether the leader is still matchmaking before reading the service stream. The check exists in `TryFollowPartyLeader` (line ~1040) but not in the poll retry loop.

**Does #440 fix it?** NO. The PR adds a SMELL comment at line 1040 (`SMELL(critical): TOCTOU window`) documenting the problem in `TryFollowPartyLeader`, but does not restore the guard in `pollFollowPartyLeader`.

### Bug 3: `configureParty` stream validation removed (phantom party members)

**Status on main:** Confirmed. The `StreamUserKick` block that validated party members against the matchmaking stream was removed in `8b83bdd40` (buried in a "chore: fix LSP diagnostics" commit).

**Does #440 fix it?** NO. The PR does not touch `configureParty` except to add a SMELL comment about the 30s timeout.

### Bug 4: `pollFollowPartyLeader` mode check inconsistency

**Status on main:** PARTIALLY RESOLVED since audit. On current main (HEAD), both `TryFollowPartyLeader` (line ~1136) and `pollFollowPartyLeader` (line ~1341) use the same three-mode switch: `case evr.ModeSocialPublic, evr.ModeCombatPublic, evr.ModeArenaPublic`. `ModeSocialPrivate` is excluded from both. The audit reports reference an older state where `pollFollowPartyLeader` still included `ModeSocialPrivate` -- that was fixed by a subsequent commit.

**Does #440 fix it?** N/A -- the bug was already fixed on main before the rebase.

### Bug 5: Released follower `lobbyGroup` not nilled

**Status on main:** FIXED. Main at lines ~170-176 correctly nils `lobbyGroup` for non-social released followers:
```go
if !shouldFollowerFindOrCreateSocial(lobbyParams.Mode) {
    lobbyParams.SetPartySize(1)
    lobbyGroup = nil
}
```

**Does #440 fix it?** N/A -- already fixed on main. The rebase decisions document (Conflict 3) correctly chose to keep main's version over PR #440's unconditional `SetPartySize(1)`.

### Bug 6: `isFollowerInLeaderMatch` returns true on error (false convergence)

**Status on main:** Confirmed. Lines ~1211-1217: when `MatchLabelByID` fails, the function falls through to `return true`, declaring convergence without verification. PR #446's tightening (return false on error) was overwritten.

**Does #440 fix it?** NO. The PR does not touch `isFollowerInLeaderMatch`.

### Bug 7: Two competing convergence strategies in same path

**Status on main:** Confirmed. The poll loop in `pollFollowPartyLeader` contains a `shouldFollowerFindOrCreateSocial` check (line ~1350 in the audit, though exact location varies) that returns false after confirming the leader's match has open slots, deliberately avoiding the join to force the follower through `lobbyFindOrCreateSocial` instead. This creates a dead path where the poll succeeds but refuses to act.

**Does #440 fix it?** NO. The PR does not modify the poll loop logic.

### Bug 8: `publicGroupID()` removed -- cross-guild public matchmaking broken

**Status on main:** Confirmed. `MatchmakingStream()` returns `PresenceStream{Mode: StreamModeMatchmaking, Subject: p.GroupID}` -- players in different guilds are on different matchmaking streams for public modes. `GuildGroupStream()` has the same problem. `newLobby` creates matches with guild-specific GroupIDs. `BackfillSearchQuery` and `MatchmakingParameters` filter by guild-specific GroupID.

**Does #440 fix it?** YES. This is the single behavioral fix in the PR. The inline `groupID = uuid.Nil` override is applied to all 5 affected locations: `BackfillSearchQuery`, `MatchmakingParameters` (properties + query), `MatchmakingStream`, `GuildGroupStream`, and `newLobby`. Public mode players will share a single matchmaking pool regardless of guild.

---

### Bug Fix Summary

| # | Bug | #440 Fixes? | Notes |
|---|-----|-------------|-------|
| 1 | maxNonJoinableCycles = 1 | NO | Not touched |
| 2 | Matchmaking stream guard removed | NO | SMELL comment added, guard not restored |
| 3 | configureParty stream validation removed | NO | Not touched |
| 4 | pollFollowPartyLeader mode check inconsistency | N/A | Already fixed on main |
| 5 | Released follower lobbyGroup not nilled | N/A | Already fixed on main |
| 6 | isFollowerInLeaderMatch false convergence | NO | Not touched |
| 7 | Two competing convergence strategies | NO | Not touched |
| 8 | publicGroupID removed | YES | Full fix across all 5 locations |

**Score: 1 out of 6 remaining bugs fixed.**

---

## 3. The Critical Question: States or If-Statements?

**PR #440 does not think in states. It does not think at all -- it annotates.**

There is no state machine, no state enum, no transition table, no typed result from `TryFollowPartyLeader`, no `FollowResult{Joined, FallbackToSocial, FallbackToMatchmaking, Failed}`. The PR adds SMELL comments describing problems with the existing if-statement-driven architecture but does not change that architecture.

The party system on both main and PR #440 operates through:

1. **Implicit state via tracker streams.** A player's "state" is inferred by checking which tracker streams they have presence on (matchmaking stream, service stream, party stream). There is no single authoritative state field.

2. **Boolean returns from functions.** `TryFollowPartyLeader` returns `bool`. `pollFollowPartyLeader` returns `bool`. The caller infers what happened from the return value combined with side-effect mutations to `params.Mode`.

3. **Nested if-else chains.** The follower dispatch in `lobbyFind` (lines ~95-190) is a 95-line nested conditional that determines behavior through layered if-checks on `isLeader`, `shouldFollowerFindOrCreateSocial`, `headingToSocial`, poll results, and context cancellation.

4. **Side-effect mutation as communication.** `TryFollowPartyLeader` mutates `params.Mode` to signal "force to social" to the caller. The caller must re-check the mode after the function returns to detect this hidden state change.

PR #440 is architecturally identical to main. It is the same if-statement-driven design with 20+ warning labels attached.

---

## 4. Strategic Recommendation

### Path A: Adopt PR #440

**Confidence: 3/10**

**Pros:**
- Fixes Bug #8 (cross-guild matchmaking) -- the only live regression that affects matchmaking pool correctness
- SMELL annotations are accurate and useful as documentation
- Variable shadowing fix is correct
- Rebase was done carefully with good conflict resolution decisions

**Cons:**
- Fixes 1 of 6 remaining bugs
- No architectural improvement whatsoever
- The SMELL comments have zero runtime impact -- they are documentation, not code
- The GroupID fix is 20 lines of duplicated inline code that should be a helper function (the same pattern `publicGroupID()` was extracted into, then removed, now re-inlined)
- Merging a 7-file PR to fix one bug creates the illusion of progress without substance
- The commit message "refactor: party matchmaking overhaul" is misleading -- this is not a refactor and not an overhaul

**Risks:**
- Creates false confidence that the party system has been meaningfully improved
- Future developers may read the SMELL comments and assume the problems are being tracked/addressed, reducing urgency to fix them
- The inline GroupID pattern (repeated 5 times) will drift if modes are added

**Verdict:** Do not merge this PR as-is. Extract the GroupID fix as a standalone targeted commit.

### Path B: Patch Main

**Confidence: 6/10**

**Pros:**
- Each bug gets a focused, reviewable fix
- No risk of importing unrelated changes
- Matches the existing commit style (the good commits in the history -- `d9d0bec63`, `c1c1347cb`, `03a249078` -- are all targeted fixes)
- Can prioritize: Bug #8 (matchmaking broken) and Bug #1 (cycles=1) are 1-line fixes; Bug #6 (false convergence) is a 3-line fix

**Cons:**
- Continues the pattern of incremental patches that created the 8 bugs in the first place
- The if-statement architecture means each patch interacts with every other patch through implicit state
- Bug #2 (matchmaking stream guard) and Bug #3 (stream validation) require understanding the full poll loop to restore correctly -- they are not simple patches
- Bug #7 (competing convergence strategies) cannot be fixed with a patch -- it requires a design decision about which strategy wins

**Risks:**
- Patch #N+1 may re-break what Patch #N fixed, as happened repeatedly in the commit history
- Without architectural guardrails, correctness depends entirely on the patch author understanding all 1376 lines of `evr_lobby_find.go`
- The audit reports themselves become stale as patches land

**Verdict:** Viable for the 3 simple bugs (8, 1, 6). Insufficient for bugs 2, 3, 7.

### Path C: Hybrid (Extract #440's ideas into targeted patches)

**Confidence: 5/10**

**Pros:**
- Gets the GroupID fix (Bug #8) immediately
- The SMELL annotations could go in as a separate documentation commit
- Avoids merging a misleadingly-named PR

**Cons:**
- #440's "ideas" are limited to: one bug fix, one variable rename, and documentation comments. There is nothing architectural to extract.
- This path is functionally identical to Path B with the GroupID fix prioritized first

**Risks:** Same as Path B.

**Verdict:** This is Path B with extra steps. The PR does not contain structural innovations worth extracting separately.

### Path D: State Machine First (Issue #456)

**Confidence: 8/10**

**Pros:**
- Addresses the root cause: the party system has no explicit state model, which is why 16+ incremental patches created 8 bugs
- A state machine makes illegal transitions unrepresentable. You cannot have "released follower with non-nil lobbyGroup" if the state machine enforces that release transitions nil the group
- Convergence strategy becomes a property of the state, not a runtime if-check. Social-mode followers are in a different state than arena followers, with different legal transitions
- `TryFollowPartyLeader` returning a typed enum (`FollowResult`) instead of `bool` + side-effect mutation eliminates the "hidden state change" anti-pattern that caused Bug #7
- Makes the system testable: you can test state transitions in isolation without standing up the full tracker/matchmaker infrastructure
- The 5 audit reports become the specification: each bug maps to a missing state guard or illegal transition

**Cons:**
- Largest upfront investment (~2-5 days of focused work)
- Requires touching `lobbyFind`, `TryFollowPartyLeader`, `pollFollowPartyLeader`, `configureParty`, and `isFollowerInLeaderMatch` -- the same code that 16 PRs have been fighting over
- Risk of introducing new bugs during the structural change
- Requires a clear specification before writing code (Issue #456)

**Risks:**
- If done poorly, the state machine becomes a wrapper around the same if-statements
- If done incrementally (states for part of the flow, if-statements for the rest), the boundary between the two systems becomes a new source of bugs
- Requires buy-in from all contributors to maintain the state model going forward

**Verdict:** This is the correct long-term path. The existing architecture has demonstrated, through 16 PRs and 8 bugs, that implicit state managed through tracker streams and boolean returns cannot maintain correctness under concurrent development. The state machine does not need to be elaborate -- a simple enum with a transition function and an assertion on every state read would prevent the majority of these bugs.

---

## 5. Recommended Approach

**Immediate (this week):**

1. Do NOT merge PR #440 as-is. The commit message claims "overhaul" but delivers one bug fix and comments.

2. Cherry-pick the GroupID fix (Bug #8) as a standalone commit. Extract the inline pattern into a `publicGroupID()` method on `*LobbySessionParameters` (as `87bc4b850` originally did) instead of duplicating the mode check 5 times. One line to maintain instead of five.

3. Fix Bug #1 (`maxNonJoinableCycles = 3`) in a one-line commit. This was already validated on the side branch.

4. Fix Bug #6 (`isFollowerInLeaderMatch` return false on error) in a 3-line commit. PR #446's version was correct.

**Short-term (next 2 weeks):**

5. Restore Bug #2 (matchmaking stream guard in poll loop) with a targeted commit and test.

6. Design the state machine (Issue #456). The specification should define:
   - Every legal player state (Idle, Matchmaking, Following, InMatch, Released, etc.)
   - Every legal transition and what triggers it
   - What data is valid in each state (e.g., `lobbyGroup` is non-nil only in Following and Matchmaking states)
   - Which convergence strategy applies to each state

**Medium-term (next sprint):**

7. Implement the state machine. Start with the follower path (it has the most bugs). The leader path is simpler and can follow.

8. Bug #3 (stream validation), Bug #7 (competing strategies), and the remaining TOCTOU races should be fixed as part of the state machine implementation, not as patches on top of the current architecture.

---

## 6. Honest Assessment

PR #440 is not a party matchmaking overhaul. It is one bug fix, one variable rename, and 20+ documentation comments packaged under a misleading name. The SMELL annotations are accurate observations, but observations are not fixes. The PR does not introduce states, does not resolve the architectural problems that caused the 8 bugs, and does not change the system's behavior except for the GroupID fix.

The current main branch is not unfixable, but it is unmaintainable. The 95-line nested conditional in `lobbyFind` that dispatches follower behavior through implicit state checks, boolean returns, and side-effect mutations will continue to break under any non-trivial change. The 16 PRs that created the 8 bugs were not written by incompetent people -- they were written by competent people working on a system that makes correctness invisible. The if-statement architecture does not fail loudly; it fails silently, through stale tracker reads and unchecked boolean returns, and the failures only manifest at runtime under specific timing conditions.

The state machine (Path D) is the only approach that changes this dynamic. Everything else is another round of patches on a system that has demonstrated it cannot hold patches.
