# Party Behavioral Test Architecture

## Problem Statement

The party system has a persistent bug: party members get separated during matchmaking. 16+ PRs have attempted fixes over 6 weeks. The existing 2,484 lines of tests across 5 files verify **PartyHandler mechanics** (join, leave, promote, matchmaker add/remove) and **follow path edge cases** (TryFollowPartyLeader early returns, pollFollowPartyLeader branch coverage, matchmaking monitor races). None of these tests verify the user-facing invariant: **if you are in a party, you play together on the same team.**

### Production Failure Data (May 25-26)

| Failure Class | Count | Path |
|---|---|---|
| "failed to join match" (lobby full) | 1,404 | Leader placed, follower retries 3x (~18s), released solo |
| Followers released to independent matchmaking | 64 | pollFollowPartyLeader returns false |
| Failed joins to leader's lobby | 128 | lobbyJoin returns ServerIsFull |
| Leader in non-joinable mode | 72 | label.Mode switch rejects follow |
| Repaired split party team assignment | 7 | repairSplitTicketTeams post-hoc swap |

### What Existing Tests Cover (and Do Not Cover)

**Covered:**
- PartyHandler join/leave/promote/close mechanics (`evr_party_system_test.go`)
- TryFollowPartyLeader: every early-return guard (nil leader, self-is-leader, leader matchmaking, follower already in match, leader no match, empty match ID, different match triggers label lookup)
- pollFollowPartyLeader: leader disappears, follower becomes leader, leader matchmaking then settles, leader left match, nil match ID, leader switches matches, context timeout, concurrent updates
- Duo desync: matchmaking monitor cancels follower context during poll
- repairSplitTicketTeams: split party repair, naturally aligned parties unchanged
- SMELL regressions: deferred Untrack TOCTOU, shouldFollowerFindOrCreateSocial boundary

**Not Covered (the gap):**
- End-to-end: party forms, enters matchmaking, all members arrive in same match
- Lobby full: matchmaker places leader but match fills before follower joins
- Crash/reconnect: member crashes, reconnects, party state preserved
- Staggered join: leader enters matchmaking before follower joins runtime party (the original #406 root cause, addressed only by `MatchmakingStartGracePeriod`)
- Team integrity: party members assigned to same team, not split across teams, through the full `buildMatch` -> `repairSplitTicketTeams` path with realistic multi-party scenarios
- Private mode mismatch: leader enters `ModeSocialPrivate`, follower blocked by mode switch

---

## Test 1: Core Invariant — Party Members Converge to Same Match, Same Team

### Scenario

1. Create a party of 2 (leader + follower).
2. Leader calls `lobbyFind` with `ModeArenaPublic`.
3. Follower calls `lobbyFind` with `ModeArenaPublic` and same `PartyGroupName`.
4. The matchmaker runs and produces a match with both players in the entrants list.
5. `buildMatch` splits entrants into teams.
6. Both players are placed into the match via `LobbyJoinEntrants`.
7. Assert: both players are in the same match (service stream presence).
8. Assert: both players are on the same team (team index in match presences).

### Acceptance Criteria

- **PASS**: Leader and follower service stream presences point to the same MatchID, AND both have the same TeamIndex in the match state.
- **FAIL**: Different MatchIDs, or same MatchID but different TeamIndex.

### Interface Requirements

This test requires a level of integration above the existing test infrastructure. The current `createLightMatchmaker` creates a `LocalMatchmaker` that supports `Add`/`Remove` but does not run the matchmaker processing loop (`Process`). The test needs:

1. **Mock Matchmaker with Process**: Extend `createLightMatchmaker` or create a `matchmakerWithProcess` that can accept tickets and produce matches. Alternatively, bypass the matchmaker entirely: submit a pre-formed entrants list directly to `buildMatch`.

2. **Mock Match Registry with JoinAttempt**: The existing `mockFollowMatchRegistry` implements `GetMatch` but panics on `JoinAttempt`, `Join`, `Leave`. Extend it to accept `JoinAttempt` calls and track which sessions joined which matches.

3. **Mock Session Registry**: `PrepareEntrantPresences` calls `sessionRegistry.Get(sessionID)`. Need a mock that returns minimal `Session` objects for each party member.

4. **Team Verification**: After `buildMatch` runs, inspect the resulting `[2][]*MatchmakerEntry` teams array to verify both party members are on the same team. This can be done by checking that all entries with the party's ticket are in the same team index.

**Simplest viable approach**: Test at the `buildMatch` level. Construct a `[]*MatchmakerEntry` where two entries share the same party ticket (simulating the matchmaker's output), call `buildMatch` or the team-splitting logic, and verify team co-location. This bypasses the full `lobbyFind` pipeline but tests the critical path from matchmaker output to team assignment.

### Why This Matters

This is the fundamental invariant. Every production failure is a violation of it. Without this test, individual fix PRs cannot prove they restore the invariant; they can only prove they change a specific code path.

### Estimated Complexity

**Medium-High.** The "simple" version (test at `buildMatch` level with pre-formed entrants) is straightforward — the existing `TestBuildMatch_PartiesStayTogether` is close but does not go through `repairSplitTicketTeams` in a scenario where the matchmaker has actually split the party. A full integration version (two sessions through `lobbyFind` to match placement) requires significant new infrastructure: mock session registry, mock match registry with join semantics, mock matchmaker processing loop.

---

## Test 2: Lobby Full — Follower Released When Leader's Match Fills

### Scenario

1. Create a party of 2 (leader + follower).
2. Leader calls `lobbyFind`. Matchmaker places leader into Match A.
3. Match A fills to capacity (other solo players join) before the follower's follow attempt.
4. Follower enters `TryFollowPartyLeader`, calls `MatchLabelByID`, sees `label.OpenPlayerSlots() < requiredSlots`.
5. Follower enters `pollFollowPartyLeader`, retries, same result.
6. After `maxNonJoinableCycles` (currently 1), `pollFollowPartyLeader` returns false.
7. Follower falls through to independent matchmaking (`lobbyParams.SetPartySize(1)`, `lobbyGroup = nil`).
8. Assert: follower enters matchmaking as solo, NOT as party member.
9. Assert: follower eventually lands in a DIFFERENT match from the leader.

### Acceptance Criteria

- **PASS**: After `pollFollowPartyLeader` returns false, `lobbyParams.GetPartySize() == 1` and `lobbyGroup` is nil. The follower does not re-enter party matchmaking. The follower is placed in a match (any match).
- **FAIL**: Follower hangs indefinitely in poll loop, or follower re-enters matchmaking with party size > 1 (which would block backfill), or follower is never placed.

### Interface Requirements

1. **Mock Match Registry**: `GetMatch` returns a `MatchLabel` with `Open: true` but `OpenPlayerSlots() == 0` (or `< requiredSlots`). This triggers the "match is full" path.

2. **Existing `followTestEnv`**: Can be reused. Set leader's service stream to Match A. Provide a mock registry where Match A is full. Run `TryFollowPartyLeader` and verify it returns false. Then verify `pollFollowPartyLeader` returns false after the non-joinable cycle limit.

3. **State Verification After Release**: After the follow path returns false, the calling code in `lobbyFind` (lines 166-178) sets `lobbyParams.SetPartySize(1)` and `lobbyGroup = nil`. Since this is in `lobbyFind` itself (not in the follow functions), testing this requires either:
   - A higher-level test that exercises `lobbyFind` directly (harder, needs full pipeline).
   - Or: a contract test that verifies `TryFollowPartyLeader` returns false when the match is full AND `pollFollowPartyLeader` returns false after the cycle limit. The caller's behavior is then verified by code inspection or a separate unit test of lines 166-178.

### Why This Matters

This is the **dominant production failure** (1,404 of 1,404 "failed to join match" errors). The follower spends ~18s in retry cycles before being released solo. The test ensures the release-to-solo path actually works and does not leave the follower stuck.

### Estimated Complexity

**Medium.** The existing `mockFollowMatchRegistry` and `followTestEnv` provide most of the infrastructure. The main new work is:
- Setting up a match label where `OpenPlayerSlots() < requiredSlots`.
- Verifying the `maxNonJoinableCycles` limit triggers correctly.
- Optionally testing the `lobbyFind` caller behavior after the false return.

---

## Test 3: Crash/Reconnect — Party State Preserved

### Scenario

1. Create a party of 3 (leader + 2 followers).
2. All 3 are in Match A.
3. Follower 1 disconnects (session closed, presence removed).
4. Follower 1 reconnects with a new session ID.
5. Follower 1 rejoins the party (via `JoinPartyGroup` with same `PartyGroupName`).
6. Assert: party now has 3 members (leader + follower 2 + reconnected follower 1).
7. Assert: party leader is unchanged.
8. Leader queues for next match. Follower 1 is included in the party ticket.

### Acceptance Criteria

- **PASS**: After reconnect, `lobbyGroup.Size() == 3`, leader is unchanged, and `MatchmakerAdd` with the party includes all 3 session IDs.
- **FAIL**: Party size is wrong (follower 1's old session still occupies a slot), or party is stopped, or leader has changed, or reconnected follower 1 is not in the matchmaker ticket.

### Interface Requirements

1. **PartyHandler Leave + Rejoin**: Simulate disconnect by calling `ph.Leave([]*Presence{follower1OldPresence})`, then rejoin with a new presence (new session ID, same user ID) via `ph.Join([]*Presence{follower1NewPresence})`.

2. **PartyPresenceList Dedup**: Verify that the `PartyPresenceList` does not allow duplicate user IDs (old session + new session for the same user).

3. **JoinPartyGroup Integration**: The reconnect path goes through `JoinPartyGroup`, which calls `partyRegistry.PartyJoinRequest`. Need to test that a user who was previously in the party can rejoin after disconnect. This may require a `LocalPartyRegistry` (available via `createLightPartyHandler`).

4. **MatchmakerAdd Verification**: After rejoin, call `ph.MatchmakerAdd` and verify the returned ticket includes all current members.

### Why This Matters

Crashes and reconnects are common in VR games. If the party state is corrupted on reconnect (e.g., the old session still holds a slot, or the party is stopped), the reconnected player will be solo-queued. This is not currently covered by any test.

### Estimated Complexity

**Medium.** The `createLightPartyHandler` infrastructure handles party creation and membership. The main unknowns are:
- Whether `PartyPresenceList` handles duplicate user IDs correctly (same user, different session).
- Whether `LocalPartyRegistry` supports a user leaving and rejoining with a new session.
- Whether the matchmaker ticket correctly reflects the new session after rejoin.

---

## Test 4: Leader Transition During Poll — Leader Leaves Match While Follower Polling

### Scenario

1. Create a party of 2 (leader + follower).
2. Leader is in Match A. Follower enters `pollFollowPartyLeader`.
3. Between poll cycles, leader leaves Match A (service stream removed or updated to empty).
4. `pollFollowPartyLeader` detects `presence == nil` on the next cycle.
5. Returns false.
6. Follower falls back. Assert: follower does NOT hang; returns within 2 poll cycles.

#### Sub-scenario: Leader Transitions to New Match

1. Same setup, but instead of leaving, leader transitions from Match A to Match B.
2. `pollFollowPartyLeader` sees the leader's match change.
3. Follower should attempt to join Match B, not remain stuck waiting for Match A.

### Acceptance Criteria

- **PASS (leader leaves)**: `pollFollowPartyLeader` returns false within 2 poll cycles (~6-10s) after leader's service stream disappears.
- **PASS (leader transitions)**: `pollFollowPartyLeader` detects the new match ID and either joins it or returns false. Does NOT loop forever on the old match ID.
- **FAIL**: Poll hangs, or poll returns true despite follower not being in any match, or follower attempts to join the old match after leader has moved.

### Interface Requirements

The existing test infrastructure already covers most of this:
- `TestPoll_LeaderDisappears_ReturnsFalse` covers leader vanishing.
- `TestPoll_LeaderSwitchesMatches_FollowerInNewMatch_ReturnsTrue` covers leader transitioning.

**What is NOT covered**: The gap between "leader leaves match" and "context cancellation" — specifically the case where the leader's `lobbyFind` defer fires (Untrack matchmaking stream), causing the follower's matchmaking monitor to cancel the context. This is the **C1 SMELL race**. The test `TestDuoDesync_MonitorCancelsFollowerContext_DuringPoll` covers this for the matchmaking stream removal case, but NOT for the service stream transition case where the leader moves between matches during a game (not matchmaking).

**New test needed**: Leader is in Match A (not matchmaking), follower is polling. Leader transitions to Match B. Between the two poll cycle wakeups, the leader's presence briefly shows no match (the transition gap). The poll should handle this without returning false.

### Why This Matters

72 production failures were "leader in non-joinable mode" which includes transient states during match transitions. The 3-second poll interval creates a window where the leader's state can change between reads.

### Estimated Complexity

**Low-Medium.** Mostly leverages existing `followTestEnv`. The new test is a timing variant of `TestPoll_LeaderSwitchesMatches` where the leader briefly has no service stream between Match A and Match B.

---

## Test 5: Private Mode Mismatch — Leader Enters Private, Follower Should Not Follow

### Scenario

1. Create a party of 2.
2. Leader enters `ModeSocialPrivate` match.
3. Follower calls `lobbyFind` with `ModeArenaPublic`.
4. `TryFollowPartyLeader` calls `MatchLabelByID`, gets `label.Mode == ModeSocialPrivate`.
5. The mode switch (line 1136-1143) hits the `default` case and returns false.
6. Follower falls through to normal matchmaking.

#### Sub-scenario: Leader enters `ModeArenaPrivate`

Same as above but with `ModeArenaPrivate`. Same expected behavior.

### Acceptance Criteria

- **PASS**: `TryFollowPartyLeader` returns false when the leader's match mode is `ModeSocialPrivate`, `ModeArenaPrivate`, `ModeArenaTournment`, or `ModeCombatPrivate`. Follower proceeds to normal matchmaking.
- **FAIL**: Follower attempts to join the private match, or hangs in `pollFollowPartyLeader`.

### Interface Requirements

1. **Mock Match Registry**: Return a `MatchLabel` with `Mode: evr.ModeSocialPrivate` (or other private modes).
2. **Existing `followTestEnv` + `mockFollowMatchRegistry`**: Set leader's match to a private-mode label.
3. **Mode Enumeration**: Test ALL non-joinable modes to prevent regression if new modes are added.

### Why This Matters

72 production failures were "leader in non-joinable mode". While the mode switch exists in the code, there is no test verifying it for each private mode. A regression here would silently allow party follows into private matches.

### Estimated Complexity

**Low.** The existing infrastructure handles this. It is a parameterized version of `TestTryFollow_FollowerInDifferentMatch_AttemptsJoin` but with non-joinable mode labels. Can be a table-driven test.

---

## Test 6: Staggered Join — Leader Enters Matchmaking Before Follower Joins Runtime Party

### Scenario

This is the original #406 root cause. The `MatchmakingStartGracePeriod` (3 seconds) was added to mitigate it.

1. Create a party group name. Leader's client calls `lobbyFind` first.
2. `configureParty` → `JoinPartyGroup` → leader is alone in the party (`lobbyGroup.Size() == 1`).
3. Leader enters `graceWaitLoop` (lines 394-413): waits up to `MatchmakingStartGracePeriod` for followers to join.
4. **Case A (grace works)**: Follower joins the party during the grace period. `lobbyGroup.Size()` becomes 2. Leader breaks out of grace loop and submits a party ticket for 2 players.
5. **Case B (grace fails)**: Follower does NOT join within 3 seconds (e.g., slow client, network latency). Leader submits a solo ticket. Follower joins the party later, but the leader's ticket is already submitted.

### Acceptance Criteria

- **Case A PASS**: Leader waits for follower, then submits ticket with `partySize == 2`. Both are in the ticket.
- **Case A FAIL**: Leader submits solo ticket despite follower joining during grace period.
- **Case B PASS**: Leader submits solo ticket after grace expires. Follower, arriving later, enters the follow path (`TryFollowPartyLeader` / `pollFollowPartyLeader`). Follower eventually joins the leader's match OR is released to independent matchmaking. Neither player hangs.
- **Case B FAIL**: Follower enters matchmaking with a party ticket that conflicts with the leader's solo ticket, or follower is silently dropped.

### Interface Requirements

1. **`configureParty` in Isolation**: The grace loop is inside `configureParty`. Testing it requires calling `configureParty` with a `LobbyGroup` whose `Size()` changes during the wait. This means running `configureParty` in a goroutine and having another goroutine call `ph.Join` on the underlying `PartyHandler` after a delay.

2. **Timing Control**: The grace period is 3 seconds. The test needs to control when the follower joins:
   - Before 3s (Case A): Use a goroutine with `time.Sleep(1 * time.Second)` then `ph.Join`.
   - After 3s (Case B): Use a goroutine with `time.Sleep(4 * time.Second)` then `ph.Join`.

3. **Ticket Verification**: After `configureParty` returns, verify `lobbyParams.GetPartySize()` and the returned `memberSessionIDs` slice.

4. **Full Pipeline Requirements for Case B**: Case B requires testing the follower's entry into `lobbyFind` AFTER the leader has already submitted a solo ticket. This needs a more complete pipeline mock (session registry, tracker, etc.).

### Why This Matters

This was the original root cause of the party separation bug (#406). The 3-second grace period is a timing-based mitigation, not a guarantee. If the grace period is too short (e.g., VR clients on slow networks), the bug recurs. This test documents the exact timing boundary.

### Estimated Complexity

**High.** Case A (grace works) is testable with the existing `createLightPartyHandler` + goroutine timing. Case B (grace fails, follower enters follow path) requires exercising `lobbyFind`'s post-`configureParty` logic, which needs a more complete mock pipeline than exists today. The main cost is building a `configureParty`-level test harness that can control `lobbyGroup.Size()` timing.

---

## Test 7: Party Team Assignment — Members on Same Party End Up on Same Team

### Scenario

1. Matchmaker produces 8 entrants: 1 party of 2 + 6 solo players.
2. Matchmaker's output array has the party members split across teams (the initial array ordering places one in each half).
3. `buildMatch` calls `repairSplitTicketTeams`.
4. After repair, both party members are on the same team.
5. Assert: all entries sharing the party ticket are in the same team index.

#### Sub-scenario: Two Parties

1. Matchmaker produces 8 entrants: 2 parties of 2 + 4 solo players.
2. Party A members split across teams. Party B already together.
3. After repair, both parties are intact.

#### Sub-scenario: Large Party (3+)

1. Matchmaker produces 10 entrants: 1 party of 3 + 7 solo players.
2. Party members are in positions 3, 5, 7 (split across both halves).
3. After repair, all 3 are on the same team.

#### Sub-scenario: Impossible Repair

1. Matchmaker produces 8 entrants: 1 party of 5. Teams are 4v4.
2. Party of 5 cannot fit on one team of 4.
3. `repairSplitTicketTeams` returns an error.
4. Assert: error is returned, match is not created with split party.

### Acceptance Criteria

- **PASS**: After `repairSplitTicketTeams`, all entries sharing a ticket are in the same team index. Team sizes remain equal.
- **FAIL**: Party members are on different teams, or team sizes are unbalanced, or the repair silently succeeds when it should have errored.

### Interface Requirements

1. **Direct `repairSplitTicketTeams` Testing**: This function is already tested in `evr_lobby_builder_team_assignment_test.go`, but only for the 2-party case and the already-aligned case. New test cases needed:
   - Large party (3+) split across teams.
   - Two parties both split.
   - Impossible repair (party larger than team size).
   - Party of 1 (should be no-op).

2. **`buildMatch` Integration**: Test that `buildMatch` actually calls `repairSplitTicketTeams` and that the repaired teams are used for `EvrMatchPresence` construction. This is the integration layer above the unit test.

3. **Entrant Construction**: Helper to create `[]*MatchmakerEntry` with controlled ticket assignments (party members sharing tickets, solo players with unique tickets). The existing `testMatchmakerEntryWithTicket` helper handles this.

### Why This Matters

7 production "repaired split party team assignment" events means the repair is firing in production. The repair is a post-hoc fix. If it fails (e.g., impossible case, or a bug in the swap logic), party members play against each other. The existing tests only cover 2 of the needed scenarios.

### Estimated Complexity

**Low.** The infrastructure exists (`testMatchmakerEntryWithTicket`, `teamIndexesForTicket`). This is table-driven test expansion of `evr_lobby_builder_team_assignment_test.go`. The `buildMatch` integration layer is slightly harder (needs a mock `LobbyBuilder` with session/match registry), but the unit tests on `repairSplitTicketTeams` alone cover the critical path.

---

## Infrastructure Requirements Summary

### Already Available

| Component | Location | What It Provides |
|---|---|---|
| `createLightMatchmaker` | `evr_party_system_test.go` | LocalMatchmaker with Add/Remove |
| `createLightPartyHandler` | `evr_party_system_test.go` | PartyHandler with party registry, tracker, stream manager |
| `followTestEnv` | `evr_lobby_follow_test.go` | Duo party test env with mock tracker |
| `mockMatchmakingTracker` | `evr_lobby_matchmaking_monitor_test.go` | Controllable tracker for presence/stream testing |
| `mockFollowMatchRegistry` | `evr_lobby_follow_test.go` | Mock MatchRegistry with GetMatch |
| `testMatchmakerEntryWithTicket` | `evr_lobby_builder_team_assignment_test.go` | Entrant construction helper |
| `MonitorMatchmakingStreamV2` | `evr_lobby_matchmaking_monitor_test.go` | Refactored monitor for testing |

### Needs to Be Built

| Component | Needed By Tests | Description |
|---|---|---|
| `mockFollowMatchRegistry` with `JoinAttempt` | 1, 2 | Extend existing mock to accept join attempts and track accepted/rejected sessions |
| Mock `SessionRegistry` | 1, 6 | Return minimal `Session` objects for `PrepareEntrantPresences` |
| `configureParty` test harness | 6 | Run `configureParty` with controllable `LobbyGroup.Size()` timing |
| `followTestEnv` for 3+ party | 3, 4 | Extend `newFollowTestEnv` to support N-member parties |
| `buildMatch` integration harness | 1 (full version) | Mock `LobbyBuilder` that can call `buildMatch` and return team assignments |

---

## Implementation Priority and Sequencing

```
Priority 1 (highest value, lowest cost):
  Test 7 — Party Team Assignment        [LOW complexity, covers repairSplitTicketTeams gaps]
  Test 5 — Private Mode Mismatch        [LOW complexity, covers 72 production failures]

Priority 2 (high value, medium cost):
  Test 2 — Lobby Full                   [MEDIUM complexity, covers 1,404 production failures]
  Test 4 — Leader Transition During Poll [LOW-MEDIUM complexity, covers timing gaps]

Priority 3 (high value, higher cost):
  Test 1 — Core Invariant               [MEDIUM-HIGH complexity, the fundamental invariant]
  Test 3 — Crash/Reconnect              [MEDIUM complexity, uncovered failure mode]

Priority 4 (high value, highest cost):
  Test 6 — Staggered Join               [HIGH complexity, original root cause]
```

The recommended sequencing is:
1. Start with Tests 5 and 7 (low cost, immediate coverage).
2. Then Tests 2 and 4 (medium cost, cover the dominant production failures).
3. Then Test 1 (the invariant test; may require new infrastructure that enables Test 6).
4. Then Tests 3 and 6 (require the most new infrastructure).

Each test can be implemented independently. Tests 1 and 6 share infrastructure (mock session registry, configureParty harness) and should be planned together.
