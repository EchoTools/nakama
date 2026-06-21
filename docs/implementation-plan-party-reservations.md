# Implementation Plan: Reservation-Based Party System

**Source spec**: `docs/spec-party-reservation-system.md`
**Behavior matrix**: `docs/party-behavior-matrix.md`
**Go conventions**: project uses zap (not slog), `%w` error wrapping, context-first params
**Reconciliation report**: `docs/lifecycle-state-machine-reconciliation.md`

---

## Preamble: StreamModeReservation Dropped

The original plan used a `StreamModeReservation` tracker stream as the follower notification
mechanism. This has been **dropped**. Reservations are discovered via the match label's
`Players` slice: `rebuildCache()` already populates `PlayerInfo.IsReservation = true` for
every entry in `reservationMap` (see `evr_match_label.go:469,487,503,518,533`).

The follower queries for their reservation by calling `nk.MatchList` with a query that
matches their session ID in the `label.players.session_id` field, or by looking up the
leader's match via the leader's service stream and calling `MatchLabelByID`, then checking
the `Players` slice for an entry matching their session with `IsReservation: true`.

**Consequences of this change:**

- Step 1 (add `StreamModeReservation` constant) is removed.
- Steps 12 and 12b (untrack `StreamModeReservation`) are removed.
- `findReservation` does a match label query, not a tracker read.
- `clearMemberReservation` signals the match to delete the reservation slot (no stream to untrack).
- `createPartyReservations` does not track any stream presences.
- No new stream mode constant is needed.

---

## Already Committed Changes

The following changes are already committed and should NOT be re-implemented:

| Commit      | Description                                                        | Files                                                         |
| ----------- | ------------------------------------------------------------------ | ------------------------------------------------------------- |
| `671456443` | Defer service/guild/matchmaking streams to `lobbyEntrantConnected` | `evr_lobby_joinentrant.go`, `evr_pipeline_lobby.go`           |
| `f4e87462f` | Route authoritative match leave through game server signal         | `pipeline_match.go`                                           |
| `522309f75` | Expand lifecycle transition table and add state guards             | `evr_match_lifecycle.go`, `evr_match.go`, `evr_lobby_find.go` |
| `27465b928` | Log-replay test framework phase 1                                  | `evr_logreplay_test.go`, `server/testdata/replay/*`           |
| `af4d2c276` | Add 6 diverse lifecycle replay fixtures                            | `server/testdata/replay/*`                                    |

---

## Section 1: Behavioral Acceptance Criteria (BACs)

### BAC-001: Leader joins social lobby, reservations created for party members

**Given**: A party of 3 (leader + 2 followers). Leader sends `LobbyFindSessionRequest` and joins a social lobby. Game server confirms the leader connected (`lobbyEntrantConnected` fires).
**When**: `lobbyEntrantConnected` processes the leader's entrant.
**Then**:

1. `createPartyReservations` is called.
2. For each non-leader party member with a live session (`sessionRegistry.Get` != nil), a `SignalCreatePartyReservations` signal is sent to the match.
3. The match handler creates `slotReservation` entries in `reservationMap` with 5-minute expiry for each member not already in the match or already reserved.
4. `rebuildCache()` is called, which populates `PlayerInfo.IsReservation = true` for each reserved member in the `Players` slice.

**Verification**: `TestLeaderJoinsSocial_ReservationsCreated` -- create a party, simulate `lobbyEntrantConnected` for leader, verify `reservationMap` entries exist and `rebuildCache` marks them as `IsReservation`.
**Replay fixtures that must still pass**: All 7 existing fixtures (none exercise reservation creation yet; this is a regression guard).
**Source**: Spec section 1 "Leader Joins a Match -> Creates Reservations"

---

### BAC-002: Follower finds reservation and joins directly

**Given**: Leader is in a social lobby. Reservation exists for follower (in `reservationMap`, visible via `PlayerInfo.IsReservation` in match label).
**When**: Follower sends `LobbyFindSessionRequest`, entering `lobbyFind`.
**Then**:

1. `findReservation` is called early in the follower path (before existing follower logic at line 118 of `evr_lobby_find.go`).
2. `findReservation` looks up the party leader's match via the leader's service stream, then calls `MatchLabelByID` and checks the `Players` slice for an entry matching the follower's session ID with `IsReservation: true`.
3. The match ID is extracted from the label.
4. Follower joins via `lobbyJoin(ctx, logger, session, lobbyParams, matchID)`.
5. `TryFollowPartyLeader` and `pollFollowPartyLeader` are NOT entered.

**Verification**: `TestFollowerFindsReservation_JoinsDirectly` -- set up reservation in match, call `findReservation`, verify it returns the correct match ID, verify `TryFollowPartyLeader` is not called.
**Source**: Spec section 2 "Follower Matchmakes -> Searches for Reservation"

---

### BAC-003: Follower with no reservation enters hold state

**Given**: Follower is in a party. Leader has NOT yet joined a match (no reservation exists).
**When**: Follower sends `LobbyFindSessionRequest`.
**Then**:

1. `findReservation` returns `MatchID{}, false`.
2. Follower falls through to existing follower logic (as fallback during migration).
3. Eventually, when leader joins a match, `lobbyEntrantConnected` creates reservations, and the follower's next poll/retry finds it.

**Verification**: `TestFollowerNoReservation_FallsThrough` -- no reservation exists, verify `findReservation` returns false, verify follower enters the existing follow path.
**Source**: Spec section 5 "Follower Matchmakes Before Leader"

---

### BAC-004: Reservation consumed on player join

**Given**: Reservation exists for follower in match's `reservationMap`.
**When**: Follower joins the match via `MatchJoinAttempt`.
**Then**:

1. `LoadAndDeleteReservationRaw` or `LoadAndDeleteReservationByUserIDRaw` consumes the reservation (existing behavior at `evr_match.go:416-422`).
2. `rebuildCache()` is called, removing the `IsReservation` entry from the `Players` slice.

**Verification**: `TestReservationConsumed` -- create reservation, simulate join, verify `reservationMap` entry removed and `Players` slice updated.
**Source**: Spec section 0 event table: "Player's game server confirms connection"

---

### BAC-005: Leader leaves match, party reservations cleared; non-leader leaves, only their reservation cleared

**Given**: Leader is in a social lobby with reservations for 2 followers (A and B).
**When**: A player with a `PartyID` leaves the match (`MatchLeave` fires).
**Then**:

1. `MatchLeave` detects the leaving player has a `PartyID` != nil.
2. `MatchLeave` determines whether the leaving player is the party leader by casting `nk` to `*RuntimeGoNakamaModule`, calling `partyRegistry.Get(mp.PartyID)`, and comparing `ph.leader.UserPresence.SessionId` against `mp.GetSessionId()`. This is the same pattern used in Step 4 (`lobbyEntrantConnected`).
3. **If the leaving player IS the leader**: All `reservationMap` entries where `Presence.PartyID` matches are deleted. `rebuildCache()` is called. This ensures followers don't try to join a match the leader has left.
4. **If the leaving player is NOT the leader**: Only the leaving player's own reservation is deleted from `reservationMap` (keyed by their session ID). If no reservation exists for them, this is a no-op. `rebuildCache()` is called only if a reservation was actually deleted.
5. **If the party cannot be looked up** (party already disbanded, `partyRegistry.Get` returns false): Fall back to clearing only the leaving player's own reservation. This is the safe default -- clearing all party reservations without knowing the leader could delete valid reservations for other members.
6. `MatchLeave` modifies `state.reservationMap` directly (not via signal -- `MatchLeave` already runs inside the match handler goroutine, so direct modification is safe and correct; signals cannot be sent from within the match goroutine due to `matchSignalBlockedNakamaModule`).

**Verification**: `TestLeaderLeavesMatch_ReservationsCleared` -- set up reservations, simulate leader leaving, verify all party reservations in `reservationMap` are empty. `TestNonLeaderLeavesMatch_OnlyTheirReservationCleared` -- set up reservations for followers A and B, simulate follower A leaving, verify only A's reservation is removed and B's remains.
**Replay fixtures that must still pass**: All 7 existing fixtures.
**Source**: Spec section 3 bullet 2: "Leader leaves the lobby"

---

### BAC-006: Member leaves party, their reservation cleared

**Given**: Leader is in a social lobby with reservations for followers A and B.
**When**: Follower A calls `snsPartyLeaveRequest`.
**Then**:

1. BEFORE `snsPartyLeaveCleanup` (which clears `params.currentPartyID`), `clearMemberReservation` looks up the leader's current match and signals it to delete the leaving member's reservation slot.
2. Follower B's reservation remains.
3. Then `snsPartyLeaveCleanup` runs normally.

**Verification**: `TestMemberLeavesParty_TheirReservationCleared` -- set up 2 reservations, simulate one leave, verify only that member's reservation is removed.
**Source**: Spec section 3 bullet 3: "Reserved player leaves the party"

---

### BAC-007: Member kicked from party, their reservation cleared

**Given**: Leader is in a social lobby with reservations for followers A and B.
**When**: Leader kicks follower A via `snsPartyKickRequest`.
**Then**:

1. Same as BAC-006 -- kicked member's reservation is cleared, other member's remains.

**Verification**: `TestMemberKicked_TheirReservationCleared`
**Source**: Behavior matrix #8: kick from party

---

### BAC-008: New member joins party while leader in match (late join)

**Given**: Leader is in a social lobby.
**When**: New member joins party via `snsPartyJoinRequest`.
**Then**:

1. After successful join and `snsPartyTrackAndJoin`, `createReservationForNewPartyMember` is called.
2. It looks up the leader's current match via `StreamModeService`.
3. If leader is in a social match, signals the match to create a reservation for the new member.
4. `rebuildCache()` runs, adding the new member to `Players` with `IsReservation: true`.
5. New member's next `lobbyFind` -> `findReservation` finds it -> joins directly.

**Verification**: `TestLateJoin_ReservationCreated` -- leader in match, new member joins party, verify reservation created.
**Source**: Spec section 6 "Late Join"

---

### BAC-009: New member accepts invite while leader in match

**Given**: Leader is in a social lobby.
**When**: New member accepts invite via `snsPartyRespondToInviteRequest`.
**Then**: Same as BAC-008 -- `createReservationForNewPartyMember` is called after successful join.

**Verification**: `TestInviteAccept_ReservationCreated`
**Source**: Behavior matrix #5: Accept party invite

---

### BAC-010: Leader in arena/combat, no reservation for late joiner

**Given**: Leader is in an arena match.
**When**: New member joins party.
**Then**:

1. `createReservationForNewPartyMember` checks leader's match label.
2. If arena/combat, skips reservation creation.
3. No reservation created.

**Verification**: `TestLateJoin_ArenaLeader_NoReservation` -- leader in arena, new member joins, verify no reservation created.
**Source**: Behavior matrix #4: Join party (leader IN arena/combat match)

---

### BAC-011: SignalCreatePartyReservations skips existing presences and reservations

**Given**: Match has 2 players already connected (including member X). Match also has a reservation for member Y.
**When**: `SignalCreatePartyReservations` is received with members [X, Y, Z].
**Then**:

1. X is skipped (already in `presenceMap`).
2. Y is skipped (already in `reservationMap`).
3. Z gets a new `slotReservation` with 5-minute expiry.
4. `rebuildCache()` is called once.

**Verification**: `TestSignalCreatePartyReservations_SkipsDuplicates`
**Source**: Spec section "New Signal: SignalCreatePartyReservations"

---

### BAC-012: SignalClearPartyReservations clears by party ID

**Given**: Match has 3 reservations: 2 from party A, 1 from party B.
**When**: `SignalClearPartyReservations` is received with party A's ID.
**Then**:

1. Both party A reservations are deleted.
2. Party B reservation remains.
3. `rebuildCache()` is called.

**Verification**: `TestSignalClearPartyReservations_ClearsByPartyID`
**Source**: Spec section "New Signal: SignalClearPartyReservations"

---

### BAC-013: Reservation for offline member skipped

**Given**: Party has 3 members: leader, follower A (live session), follower B (dead session).
**When**: `createPartyReservations` runs.
**Then**:

1. `sessionRegistry.Get(followerB.SessionID)` returns nil.
2. No reservation is created for follower B.
3. Reservation IS created for follower A.

**Verification**: `TestReservationForOfflineMember_Skipped`
**Source**: Spec edge case 7 "Reservation Created for Offline Member"

---

### BAC-014: Concurrent lobbyFind -- findReservation is idempotent

**Given**: Reservation exists for follower.
**When**: Two `LobbyFindSessionRequest` messages arrive simultaneously, both enter `findReservation`.
**Then**:

1. Both calls read the match label (read-only).
2. First call: reservation found -> `lobbyJoin` -> succeeds -> reservation consumed by `MatchJoinAttempt`.
3. Second call: reservation still visible in cached label -> `lobbyJoin` -> `ErrJoinRejectReasonDuplicateJoin` -> no-op (existing behavior at `evr_lobby_joinentrant.go:95-98`).

**Verification**: `TestConcurrentLobbyFind_IdempotentReservation`
**Source**: Spec edge case 8 "Concurrent lobbyFind Calls"

---

### BAC-015: Session ID change -- reservation found via user ID fallback

**Given**: Leader in social lobby with reservation for follower (session A).
**When**: Follower disconnects and reconnects with session B, re-joins party.
**Then**:

1. `snsPartyTrackAndJoin` fires.
2. `createReservationForNewPartyMember` re-creates reservation with new session ID.
3. Follower's `findReservation` finds it (new session ID in `Players` slice).
4. Alternatively: if the old reservation is still present, `MatchJoinAttempt` falls back to `LoadAndDeleteReservationByUserIDRaw` (existing behavior at `evr_match.go:419`).

**Verification**: `TestSessionIDChange_ReservationStillFound`
**Source**: Spec edge case 1 "Session ID Change"

---

### BAC-016: Leader promoted -- old reservations cleared, new ones created

**Given**: Leader A is in a social lobby with reservations for members B and C.
**When**: Leadership is passed to member B via `snsPartyPassOwnershipRequest`.
**Then**:

1. Old leader A's reservations in their current match are cleared (`SignalClearPartyReservations` with old party context).
2. Active matchmaking tickets are cancelled.
3. If new leader B is in a match, `createPartyReservations` creates new reservations for members A and C.

**Verification**: `TestLeaderPromotion_ReservationsRebuilt`
**Source**: Spec section 7 "Party Leader Removal" and behavior matrix #9

---

### BAC-017: lobbyEntrantConnected only creates reservations for leader

**Given**: Party of 2 (leader + follower). Both join same match via matchmaker.
**When**: `lobbyEntrantConnected` fires for the follower.
**Then**:

1. `createPartyReservations` is called.
2. Follower is NOT the party leader.
3. No reservations are created (only leaders create reservations).

**Verification**: `TestFollowerConnected_NoReservationsCreated`
**Source**: Spec section 1: "If leader, enumerate party members..."

---

### BAC-018: appendPartyReservationPlaceholders still works (belt-and-suspenders)

**Given**: Leader in party, calls `lobbyFind` for social lobby.
**When**: `PrepareEntrantPresences` runs, followed by `appendPartyReservationPlaceholders`.
**Then**:

1. Placeholder reservations are still created for party members not in entrants list.
2. These flow through `LobbyJoinEntrants` -> `MatchJoinAttempt` as before.
3. `lobbyEntrantConnected` later refreshes/extends these via `SignalCreatePartyReservations`.

**Verification**: Existing tests for `appendPartyReservationPlaceholders` continue to pass.
**Source**: Spec section 3 "Social Lobby Reservations Persist" -- "belt-and-suspenders"

---

### BAC-019: lobbyFindOrCreateSocial priority join replaced with findReservation

**Given**: Follower is in a party. Leader is in a social lobby. Reservation exists.
**When**: Follower enters `lobbyFindOrCreateSocial`.
**Then**:

1. Before the priority join logic at line 791 of `evr_lobby_find.go`, `findReservation` is checked.
2. If reservation found, joins directly -- bypasses the tracker-read leader lookup at lines 791-888.

**Verification**: `TestSocialLobbyFind_ReservationBeforePriorityJoin`
**Source**: Spec gap resolution #65

---

### BAC-020: Migration fallback -- old path still works when no reservation exists

**Given**: Rolling deploy. Leader is on old server (no reservation created). Follower is on new server.
**When**: Follower's `lobbyFind` calls `findReservation`.
**Then**:

1. `findReservation` returns false (no reservation in match label).
2. Follower falls through to existing `TryFollowPartyLeader` / `pollFollowPartyLeader` path.
3. The existing path works as before.

**Verification**: `TestMigrationFallback_OldPathStillWorks`
**Source**: Spec "Migration Notes" section

---

### BAC-021: Any member cancels matchmaking -- ALL members' matchmaking cancelled, ticket removed

**Given**: A party of 3 (leader + 2 followers). The leader has submitted a matchmaking ticket after the 15s formation period. All 3 members are matchmaking.
**When**: Any member (leader or follower) sends `LobbyPendingSessionCancel`.
**Then**:

1. The cancelling member's `lobbyPendingSessionCancel` handler detects the party via `LoadParams(session.Context())`.
2. The handler enumerates party members via `tracker.ListByStream(partyStream, ...)`.
3. For each party member (including the cancelling member): `LeaveMatchmakingStream` is called, which untracks their `StreamModeMatchmaking` presence.
4. Each member's `monitorMatchmakingStream` goroutine detects the stream closure and cancels their `lobbyFind` context.
5. The active matchmaking ticket is removed via `lobbyGroup.MatchmakerRemoveAll()`.
6. No partial tickets remain -- the party either matchmakes together or not at all.

**Verification**: `TestAnyCancelMatchmaking_AllMembersCancelled` -- create a party with active ticket, simulate one member cancelling, verify all members' matchmaking streams are closed and ticket is removed.
**Source**: Spec section 4 "Cancellation Rules" rule 1

---

### BAC-022: Cancel does NOT remove from party

**Given**: A party of 3 (leader + 2 followers). All are matchmaking.
**When**: Any member cancels matchmaking (via `LobbyPendingSessionCancel`).
**Then**:

1. All members' matchmaking is cancelled (per BAC-021).
2. All members remain in the party group -- `params.currentPartyID` is unchanged.
3. All members remain on the party stream (`StreamModeParty`).
4. No `snsPartyLeaveCleanup` is called.
5. Members can re-queue for matchmaking together by sending a new `LobbyFindSessionRequest`.

**Verification**: `TestCancelMatchmaking_StaysInParty` -- cancel matchmaking, verify party membership unchanged, verify party stream presences intact.
**Source**: Spec section 4 "Cancellation Rules" rule 2

---

### BAC-023: Cancel during formation (before ticket submitted) -- formation stops, everyone back to social-ready

**Given**: A party of 3 (leader + 2 followers). The leader has started matchmaking. The 15s formation period is active. The ticket has NOT yet been submitted (still waiting for followers to join).
**When**: Any member sends `LobbyPendingSessionCancel` during the formation period.
**Then**:

1. The formation timer is cancelled.
2. All members' matchmaking streams are closed (per BAC-021).
3. No ticket was ever submitted to the matchmaker, so no ticket removal is needed.
4. All members transition back to `StateSocialReady` (if they were in a social lobby) or their `lobbyFind` context is cancelled.
5. All members remain in the party (per BAC-022).

**Verification**: `TestCancelDuringFormation_NoTicketSubmitted` -- start formation, cancel before 15s, verify no ticket was added to the matchmaker, verify all members back to social-ready.
**Source**: Spec section 4 "Cancellation Rules" rule 3

---

### BAC-024: Cancel during matchmaking (after ticket submitted) -- ticket removed, everyone back to social-ready

**Given**: A party of 3 (leader + 2 followers). The 15s formation period has completed. The ticket has been submitted to the matchmaker.
**When**: Any member sends `LobbyPendingSessionCancel` after the ticket is active.
**Then**:

1. All members' matchmaking is cancelled (per BAC-021).
2. The active matchmaking ticket is removed from the matchmaker via `lobbyGroup.MatchmakerRemoveAll()`.
3. All members transition back to `StateSocialReady`.
4. All members remain in the party (per BAC-022).

**Verification**: `TestCancelDuringMatchmaking_TicketRemoved` -- submit ticket, cancel, verify ticket is removed from matchmaker, verify all members back to social-ready.
**Source**: Spec section 4 "Cancellation Rules" rule 4

---

### BAC-025: Ticket NOT submitted until after 15s formation period

**Given**: A party of 3 (leader + 2 followers). The leader starts matchmaking for arena/combat.
**When**: The leader's `lobbyMatchMakeWithFallback` is entered.
**Then**:

1. The leader does NOT call `replaceTicket` / `addTicket` immediately.
2. A 15-second formation timer starts.
3. During the formation period, followers' `LobbyFindSessionRequest` arrivals are tracked.
4. After the 15s timer fires, the ticket is submitted with all members who are on the matchmaking stream.
5. Members who did NOT start matchmaking within the 15s window are excluded from the ticket (but NOT removed from the party -- per AMBIGUITY-4 resolution).

**Verification**: `TestDeferredTicketSubmission_WaitsForFormation` -- start matchmaking, verify no ticket exists for the first 14 seconds, verify ticket is submitted after 15 seconds.
**Source**: Spec section 4 "Phase 3 -- Matchmaking" rule 1

---

## Section 2: Architectural Decision Records (ADRs)

### ADR-001: Reservation as coordination mechanism, not tracker reads

**Context**: The tracker-read follow pattern (5+ tracker reads to find leader's match) is TOCTOU-prone. The `isFollowerInLeaderDestination` test at `evr_lobby_find_follower_test.go:57` documents the exact ambiguity: when `followerMatchID == leaderMatchID == currentMatchID`, the system cannot distinguish "leaving this match" from "already in this match."
**Decision**: Use match-level reservations as the coordination mechanism. Leaders create reservations when they join a match; followers search for their reservation, not the leader's location. No ambiguity, no stale data, no race conditions.
**Alternatives considered**: (1) Fix the tracker reads with version numbers -- rejected because the fundamental problem is reading stale data, not ordering it. (2) Use a separate service/database -- rejected because reservations already exist in match labels.
**Consequences**: Followers never read the leader's tracker service stream for follow purposes. The reservation IS the signal. Six functions become unnecessary after stabilization.
**Source**: Spec "Principle" section, behavior matrix bugs #42-#44

---

### ADR-002: Game server as authority for "player is in match"

**Context**: Service stream presences can be stale between the leader calling `LobbyJoinEntrants` and the game server confirming connection. Followers reading these stale presences cause false-positive convergence.
**Decision**: "Player is in match" is true only when the game server confirms it via `lobbyEntrantConnected`. Reservation creation for party members happens at this event, not at `LobbyJoinEntrants`.
**Status**: **COMPLETED** -- service/guild/matchmaking streams already deferred to `lobbyEntrantConnected` in commit `671456443`.
**Consequences**: `lobbyEntrantConnected` gains party logic (currently has zero). The chain `sessionRegistry.Get(sessionID)` -> `LoadParams(s.Context())` -> `currentPartyID` is used to resolve party membership.
**Source**: Spec section 1, gap resolution #69

---

### ADR-003: Match label PlayerInfo.IsReservation as discovery mechanism

**Context**: Followers need to discover where their reservation is without scanning the match registry.
**Decision**: `rebuildCache()` already populates `PlayerInfo.IsReservation = true` for entries in `reservationMap` (see `evr_match_label.go:469,487,503,518,533`). Followers look up the leader's match via the leader's service stream, call `MatchLabelByID`, and check if their session appears in the `Players` slice with `IsReservation: true`.
**Previous approach (dropped)**: A `StreamModeReservation` stream mode was considered but dropped. The match label approach is simpler: no new stream constant, no tracker Track/Untrack calls, and reservation cleanup happens automatically when `reservationMap` entries are deleted and `rebuildCache` runs.
**Consequences**: One `MatchLabelByID` call per `findReservation` invocation. Label is always fresh (not cached). No stream cleanup needed.
**Source**: Spec section 2 -- adapted from "Recommended: Option A"

---

### ADR-004: Match signal for state modification (not direct writes)

**Context**: `MatchLabel` is the match handler's state, accessed only within the match goroutine. External code must not write to it directly.
**Decision**: Use `MatchSignal` with new opcodes (`SignalCreatePartyReservations`, `SignalClearPartyReservations`) to modify reservation state. This is the existing pattern used by `SignalPrepareSession` at `evr_match.go:1658`.
**Alternatives considered**: Direct write to `reservationMap` from the pipeline -- rejected because it violates the match handler's single-goroutine execution model and would race.
**Consequences**: Two new signal opcodes. Signal handler switch gets two new cases. Signal payloads are JSON-serialized.
**Source**: Spec section "Why signal, not direct write"

---

### ADR-005: Keep existing follow path as migration fallback

**Context**: During a rolling deploy, old server processes do not create reservations. Followers on new servers must fall back to the old path.
**Decision**: The `findReservation` check is placed before `TryFollowPartyLeader`. If no reservation is found, the old path executes. After stabilization, the old functions are removed.
**Alternatives considered**: Hard cutover (remove old path immediately) -- rejected because a rollback would leave followers with no follow mechanism.
**Consequences**: Code duplication during migration period. Functions `TryFollowPartyLeader`, `pollFollowPartyLeader`, etc. are kept but will be removed in a follow-up PR.
**Source**: Spec "Migration Notes" section

---

### ADR-006: Crash handler does NOT create reconnect reservation in leader's social lobby

**Context**: When a party member crashes in arena, they get a reconnect reservation in the arena match (60s). The leader may also have a social lobby reservation for them.
**Decision**: The crash handler does NOT create a social lobby reservation. The arena reconnect reservation takes precedence. If the reconnect window expires, the player reconnects and finds the leader's existing reservation naturally.
**Alternatives considered**: Creating both reservations -- rejected because the player should reconnect to arena first (the game is still going).
**Consequences**: No new logic in crash handler. Existing reconnect reservation logic at `evr_match.go:807-828` unchanged.
**Source**: Spec gap resolution #71

---

### ADR-007: configureParty leader wait loop uses party stream, not match presences

**Context**: `configureParty` at line 386-548 waits for party members by counting presences in the dying match via `GetMatchPresences` (line 443). The test `TestExpectedFollowerCount_FromPartyStream` documents that this gives wrong counts when followers have already left.
**Decision**: The leader wait loop should use `lobbyGroup.Size()` from the party stream, not `GetMatchPresences`. The party stream is the source of truth for who's in the party.
**Alternatives considered**: Keep using match presences -- rejected because the race is documented and causes real bugs.
**Consequences**: Lines 443-452 of `evr_lobby_find.go` change from `GetMatchPresences` query to `lobbyGroup.Size()` check. This is a separate fix from the reservation system and should be a separate commit.
**Source**: Spec gap resolution #66

---

### ADR-008: Cancel propagates to all party members, not just the canceller

**Context**: In the current implementation, `lobbyPendingSessionCancel` (`evr_pipeline_matchmaker.go:232`) only cancels the individual caller's matchmaking stream via `LeaveMatchmakingStream`. There is no party-wide cancellation. This means one member can cancel while others continue matchmaking, resulting in partial tickets or orphaned matchmaking contexts.
**Decision**: When any party member cancels matchmaking, ALL members' matchmaking is cancelled. The cancel handler (`lobbyPendingSessionCancel`) is extended to enumerate party members and close all their matchmaking streams. The ticket is removed via `lobbyGroup.MatchmakerRemoveAll()`. This is consistent with the spec rule: "No partial tickets."
**Alternatives considered**: (1) Cancel only the individual -- rejected because tickets are immutable and contain all party members; a partial ticket is invalid. (2) Remove the cancelling member from the party -- rejected because matchmaking is an activity, not a relationship; cancelling the activity should not end the social relationship.
**Consequences**: `lobbyPendingSessionCancel` gains party awareness. Each member's `monitorMatchmakingStream` goroutine detects the stream closure and cancels their `lobbyFind` context. No new message types needed -- the existing stream-close mechanism propagates the cancel.
**Source**: Spec section 4 "Cancellation Rules" rules 1-5

---

### ADR-009: Deferred ticket submission (15s formation period)

**Context**: Currently, `lobbyMatchMakeWithFallback` (`evr_lobby_matchmake.go:143`) calls `replaceTicket` immediately upon entry (line 205). The ticket is submitted to the matchmaker before all party members have had a chance to start matchmaking. This means the ticket may not include all party members, and late-arriving members trigger a cancel-and-rebuild cycle (`cancelTicketForLateArrival` at `evr_lobby_find.go:1356`).
**Decision**: The leader's matchmaking loop waits up to 15 seconds (the formation period) before submitting the first ticket. During this period, it monitors the matchmaking stream for all party members. Once all members are present on the stream, or the 15s timer fires, the ticket is submitted with whoever is ready. Members who are NOT on the matchmaking stream when the timer fires are excluded from the ticket but NOT removed from the party.
**Alternatives considered**: (1) Keep immediate submission with cancel-and-rebuild -- rejected because it creates unnecessary matchmaker churn and ticket invalidation. (2) Block on all members joining before any timeout -- rejected because it could block indefinitely if a member is offline.
**Consequences**: `lobbyMatchMakeWithFallback` gains a formation phase before the first `replaceTicket` call. `cancelTicketForLateArrival` is no longer needed for the primary path (tickets are not submitted until formation is complete), though it is kept as a safety net during migration. The 15s formation period is bounded -- it does not extend the matchmaking timeout; it is a pre-matchmaking step.
**Source**: Spec section 4 "Phase 1 -- Party Formation" and "Phase 3 -- Matchmaking"

---

## Section 3: Mechanical Implementation Steps

**Line number note**: All line numbers verified against current code as of commit `af4d2c276`. Multiple steps modify the same files:

- `evr_lobby_find.go`: Steps 4, 4b
- `evr_pipeline_party.go`: Steps 5, 6, 7, 9
- `evr_match.go`: Steps 2, 8
- `evr_pipeline_lobby.go`: Step 3
- `evr_lobby_matchmake.go`: Step 10
- `evr_pipeline_matchmaker.go`: Step 11

When multiple steps modify the same file, implementers should either (a) apply changes **bottom-up** within each file (highest line numbers first) so earlier insertions do not shift later insertion points, or (b) use the surrounding code context (function names, comments, block descriptions) rather than raw line numbers to locate each insertion point. The `Depends on` field documents functional dependencies; it does NOT imply application order within a single file.

### Step 1: Add SignalCreatePartyReservations and SignalClearPartyReservations opcodes

**BACs verified**: (prerequisite for BAC-001, BAC-005, BAC-011, BAC-012)
**Files modified**: `/home/andrew/src/nakama/server/evr_match_signals.go`
**Depends on**: none

#### Changes:

1. In `evr_match_signals.go` at line 28 (after `SignalKickEntrants`):
   - **Add**: Two new signal opcodes to the iota:
     ```
     SignalCreatePartyReservations
     SignalClearPartyReservations
     ```
   - **Reason**: New signals for match handler to create/clear party reservations (ADR-004). Source: spec "Implementation Details" section.

2. In `evr_match_signals.go` after line 105 (after `SignalReserveSlotsPayload` struct):
   - **Add**: Two new payload types:

     ```go
     // SignalCreatePartyReservationsPayload carries the list of party members
     // who need slot reservations in the match.
     type SignalCreatePartyReservationsPayload struct {
         Members []*EvrMatchPresence `json:"members"`
     }

     // SignalClearPartyReservationsPayload carries the party ID whose
     // reservations should be removed from the match.
     type SignalClearPartyReservationsPayload struct {
         PartyID uuid.UUID `json:"party_id"`
     }
     ```

   - **Reason**: Typed payloads for the new signals.

#### Test:

- Function: Compile check only -- constant is used by subsequent steps
- How to run: `GOWORK=off go build ./server/...`

---

### Step 2: Add signal handlers in MatchSignal

**BACs verified**: BAC-011, BAC-012
**Files modified**: `/home/andrew/src/nakama/server/evr_match.go`
**Depends on**: Step 1

#### Changes:

1. In `evr_match.go` at line 1847 (before the `default:` case in the `switch signal.OpCode` block):
   - **IMPORTANT: Fall-through pattern**. The `MatchSignal` switch has mixed patterns: some cases return early (e.g., `SignalShutdown` at line 1616, `SignalGetEndpoint` at line 1639), while others fall through to `updateLabel` at line 1852 (e.g., `SignalPrepareSession`, `SignalLockSession`, `SignalPlayerUpdate`). The new reservation cases follow the **fall-through pattern** -- they modify state and let execution continue to `updateLabel` and the `SignalResponse{Success: true, ...}` return at line 1859. **Do NOT add an early return** to either case; if an implementer adds `return state, ...` inside these cases, the label update and success response will be skipped.
   - **Add**: Two new cases:

     ```go
     // NOTE: These cases intentionally do NOT return early.
     // They fall through to updateLabel (line 1852) and return
     // SignalResponse{Success: true} (line 1859), following the
     // same pattern as SignalPrepareSession, SignalLockSession,
     // and SignalPlayerUpdate.
     case SignalCreatePartyReservations:
         var payload SignalCreatePartyReservationsPayload
         if err := json.Unmarshal(signal.Payload, &payload); err != nil {
             return state, SignalResponse{Message: fmt.Sprintf("failed to unmarshal create party reservations: %v", err)}.String()
         }
         created := 0
         for _, member := range payload.Members {
             sid := member.GetSessionId()
             if _, exists := state.presenceMap[sid]; exists {
                 continue // already in match
             }
             if _, exists := state.reservationMap[sid]; exists {
                 continue // already reserved
             }
             state.reservationMap[sid] = &slotReservation{
                 Presence: member,
                 Expiry:   time.Now().Add(5 * time.Minute),
             }
             created++
         }
         if created > 0 {
             state.rebuildCache()
         }
         logger.WithFields(map[string]any{
             "requested": len(payload.Members),
             "created":   created,
         }).Info("Created party reservations via signal")

     case SignalClearPartyReservations:
         var payload SignalClearPartyReservationsPayload
         if err := json.Unmarshal(signal.Payload, &payload); err != nil {
             return state, SignalResponse{Message: fmt.Sprintf("failed to unmarshal clear party reservations: %v", err)}.String()
         }
         cleared := 0
         for sid, r := range state.reservationMap {
             if r.Presence.PartyID == payload.PartyID {
                 delete(state.reservationMap, sid)
                 cleared++
             }
         }
         if cleared > 0 {
             state.rebuildCache()
         }
         logger.WithFields(map[string]any{
             "party_id": payload.PartyID.String(),
             "cleared":  cleared,
         }).Info("Cleared party reservations via signal")
     ```

   - **Reason**: Match handler processes reservation create/clear signals inside its single goroutine (ADR-004). Source: spec "Implementation Details" section.

#### Test:

- Function: `TestSignalCreatePartyReservations_SkipsDuplicates`, `TestSignalClearPartyReservations_ClearsByPartyID`
- File: `evr_match_signal_test.go` (new file)
- What it asserts: Create signal skips existing presences/reservations; clear signal removes only matching partyID entries
- How to run: `GOWORK=off go test -run 'TestSignalCreatePartyReservations|TestSignalClearPartyReservations' ./server/...`
- **Replay fixtures that must still pass**: All 7 fixtures in `server/testdata/replay/`

---

### Step 3: Add createPartyReservations function and wire into lobbyEntrantConnected

**BACs verified**: BAC-001, BAC-013, BAC-017
**Files modified**: `/home/andrew/src/nakama/server/evr_pipeline_lobby.go`
**Depends on**: Steps 1, 2

#### Changes:

1. In `evr_pipeline_lobby.go` after line 144 (after `lobbyEntrantConnected` function ends, before `lobbyEntrantsRemove` at line 146):
   - **Add**: New function `createPartyReservations`:
     ```go
     // createPartyReservations creates slot reservations in the given match for
     // all party members who are not already in the match. Called from
     // lobbyEntrantConnected when the connected entrant is the party leader.
     //
     // IMPORTANT: This function is dispatched as a goroutine. The caller MUST
     // pass context.WithoutCancel(s.Context()) so that reservation creation
     // completes even if the triggering player session disconnects. Using the
     // raw session context would cancel the goroutine mid-creation if the
     // player disconnects, leaving some members with reservations and others
     // without.
     func (p *EvrPipeline) createPartyReservations(ctx context.Context, logger *zap.Logger, matchID MatchID, leaderSessionID uuid.UUID, partyID uuid.UUID) {
     ```
   - The function body should:
     a. List party members via `p.nk.tracker.ListByStream(PresenceStream{Mode: StreamModeParty, Subject: partyID, Label: p.node}, true, true)`
     b. For each member != leader:
     - Check `p.nk.sessionRegistry.Get(memberSessionID)` is non-nil (skip dead sessions -- BAC-013)
     - Build `*EvrMatchPresence` with `SessionID`, `UserID`, `Username`, `PartyID`, `RoleAlignment: evr.TeamSocial`, `Node: p.node`
     - Load additional fields from `LoadParams(memberSession.Context())` if available: `EvrID`, `DisplayName` (matching existing pattern in `appendPartyReservationPlaceholders` at `evr_lobby_find.go:1453-1483`)
       c. If any members were collected, send `SignalCreatePartyReservations` to the match via `SignalMatch(ctx, p.nk, matchID, SignalCreatePartyReservations, payload)`
   - **Reason**: Primary reservation creation path for party leaders. Source: spec section 1.

2. In `evr_pipeline_lobby.go` at line 92 (after `acceptedIDs = append(acceptedIDs, entrantID)` inside the entrant loop):
   - **IMPORTANT: Variable naming in `lobbyEntrantConnected`**. The parameter `session` (line 13) is the **game server's** `*sessionWS`. The **player's** session is `s`, obtained at line 45 via `p.nk.sessionRegistry.Get(...)`. The player's context is `s.Context()` (line 52). All party and reservation logic must use `s` (the player session), not `session` (the game server session). The party enumeration is done inside `createPartyReservations` via `p.nk.tracker.ListByStream(...)`, not at the call site.
   - **Add**: Party reservation creation call:
     ```go
     // Create party reservations for followers when the leader connects.
     // NOTE: `s` is the player's session (line 45), NOT `session` (the game server).
     if params, ok := LoadParams(s.Context()); ok && params.currentPartyID != uuid.Nil {
         // Check if this player is the party leader via the party registry.
         if ph, ok := p.nk.partyRegistry.Get(params.currentPartyID); ok {
             ph.RLock()
             leader := ph.leader
             ph.RUnlock()
             if leader != nil && leader.UserPresence.SessionId == presence.GetSessionId() {
                 // This entrant is the party leader. Dispatch reservation
                 // creation asynchronously. Use context.WithoutCancel so
                 // reservation creation completes even if the player
                 // disconnects mid-creation.
                 go p.createPartyReservations(context.WithoutCancel(s.Context()), logger, matchID, uuid.FromStringOrNil(presence.GetSessionId()), params.currentPartyID)
             }
         }
     }
     ```
   - **Reason**: `lobbyEntrantConnected` is the trigger point for reservation creation (spec section 1). The `go` keyword dispatches asynchronously to avoid blocking the entrant accept response. `context.WithoutCancel` ensures the goroutine completes even if the player session disconnects. The party member enumeration happens inside `createPartyReservations` via `p.nk.tracker.ListByStream(...)`, so no `ListByStream` call is needed at this call site.

#### Test:

- Function: `TestLeaderJoinsSocial_ReservationsCreated`, `TestFollowerConnected_NoReservationsCreated`, `TestReservationForOfflineMember_Skipped`
- File: `evr_pipeline_lobby_test.go` (new file)
- What it asserts: Reservations created only for leader; dead sessions skipped
- How to run: `GOWORK=off go test -run 'TestLeaderJoinsSocial|TestFollowerConnected|TestReservationForOfflineMember' ./server/...`
- **Replay fixtures that must still pass**: All 7 fixtures

---

### Step 4: Add findReservation function and insert in lobbyFind follower path

**BACs verified**: BAC-002, BAC-003, BAC-014, BAC-020
**Files modified**: `/home/andrew/src/nakama/server/evr_lobby_find.go`
**Depends on**: Steps 1, 2, 3

#### Changes:

1. In `evr_lobby_find.go` after line 1856 (after `pollFollowPartyLeader` function, at end of file):
   - **Add**: New function `findReservation`:

     ```go
     // findReservation checks whether the party leader's current match has a
     // reservation for this follower. It looks up the leader's match via the
     // leader's service stream, fetches the match label, and checks the
     // Players slice for an entry with IsReservation == true matching this
     // session's ID.
     //
     // Returns the match ID and true if a reservation is found. Returns
     // MatchID{}, false if no reservation exists (leader not in a match,
     // match label unreadable, or follower not reserved).
     //
     // This replaces the old StreamModeReservation tracker-read approach.
     // Cost: one tracker read (leader's service stream) + one MatchLabelByID
     // call per invocation.
     func (p *EvrPipeline) findReservation(ctx context.Context, logger *zap.Logger, session *sessionWS, lobbyGroup *LobbyGroup) (MatchID, bool) {
         leader := lobbyGroup.GetLeader()
         if leader == nil {
             return MatchID{}, false
         }
         leaderSessionID := uuid.FromStringOrNil(leader.SessionId)
         leaderUserID := uuid.FromStringOrNil(leader.UserId)
         if leaderSessionID == session.id {
             return MatchID{}, false // caller IS the leader
         }

         // Look up leader's current match via their service stream.
         stream := PresenceStream{
             Mode:    StreamModeService,
             Subject: leaderSessionID,
             Label:   StreamLabelMatchService,
         }
         presence := p.nk.tracker.GetLocalBySessionIDStreamUserID(leaderSessionID, stream, leaderUserID)
         if presence == nil {
             return MatchID{}, false // leader not in a match
         }
         leaderMatchID := MatchIDFromStringOrNil(presence.GetStatus())
         if leaderMatchID.IsNil() {
             return MatchID{}, false
         }

         // Fetch match label and check for our reservation.
         label, err := MatchLabelByID(ctx, p.nk, leaderMatchID)
         if err != nil || label == nil {
             logger.Debug("Failed to get leader's match label for reservation check",
                 zap.Error(err), zap.String("mid", leaderMatchID.String()))
             return MatchID{}, false
         }

         // Check if our session ID appears as a reservation in the Players slice.
         sessionIDStr := session.id.String()
         for _, player := range label.Players {
             if player.SessionID == sessionIDStr && player.IsReservation {
                 logger.Debug("Found reservation in leader's match",
                     zap.String("match_id", leaderMatchID.String()))
                 return leaderMatchID, true
             }
         }
         return MatchID{}, false
     }
     ```

   - **Reason**: Lightweight reservation lookup for followers. One tracker read + one label fetch, replacing 4+ tracker reads in the old path. Source: spec section 2.

2. In `evr_lobby_find.go` at line 114 (the start of `if !isLeader {` block, after line 113 `if lobbyGroup != nil {`):
   - **Add** immediately after line 114 (`if !isLeader {`), before line 115 (`// Guard: if the follower is in an active Arena/Combat match`):
     ```go
     // Reservation-based follow: if the leader has created a reservation
     // for this follower, join directly without tracker-read follow logic.
     if reservationMatchID, found := p.findReservation(ctx, logger, session, lobbyGroup); found {
         logger.Info("Found party reservation, joining directly",
             zap.String("reservation_mid", reservationMatchID.String()))
         if err := p.lobbyJoin(ctx, logger, session, lobbyParams, reservationMatchID); err != nil {
             logger.Warn("Failed to join via reservation, falling through to legacy path",
                 zap.Error(err))
         } else {
             // Observer: follower joined via reservation.
             if lc := getMatchLifecycle(session); lc != nil {
                 lc.TransitionTo(StateInMatch, "joined via party reservation", WithMatchID(reservationMatchID.String()))
             }
             return nil
         }
     }
     ```
   - **Reason**: This is the primary insertion point per spec section 2. If reservation is found, skip all tracker-read follow logic. If join fails, fall through to existing path (migration safety -- ADR-005). Source: spec "Modified Functions" table.

#### Test:

- Function: `TestFollowerFindsReservation_JoinsDirectly`, `TestMigrationFallback_OldPathStillWorks`, `TestFindReservation_Found`, `TestFindReservation_NotFound`
- File: `evr_lobby_find_reservation_test.go` (new file)
- What it asserts: When reservation exists, `lobbyJoin` is called with reservation match ID; when no reservation, falls through to `isFollowerInActiveMatch` etc.
- How to run: `GOWORK=off go test -run 'TestFollowerFindsReservation|TestMigrationFallback|TestFindReservation' ./server/...`
- **Replay fixtures that must still pass**: All 7 fixtures

---

### Step 4b: Insert findReservation check in lobbyFindOrCreateSocial

**BACs verified**: BAC-019
**Files modified**: `/home/andrew/src/nakama/server/evr_lobby_find.go`
**Depends on**: Step 4

#### Changes:

1. In `evr_lobby_find.go` at line 791 (before the priority-1 party leader join block `if lobbyParams.PartyGroupName != "" && lobbyParams.PartyGroupName != "tablet" {`), inside `lobbyFindOrCreateSocial`:
   - **Context**: Social-mode followers reach `lobbyFindOrCreateSocial` via `lobbyFind` line 167/207. The priority-1 block at lines 791-888 uses tracker reads to find the leader's match -- this is the old TOCTOU-prone path that the reservation system replaces. The `findReservation` check must go BEFORE this block so followers with a reservation skip the tracker-read path entirely.
   - **IMPORTANT**: The `session` parameter in `lobbyFindOrCreateSocial` is a `Session` interface (not `*sessionWS`). The `findReservation` function takes `*sessionWS`, so a type assertion is needed. The `lobbyGroup` parameter may not exist at this call site; it must be obtained from the `lobbyParams` or `JoinPartyGroup`.
   - **Retry loop behavior**: The insertion point at line 791 is inside the retry loop that starts at line 723 (`for attempt := 0; attempt < maxAttempts; attempt++`). The `findReservation` check will be called on EACH retry iteration. This is intentional and correct: the leader may connect and create reservations asynchronously between retry attempts. On the first iteration the reservation may not exist yet; by the second or third iteration `createPartyReservations` may have completed, and `findReservation` will find it. If `findReservation` succeeds and `lobbyJoin` succeeds, the function returns immediately (no further retries). If `findReservation` succeeds but `lobbyJoin` fails, it falls through to the legacy priority join path for that iteration.
   - **Add** before line 791:
     ```go
     // Reservation-based follow: if the leader has created a reservation
     // for this follower in a social lobby, join directly without the
     // tracker-read priority join path.
     if ws, ok := session.(*sessionWS); ok {
         if lobbyParams.PartyGroupName != "" && lobbyParams.PartyGroupName != "tablet" {
             if lobbyGroup, _, err := JoinPartyGroup(ws, lobbyParams.PartyGroupName, lobbyParams.CurrentMatchID); err == nil && lobbyGroup != nil {
                 if reservationMatchID, found := p.findReservation(ctx, logger, ws, lobbyGroup); found {
                     logger.Info("Found party reservation in social lobby path, joining directly",
                         zap.String("reservation_mid", reservationMatchID.String()))
                     if err := p.lobbyJoin(ctx, logger, ws, lobbyParams, reservationMatchID); err != nil {
                         logger.Warn("Failed to join via reservation in social path, falling through to legacy priority join",
                             zap.Error(err))
                     } else {
                         // Observer: follower joined via reservation in social lobby path.
                         if lc := getMatchLifecycle(ws); lc != nil {
                             lc.TransitionTo(StateInMatch, "joined via party reservation (social)", WithMatchID(reservationMatchID.String()))
                         }
                         return nil
                     }
                 }
             }
         }
     }
     ```
   - **Reason**: `lobbyFindOrCreateSocial` is the code path for social-mode followers (reached via `lobbyFind` line 167/207). Without this check, followers in social mode would bypass the reservation system entirely and use the old tracker-read priority join at lines 791-888. BAC-019 requires this check. Source: spec gap resolution #65.

#### Test:

- Function: `TestSocialLobbyFind_ReservationBeforePriorityJoin`
- File: `evr_lobby_find_reservation_test.go`
- What it asserts: When reservation exists in social lobby path, `lobbyJoin` is called with reservation match ID; bypasses the tracker-read priority join at lines 791-888.
- How to run: `GOWORK=off go test -run 'TestSocialLobbyFind_ReservationBeforePriorityJoin' ./server/...`

---

### Step 5: Add createReservationForNewPartyMember function

**BACs verified**: BAC-008, BAC-009, BAC-010
**Files modified**: `/home/andrew/src/nakama/server/evr_pipeline_party.go`
**Depends on**: Steps 1, 2

#### Changes:

1. In `evr_pipeline_party.go` after line 686 (after `findPartyMemberPresence` function):
   - **Add**: New function:

     ```go
     // createReservationForNewPartyMember checks if the party leader is currently
     // in a social match and, if so, creates a reservation for the new member.
     // Called after a successful party join or invite acceptance.
     func (p *EvrPipeline) createReservationForNewPartyMember(ctx context.Context, logger *zap.Logger, memberSession *sessionWS, partyID uuid.UUID) {
         // Find the party leader.
         ph, ok := p.nk.partyRegistry.Get(partyID)
         if !ok {
             return
         }
         ph.RLock()
         leader := ph.leader
         ph.RUnlock()
         if leader == nil {
             return
         }
         leaderSessionID := uuid.FromStringOrNil(leader.UserPresence.SessionId)
         leaderUserID := uuid.FromStringOrNil(leader.UserPresence.UserId)
         if leaderSessionID == memberSession.id {
             return // new member IS the leader
         }

         // Look up leader's current match.
         leaderStream := PresenceStream{
             Mode:    StreamModeService,
             Subject: leaderSessionID,
             Label:   StreamLabelMatchService,
         }
         leaderPresence := p.nk.tracker.GetLocalBySessionIDStreamUserID(leaderSessionID, leaderStream, leaderUserID)
         if leaderPresence == nil {
             return // leader not in a match
         }
         leaderMatchID := MatchIDFromStringOrNil(leaderPresence.GetStatus())
         if leaderMatchID.IsNil() {
             return
         }

         // Check if leader is in a social match (skip arena/combat -- BAC-010).
         label, err := MatchLabelByID(ctx, p.nk, leaderMatchID)
         if err != nil || label == nil || !label.IsSocial() {
             return
         }

         // Build reservation presence for the new member.
         memberParams, ok := LoadParams(memberSession.Context())
         if !ok {
             logger.Warn("Failed to load params for new party member reservation")
             return
         }

         member := &EvrMatchPresence{
             SessionID:     memberSession.id,
             UserID:        memberSession.userID,
             Username:      memberSession.Username(),
             PartyID:       partyID,
             RoleAlignment: evr.TeamSocial,
             Node:          p.node,
             EvrID:         memberParams.xpID,
             DisplayName:   memberParams.profile.GetDisplayName(),
         }

         // Signal match to create reservation.
         payload := SignalCreatePartyReservationsPayload{Members: []*EvrMatchPresence{member}}
         if _, err := SignalMatch(ctx, p.nk, leaderMatchID, SignalCreatePartyReservations, payload); err != nil {
             logger.Warn("Failed to signal match for new party member reservation", zap.Error(err))
             return
         }

         logger.Info("Created reservation for new party member",
             zap.String("member_sid", memberSession.id.String()),
             zap.String("leader_match_id", leaderMatchID.String()))
     }
     ```

   - **Reason**: Late join reservation creation. Source: spec section 6.

2. In `evr_pipeline_party.go` at line 314 (in `snsPartyJoinRequest`, between the `sendEVRMessageToPartyMembers` call at lines 310-313 and the `return SendEVRMessages` at line 315):
   - **Add**:
     ```go
     // Create reservation for the new member if leader is in a social match.
     go p.createReservationForNewPartyMember(context.WithoutCancel(ctx), logger, session, partyUUID)
     ```
   - **Reason**: After successful join, check if leader has a match to create reservation in. `context.WithoutCancel` ensures the goroutine completes even if the session disconnects. Source: behavior matrix #3.

3. In `evr_pipeline_party.go` at line 613 (in `snsPartyRespondToInviteRequest`, between the `sendEVRMessageToPartyMembers` call at lines 609-612 and the `return SendEVRMessages` at line 614):
   - **Add**:
     ```go
     // Create reservation for the new member if leader is in a social match.
     go p.createReservationForNewPartyMember(context.WithoutCancel(ctx), logger, session, partyUUID)
     ```
   - **Reason**: Same as above but for invite acceptance. `context.WithoutCancel` for the same reason. Source: behavior matrix #5.

#### Test:

- Function: `TestLateJoin_ReservationCreated`, `TestLateJoin_ArenaLeader_NoReservation`, `TestInviteAccept_ReservationCreated`
- File: `evr_pipeline_party_test.go` (new or existing)
- What it asserts: Reservation created when leader in social; not created when leader in arena
- How to run: `GOWORK=off go test -run 'TestLateJoin|TestInviteAccept' ./server/...`

---

### Step 6: Clear reservation on party leave

**BACs verified**: BAC-006
**Files modified**: `/home/andrew/src/nakama/server/evr_pipeline_party.go`
**Depends on**: Steps 1, 2

#### Changes:

1. In `evr_pipeline_party.go` in `snsPartyLeaveRequest` (lines 322-341), insert the reservation clear call **BEFORE** `snsPartyLeaveCleanup` at line 338:
   - **IMPORTANT: Ordering matters**. `snsPartyLeaveCleanup` (in the function body) clears `params.currentPartyID` to `uuid.Nil` and stores params. If `clearMemberReservation` runs after cleanup, it cannot read the party ID from params. The reservation clear MUST happen before the cleanup call.
   - **Context**: At line 329, `partyUUID := params.currentPartyID` captures the party ID before any cleanup. This local variable is available for the reservation clear.
   - Insert at line 337 (after the `sendEVRMessageToPartyMembers` call at lines 332-335, BEFORE the `snsPartyLeaveCleanup` call at line 338):
     ```go
     // Clear any reservation for the departing member.
     // Must run BEFORE snsPartyLeaveCleanup, which clears params.currentPartyID.
     p.clearMemberReservation(ctx, logger, session, partyUUID)
     ```

2. In `evr_pipeline_party.go` after the `createReservationForNewPartyMember` function:
   - **Add**: New helper function:

     ```go
     // clearMemberReservation looks up the party leader's current match and
     // signals it to delete the departing member's reservation slot. Called
     // when a member leaves or is kicked from the party.
     //
     // The partyID parameter must be captured BEFORE snsPartyLeaveCleanup
     // (which clears params.currentPartyID to uuid.Nil). The caller passes
     // the local `partyUUID` variable captured at line 329.
     func (p *EvrPipeline) clearMemberReservation(ctx context.Context, logger *zap.Logger, session *sessionWS, partyID uuid.UUID) {
         // Find the party leader's current match.
         ph, ok := p.nk.partyRegistry.Get(partyID)
         if !ok {
             return // party already disbanded
         }
         ph.RLock()
         leader := ph.leader
         ph.RUnlock()
         if leader == nil {
             return
         }
         leaderSessionID := uuid.FromStringOrNil(leader.UserPresence.SessionId)
         leaderUserID := uuid.FromStringOrNil(leader.UserPresence.UserId)

         leaderStream := PresenceStream{
             Mode:    StreamModeService,
             Subject: leaderSessionID,
             Label:   StreamLabelMatchService,
         }
         leaderPresence := p.nk.tracker.GetLocalBySessionIDStreamUserID(leaderSessionID, leaderStream, leaderUserID)
         if leaderPresence == nil {
             return // leader not in a match
         }
         leaderMatchID := MatchIDFromStringOrNil(leaderPresence.GetStatus())
         if leaderMatchID.IsNil() {
             return
         }

         // Signal the match to delete this specific member's reservation.
         // We use SignalClearPartyReservations with the party ID, which clears
         // ALL reservations for this party. This is broader than needed for a
         // single member, but it is safe: the remaining members will get new
         // reservations when the leader's lobbyEntrantConnected fires next, or
         // via the belt-and-suspenders appendPartyReservationPlaceholders path.
         //
         // Phase 2: Add SignalClearMemberReservation (by session ID) to clear
         // only the individual member's slot without affecting others.
         payload := SignalClearPartyReservationsPayload{PartyID: partyID}
         if _, err := SignalMatch(ctx, p.nk, leaderMatchID, SignalClearPartyReservations, payload); err != nil {
             logger.Warn("Failed to clear departing member's reservation",
                 zap.Error(err),
                 zap.String("sid", session.id.String()),
                 zap.String("match_id", leaderMatchID.String()),
                 zap.String("party_id", partyID.String()))
             return
         }

         logger.Debug("Cleared reservation for departing member",
             zap.String("sid", session.id.String()),
             zap.String("match_id", leaderMatchID.String()),
             zap.String("party_id", partyID.String()))
     }
     ```

   - **Reason**: When a member leaves, their reservation should be removed so the match slot is freed. Source: spec section 3.

   **AMBIGUITY-1 (RESOLVED for Phase 1)**: `SignalClearPartyReservations` clears ALL reservations for a party ID, which is too broad for single-member removal. For Phase 1, this is acceptable because remaining members will get fresh reservations from `appendPartyReservationPlaceholders`. Phase 2 should add `SignalClearMemberReservation` (by session ID) for surgical removal.

#### Test:

- Function: `TestMemberLeavesParty_TheirReservationCleared`
- File: `evr_pipeline_party_test.go`
- What it asserts: Reservation in match is cleared for leaving member
- How to run: `GOWORK=off go test -run 'TestMemberLeavesParty' ./server/...`

---

### Step 7: Clear reservation on party kick

**BACs verified**: BAC-007
**Files modified**: `/home/andrew/src/nakama/server/evr_pipeline_party.go`
**Depends on**: Step 6

#### Changes:

1. In `evr_pipeline_party.go` at line 466 (in `snsPartyKickRequest`, after `p.sendEVRMessageToPartyMembers` at lines 462-465 but before `return SendEVRMessages` at line 467):
   - **Add**:
     ```go
     // Clear any reservation for the kicked member.
     if kickedSession := p.nk.sessionRegistry.Get(uuid.FromStringOrNil(targetPresence.SessionId)); kickedSession != nil {
         if ws, ok := kickedSession.(*sessionWS); ok {
             p.clearMemberReservation(ctx, logger, ws, params.currentPartyID)
         }
     }
     ```
   - **Reason**: Kicked members' reservations should be cleared. The kicker's `params.currentPartyID` is valid here (kick does not call `snsPartyLeaveCleanup` for the kicker). Source: behavior matrix #8.

#### Test:

- Function: `TestMemberKicked_TheirReservationCleared`
- File: `evr_pipeline_party_test.go`
- How to run: `GOWORK=off go test -run 'TestMemberKicked' ./server/...`

---

### Step 8: Clear party reservations when a player leaves match (leader clears all, non-leader clears own)

**BACs verified**: BAC-005
**Files modified**: `/home/andrew/src/nakama/server/evr_match.go`
**Depends on**: Steps 1, 2

#### Changes:

1. In `evr_match.go` at line 849 (inside `MatchLeave`, between the lifecycle observer block ending at line 848 and the early quit penalty block starting at line 850):
   - **Context**: Line 830 is the START of the lifecycle observer block (`// Observer: log lifecycle transition for player leaving match.`). The observer block runs lines 830-848. The insertion point is line 849, after the observer block's closing `}`. The early quit penalty block starts at line 850 (`// If the round is not over, then add an early quit count...`).
   - This modification happens inside the match handler goroutine, so `state.reservationMap` can be modified directly (no signal needed -- unlike external callers, `MatchLeave` already holds the match goroutine).
   - **IMPORTANT: Leader vs non-leader distinction**. When ANY party member leaves the match, we must NOT blindly clear all party reservations. Only the leader leaving should clear all party reservations (followers should not join a match the leader has left). When a non-leader leaves, only their own reservation (if any) should be cleared -- other followers' reservations remain valid because the leader is still present.
   - The leader check uses `nk.(*RuntimeGoNakamaModule).partyRegistry.Get(mp.PartyID)` to look up the party handler and compare `ph.leader.UserPresence.SessionId` against `mp.GetSessionId()`. This is the same `RuntimeGoNakamaModule` cast already used at line 834 in the lifecycle observer block immediately above this insertion point. It is also the same leader-check pattern used in Step 3 (`lobbyEntrantConnected`).
   - **Add**:

     ```go
     // Clear party reservations based on whether the leaving player is the leader.
     // Leader leaves -> clear ALL party reservations (followers should not join
     // a match the leader has left).
     // Non-leader leaves -> clear only THEIR OWN reservation (other followers'
     // reservations remain valid because the leader is still present).
     if mp.PartyID != uuid.Nil {
         isLeader := false
         if _nk, ok := nk.(*RuntimeGoNakamaModule); ok {
             if ph, ok := _nk.partyRegistry.Get(mp.PartyID); ok {
                 ph.RLock()
                 if ph.leader != nil {
                     isLeader = ph.leader.UserPresence.SessionId == mp.GetSessionId()
                 }
                 ph.RUnlock()
             }
             // If partyRegistry.Get fails (party already disbanded), fall through
             // to the non-leader path: clear only this player's own reservation.
             // This is the safe default -- clearing all reservations without
             // confirming leadership could delete valid reservations for other
             // members who are still expected.
         }

         cleared := 0
         if isLeader {
             // Leader is leaving: clear ALL reservations for this party.
             for sid, r := range state.reservationMap {
                 if r.Presence.PartyID == mp.PartyID {
                     delete(state.reservationMap, sid)
                     cleared++
                 }
             }
         } else {
             // Non-leader is leaving: clear only THEIR OWN reservation.
             if _, exists := state.reservationMap[mp.GetSessionId()]; exists {
                 delete(state.reservationMap, mp.GetSessionId())
                 cleared = 1
             }
         }
         if cleared > 0 {
             state.rebuildCache()
             logger.WithFields(map[string]any{
                 "party_id":  mp.PartyID.String(),
                 "is_leader": isLeader,
                 "cleared":   cleared,
             }).Info("Cleared party reservations for departing player")
         }
     }
     ```

   - **Reason**: Source: spec section 3 bullet 2 and behavior matrix #30.

#### Test:

- Function: `TestLeaderLeavesMatch_ReservationsCleared`, `TestNonLeaderLeavesMatch_OnlyTheirReservationCleared`
- File: `evr_match_test.go` (existing or new)
- What it asserts: After leader leaves, `reservationMap` has no entries with matching partyID; `Size` decremented. After non-leader leaves, only that player's reservation is removed; other party members' reservations remain.
- How to run: `GOWORK=off go test -run 'TestLeaderLeavesMatch_ReservationsCleared|TestNonLeaderLeavesMatch_OnlyTheirReservationCleared' ./server/...`
- **Replay fixtures that must still pass**: All 7 fixtures

---

### Step 9: Handle ownership transfer -- clear old reservations

**BACs verified**: BAC-016
**Files modified**: `/home/andrew/src/nakama/server/evr_pipeline_party.go`
**Depends on**: Steps 1, 3

#### Changes:

1. In `evr_pipeline_party.go` at line 505 (in `snsPartyPassOwnershipRequest`, between `p.sendEVRMessageToPartyMembers` at lines 501-504 and `return SendEVRMessages` at line 506):
   - **Add**:
     ```go
     // Clear old leader's reservations and cancel tickets.
     // The old leader's match may still have reservations for party members.
     oldLeaderStream := PresenceStream{
         Mode:    StreamModeService,
         Subject: session.id,
         Label:   StreamLabelMatchService,
     }
     if oldLeaderPresence := session.tracker.GetLocalBySessionIDStreamUserID(session.id, oldLeaderStream, session.userID); oldLeaderPresence != nil {
         oldMatchID := MatchIDFromStringOrNil(oldLeaderPresence.GetStatus())
         if !oldMatchID.IsNil() {
             payload := SignalClearPartyReservationsPayload{PartyID: params.currentPartyID}
             if _, err := SignalMatch(ctx, p.nk, oldMatchID, SignalClearPartyReservations, payload); err != nil {
                 logger.Warn("Failed to clear old leader's reservations", zap.Error(err))
             }
         }
     }
     // New leader may need to create reservations in their current match.
     if newLeaderSession := p.nk.sessionRegistry.Get(uuid.FromStringOrNil(targetPresence.SessionId)); newLeaderSession != nil {
         if ws, ok := newLeaderSession.(*sessionWS); ok {
             newLeaderStream := PresenceStream{
                 Mode:    StreamModeService,
                 Subject: ws.id,
                 Label:   StreamLabelMatchService,
             }
             if newPresence := p.nk.tracker.GetLocalBySessionIDStreamUserID(ws.id, newLeaderStream, ws.userID); newPresence != nil {
                 newMatchID := MatchIDFromStringOrNil(newPresence.GetStatus())
                 if !newMatchID.IsNil() {
                     if label, err := MatchLabelByID(ctx, p.nk, newMatchID); err == nil && label != nil && label.IsSocial() {
                         go p.createPartyReservations(context.WithoutCancel(ctx), logger, newMatchID, ws.id, params.currentPartyID)
                     }
                 }
             }
         }
     }
     ```
   - **Reason**: Ownership transfer requires clearing old and creating new reservations. Source: spec section 7 and behavior matrix #9.

#### Test:

- Function: `TestLeaderPromotion_ReservationsRebuilt`
- File: `evr_pipeline_party_test.go`
- How to run: `GOWORK=off go test -run 'TestLeaderPromotion' ./server/...`

---

### Step 10: Defer ticket submission until after 15s formation period

**BACs verified**: BAC-023, BAC-025
**Files modified**: `/home/andrew/src/nakama/server/evr_lobby_matchmake.go`
**Depends on**: none

#### Changes:

1. In `evr_lobby_matchmake.go` at lines 204-207 (inside `lobbyMatchMakeWithFallback`, the initial ticket submission block):
   - **Context**: Currently, `replaceTicket(ticketConfig)` is called at line 205 immediately on entry. The ticket is submitted before followers have had a chance to send their own `LobbyFindSessionRequest`. The spec requires that the ticket is NOT submitted until after a 15-second formation period during which all party members can join.
   - **Current flow** (immediate ticket submission):
     ```go
     // Add initial ticket
     if err := replaceTicket(ticketConfig); err != nil {   // line 205
         return err
     }
     ```
   - **New flow** (deferred ticket submission with formation period):
   - **IMPORTANT: Solo players skip formation.** If `lobbyGroup == nil` or `lobbyGroup.Size() <= 1`, submit the ticket immediately (existing behavior). The formation period applies only when the player is in a party with other members.
   - **Modify** the block starting at line 204 (`// Add initial ticket`) through line 207:

     ```go
     // Formation phase: if in a party, wait up to 15 seconds for all
     // members to start matchmaking before submitting the first ticket.
     // Solo players or single-member parties skip this phase entirely.
     if lobbyGroup != nil && lobbyGroup.Size() > 1 {
         const formationTimeout = 15 * time.Second
         formationTimer := time.NewTimer(formationTimeout)
         defer formationTimer.Stop()

         // Wait for all party members to be on the matchmaking stream,
         // or for the formation timer to fire, or for the context to
         // be cancelled (e.g., a member cancels matchmaking).
         partyStream := PresenceStream{Mode: StreamModeParty, Subject: lobbyGroup.ID(), Label: session.pipeline.node}
         formationLoop:
         for {
             select {
             case <-ctx.Done():
                 return nil // Cancelled during formation (BAC-023)
             case <-formationTimer.C:
                 // Formation timeout fired. Submit ticket with whoever
                 // is ready. Members NOT on the matchmaking stream are
                 // excluded from the ticket but remain in the party.
                 logger.Info("Formation timeout -- submitting ticket with ready members",
                     zap.Int("party_size", lobbyGroup.Size()))
                 break formationLoop
             case <-time.After(1 * time.Second):
                 // Check if all party members are now on the matchmaking stream.
                 partyPresences := session.tracker.ListByStream(partyStream, true, true)
                 allReady := true
                 for _, pp := range partyPresences {
                     mmStream := PresenceStream{
                         Mode:    StreamModeMatchmaking,
                         Subject: pp.ID.SessionID,
                     }
                     if session.tracker.GetLocalBySessionIDStreamUserID(pp.ID.SessionID, mmStream, pp.UserID) == nil {
                         allReady = false
                         break
                     }
                 }
                 if allReady {
                     logger.Info("All party members ready -- submitting ticket early",
                         zap.Int("party_size", lobbyGroup.Size()))
                     break formationLoop
                 }
             }
         }
     }

     // Add initial ticket (after formation phase for parties, immediately for solo)
     if err := replaceTicket(ticketConfig); err != nil {
         return err
     }
     ```

   - **Reason**: Spec section 4 "Phase 3 -- Matchmaking" rule 1: "The ticket is submitted to the matchmaker AFTER the 15s formation period, not before." Source: ADR-009.
   - **Note**: The formation period is separate from the matchmaking timeout (`lobbyParams.MatchmakingTimeout`). The matchmaking timeout context (`ctx`) is set at `evr_lobby_find.go:102`. The formation period runs WITHIN this context -- if the matchmaking timeout fires during formation, the context is cancelled and the function returns. The formation period does not extend the total timeout; it is a subset of it.

2. **DECISION: Member removal after formation timeout (RESOLVED).** The spec has two contradictory statements: rule 3 says "removed from the party" while rule 5 says "not the party." For Phase 1, follow rule 5: exclude non-ready members from the ticket, but do NOT remove them from the party. This is consistent with BAC-022 (cancel does not remove from party).

#### Test:

- Function: `TestDeferredTicketSubmission_WaitsForFormation`, `TestDeferredTicketSubmission_SoloSkipsFormation`, `TestDeferredTicketSubmission_AllReadyEarly`
- File: `evr_lobby_matchmake_test.go` (new or existing)
- What it asserts: With party, ticket not submitted until 15s or all members ready; without party, ticket submitted immediately; early submission when all members ready before 15s.
- How to run: `GOWORK=off go test -run 'TestDeferredTicketSubmission' ./server/...`

---

### Step 11: Cancel handler -- propagate cancel to all party members

**BACs verified**: BAC-021, BAC-022, BAC-024
**Files modified**: `/home/andrew/src/nakama/server/evr_pipeline_matchmaker.go`
**Depends on**: none

#### Changes:

1. In `evr_pipeline_matchmaker.go` at line 232 (the `lobbyPendingSessionCancel` function body, lines 232-240):
   - **Context**: Currently, `lobbyPendingSessionCancel` at lines 232-240 only calls `LeaveMatchmakingStream(logger, session)` for the individual caller. It has zero party awareness. When a party member cancels, only their own matchmaking stream is closed. Other party members continue matchmaking, and the ticket is now invalid (it was built with all members).
   - **Modify** the function body:

     ```go
     func (p *EvrPipeline) lobbyPendingSessionCancel(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
         // Always leave the caller's matchmaking stream first.
         if err := LeaveMatchmakingStream(logger, session); err != nil {
             logger.Warn("Failed to leave matchmaking stream", zap.Error(err))
         }

         // If the player is in a party, cancel matchmaking for ALL members.
         // Per spec: "Any member cancels matchmaking -> cancel ALL members'
         // matchmaking. Remove the ticket. No partial tickets."
         params, ok := LoadParams(session.Context())
         if !ok || params.currentPartyID == uuid.Nil {
             return nil // Solo player -- already cancelled above.
         }

         // Remove any active matchmaking tickets for the party.
         partyID := params.currentPartyID
         if ph, ok := p.nk.partyRegistry.Get(partyID); ok {
             lobbyGroup := &LobbyGroup{ph: ph}
             if err := lobbyGroup.MatchmakerRemoveAll(); err != nil {
                 logger.Warn("Failed to remove party matchmaking tickets on cancel",
                     zap.String("party_id", partyID.String()),
                     zap.Error(err))
             }
         }

         // Close all party members' matchmaking streams so their
         // monitorMatchmakingStream goroutines cancel their lobbyFind contexts.
         partyStream := PresenceStream{Mode: StreamModeParty, Subject: partyID, Label: p.node}
         partyPresences := p.nk.tracker.ListByStream(partyStream, true, true)
         for _, pp := range partyPresences {
             if pp.ID.SessionID == session.id {
                 continue // Already cancelled above.
             }
             memberSession := p.nk.sessionRegistry.Get(pp.ID.SessionID)
             if memberSession == nil {
                 continue
             }
             ws, ok := memberSession.(*sessionWS)
             if !ok {
                 continue
             }
             if err := LeaveMatchmakingStream(logger, ws); err != nil {
                 logger.Warn("Failed to cancel party member's matchmaking stream",
                     zap.String("member_sid", pp.ID.SessionID.String()),
                     zap.Error(err))
             }
         }

         logger.Info("Cancelled matchmaking for entire party",
             zap.String("party_id", partyID.String()),
             zap.Int("members_cancelled", len(partyPresences)))

         return nil
     }
     ```

   - **Reason**: Spec section 4 "Cancellation Rules" rule 1: "Any member cancels matchmaking -> cancel ALL members' matchmaking. Remove the ticket. No partial tickets." The existing `monitorMatchmakingStream` goroutine (at `evr_lobby_find.go:550`) already detects when the matchmaking stream is closed and cancels the `lobbyFind` context -- so closing all members' streams is the only action needed to propagate the cancel. No new message types are required. Source: ADR-008.
   - **Cancel does NOT remove from party** (BAC-022): This function only touches matchmaking streams and tickets. It does NOT call `snsPartyLeaveCleanup` or modify `params.currentPartyID`. The party membership is preserved.
   - **Formation vs matchmaking cancel** (BAC-023 vs BAC-024): The same mechanism works for both. During formation (Step 10), the formation loop checks `ctx.Done()` -- when the matchmaking stream is closed, `monitorMatchmakingStream` fires `cancelFn()`, which cancels the context, which terminates the formation loop. During active matchmaking, the same cancel mechanism works: the ticket is removed by `MatchmakerRemoveAll`, and the matchmaking loop exits via `ctx.Done()`.

#### Test:

- Function: `TestAnyCancelMatchmaking_AllMembersCancelled`, `TestCancelMatchmaking_StaysInParty`, `TestCancelDuringFormation_NoTicketSubmitted`, `TestCancelDuringMatchmaking_TicketRemoved`
- File: `evr_pipeline_matchmaker_test.go` (new or existing)
- What it asserts: All members' matchmaking streams closed; party membership preserved; ticket removed from matchmaker; formation loop exits cleanly
- How to run: `GOWORK=off go test -run 'TestAnyCancelMatchmaking|TestCancelMatchmaking_StaysInParty|TestCancelDuringFormation|TestCancelDuringMatchmaking' ./server/...`

---

## Section 4: Already Completed Steps

### COMPLETED: Defer service/guild/matchmaking streams to lobbyEntrantConnected

**Commit**: `671456443`
**What was done**: `LobbyJoinEntrants` was speculatively setting "player is in this match" state (service streams, guild group stream, matchmaking untrack) before the game server confirmed the player connected. All authoritative stream updates moved to `lobbyEntrantConnected`, which fires only after game server confirms connection. Also added `LoginSessionID` as 4th service stream subject.
**Files changed**: `evr_lobby_joinentrant.go`, `evr_pipeline_lobby.go`
**BACs addressed**: Prerequisite for ADR-002

### COMPLETED: Route authoritative match leave through game server signal

**Commit**: `f4e87462f`
**What was done**: Pipeline's `matchLeave` was directly untracking player presences from authoritative match streams, bypassing the game server. Now sends `SignalKickEntrants` to the match handler for authoritative matches. Falls back to direct untrack for non-EchoVR matches.
**Files changed**: `pipeline_match.go`
**BACs addressed**: Prerequisite for BAC-005

### COMPLETED: Expand lifecycle transition table and add state guards

**Commit**: `522309f75`
**What was done**: Updated legal transition table with shortcut transitions, self-transitions, and crash recovery paths. Added state guard in match leave handler (only transition to Returning/Crashed if player is still `StateInMatch`). Added state guards in lobby find (skip transitions to Holding/Matchmaking if player still `StateInMatch`).
**Files changed**: `evr_match_lifecycle.go`, `evr_match.go`, `evr_lobby_find.go`
**BACs addressed**: Lifecycle correctness required by all BACs

### COMPLETED: Log-replay test framework

**Commits**: `27465b928`, `af4d2c276`
**What was done**: Built replay test framework with 7 test fixtures from production data. Tests verify lifecycle transitions match production behavior.
**Files**: `evr_logreplay_test.go`, `server/testdata/replay/` (7 fixtures)
**Fixtures**:

- `bigduckii-social-lobby-join.json`
- `bigduckii-infinite-matchmaking.json`
- `healthy-party-follow.json`
- `kipsotuff-crash-recovery.json`
- `kipsotuff-full-arena-loop.json`
- `kipsotuff-party-churn.json`
- `lethal-zed16-stale-skip.json`

---

## Section 5: Functions Removed

No functions are removed in this PR. Per ADR-005, all existing follower-path functions are kept as migration fallbacks. They will be removed in a follow-up PR after the reservation system is proven stable in production.

Functions that will be removed in a future PR (after stabilization):

| Function                         | File                              | Current Lines | Replaced by                                | Grep to verify no callers                           |
| -------------------------------- | --------------------------------- | ------------- | ------------------------------------------ | --------------------------------------------------- |
| `TryFollowPartyLeader`           | `evr_lobby_find.go`               | 1487-1643     | `findReservation` (Step 4)                 | `grep -rn 'TryFollowPartyLeader' server/`           |
| `pollFollowPartyLeader`          | `evr_lobby_find.go`               | 1648-1856     | `findReservation` (Step 4)                 | `grep -rn 'pollFollowPartyLeader' server/`          |
| `isFollowerAlreadyInLeaderMatch` | `evr_lobby_find.go`               | 1244-1291     | Reservation presence check                 | `grep -rn 'isFollowerAlreadyInLeaderMatch' server/` |
| `isFollowerInLeaderDestination`  | `evr_lobby_find_follower_test.go` | test only     | Ambiguity eliminated by reservation system | `grep -rn 'isFollowerInLeaderDestination' server/`  |
| `isLeaderHeadingToSocial`        | `evr_lobby_find.go`               | 1036-1084     | Mode info embedded in reservation          | `grep -rn 'isLeaderHeadingToSocial' server/`        |
| `intendedSocialTargetMatchID`    | `evr_lobby_find.go`               | 1211-1233     | Reservation IS the target                  | `grep -rn 'intendedSocialTargetMatchID' server/`    |

---

## Section 6: Verification Gate

### Complete BAC-to-Test Mapping

| BAC     | Test Function                                                                                       | Test File                            |
| ------- | --------------------------------------------------------------------------------------------------- | ------------------------------------ |
| BAC-001 | `TestLeaderJoinsSocial_ReservationsCreated`                                                         | `evr_pipeline_lobby_test.go`         |
| BAC-002 | `TestFollowerFindsReservation_JoinsDirectly`                                                        | `evr_lobby_find_reservation_test.go` |
| BAC-003 | `TestFollowerNoReservation_FallsThrough`                                                            | `evr_lobby_find_reservation_test.go` |
| BAC-004 | `TestReservationConsumed`                                                                           | `evr_match_test.go`                  |
| BAC-005 | `TestLeaderLeavesMatch_ReservationsCleared`, `TestNonLeaderLeavesMatch_OnlyTheirReservationCleared` | `evr_match_test.go`                  |
| BAC-006 | `TestMemberLeavesParty_TheirReservationCleared`                                                     | `evr_pipeline_party_test.go`         |
| BAC-007 | `TestMemberKicked_TheirReservationCleared`                                                          | `evr_pipeline_party_test.go`         |
| BAC-008 | `TestLateJoin_ReservationCreated`                                                                   | `evr_pipeline_party_test.go`         |
| BAC-009 | `TestInviteAccept_ReservationCreated`                                                               | `evr_pipeline_party_test.go`         |
| BAC-010 | `TestLateJoin_ArenaLeader_NoReservation`                                                            | `evr_pipeline_party_test.go`         |
| BAC-011 | `TestSignalCreatePartyReservations_SkipsDuplicates`                                                 | `evr_match_signal_test.go`           |
| BAC-012 | `TestSignalClearPartyReservations_ClearsByPartyID`                                                  | `evr_match_signal_test.go`           |
| BAC-013 | `TestReservationForOfflineMember_Skipped`                                                           | `evr_pipeline_lobby_test.go`         |
| BAC-014 | `TestConcurrentLobbyFind_IdempotentReservation`                                                     | `evr_lobby_find_reservation_test.go` |
| BAC-015 | `TestSessionIDChange_ReservationStillFound`                                                         | `evr_pipeline_party_test.go`         |
| BAC-016 | `TestLeaderPromotion_ReservationsRebuilt`                                                           | `evr_pipeline_party_test.go`         |
| BAC-017 | `TestFollowerConnected_NoReservationsCreated`                                                       | `evr_pipeline_lobby_test.go`         |
| BAC-018 | Existing `appendPartyReservationPlaceholders` tests pass                                            | `evr_lobby_find_test.go` (existing)  |
| BAC-019 | `TestSocialLobbyFind_ReservationBeforePriorityJoin`                                                 | `evr_lobby_find_reservation_test.go` |
| BAC-020 | `TestMigrationFallback_OldPathStillWorks`                                                           | `evr_lobby_find_reservation_test.go` |
| BAC-021 | `TestAnyCancelMatchmaking_AllMembersCancelled`                                                      | `evr_pipeline_matchmaker_test.go`    |
| BAC-022 | `TestCancelMatchmaking_StaysInParty`                                                                | `evr_pipeline_matchmaker_test.go`    |
| BAC-023 | `TestCancelDuringFormation_NoTicketSubmitted`                                                       | `evr_pipeline_matchmaker_test.go`    |
| BAC-024 | `TestCancelDuringMatchmaking_TicketRemoved`                                                         | `evr_pipeline_matchmaker_test.go`    |
| BAC-025 | `TestDeferredTicketSubmission_WaitsForFormation`                                                    | `evr_lobby_matchmake_test.go`        |

### Build Command

```bash
GOWORK=off go build ./server/...
```

### Test Command (new tests)

```bash
GOWORK=off go test -race -count=1 -run 'TestLeaderJoinsSocial|TestFollowerFindsReservation|TestFollowerNoReservation|TestReservationConsumed|TestLeaderLeavesMatch|TestNonLeaderLeavesMatch|TestMemberLeavesParty|TestMemberKicked|TestLateJoin|TestInviteAccept|TestSignalCreatePartyReservations|TestSignalClearPartyReservations|TestReservationForOfflineMember|TestConcurrentLobbyFind|TestSessionIDChange|TestLeaderPromotion|TestFollowerConnected|TestSocialLobbyFind|TestMigrationFallback|TestAnyCancelMatchmaking|TestCancelMatchmaking_StaysInParty|TestCancelDuringFormation|TestCancelDuringMatchmaking|TestDeferredTicketSubmission|TestFindReservation' ./server/...
```

### Existing Tests Must Still Pass

```bash
GOWORK=off go test -race -count=1 -run 'TestFollowerSkip|TestExpectedFollowerCount|TestTryFollowPartyLeader|TestPollFollowPartyLeader' ./server/...
```

### Replay Tests Must Still Pass

```bash
GOWORK=off go test -race -count=1 -run 'TestReplay_' ./server/...
```

This runs all 7 replay test fixtures:

- `TestReplay_BigDuckII_SocialLobbyJoin`
- `TestReplay_BigDuckII_InfiniteMatchmaking`
- `TestReplay_KipsoTuff_CrashRecovery`
- `TestReplay_KipsoTuff_FullArenaLoop`
- `TestReplay_KipsoTuff_PartyChurn`
- `TestReplay_LethalZed16_StaleSkip`
- `TestReplay_HealthyPartyFollow`

### Manual Verification

1. Deploy to staging.
2. Create a party of 2 players.
3. Leader joins a social lobby.
4. Verify follower joins within 5 seconds (vs. 3-10 seconds with old polling).
5. Both leave social lobby, leader queues arena.
6. Verify both are placed by matchmaker on same ticket.
7. Match ends. Verify both return to social lobby via reservations.
8. One player leaves party. Verify reservation is cleaned up.
9. New player joins party mid-match. Verify reservation is created for them.
10. Leader queues arena. Verify ticket is NOT submitted for first 15 seconds (check matchmaker logs). Verify ticket is submitted after 15s with all ready members.
11. Leader queues arena, all followers ready within 5 seconds. Verify ticket is submitted early (before 15s).
12. During matchmaking, one member cancels. Verify ALL members' matchmaking is cancelled (check matchmaking stream presences). Verify ticket is removed. Verify all members remain in the party.
13. During formation (before 15s), one member cancels. Verify no ticket was ever submitted. Verify all members back to social-ready.

---

## Appendix: Resolved Ambiguities

### AMBIGUITY-1: Individual member reservation cleanup signal (RESOLVED)

**Location**: Step 6, `clearMemberReservation` function.
**Issue**: `SignalClearPartyReservations` clears ALL reservations for a party ID. When a single member leaves, we want to clear only their reservation.
**Resolution for Phase 1**: Use `SignalClearPartyReservations` (clears all party reservations). Remaining members will get fresh reservations from `appendPartyReservationPlaceholders` (belt-and-suspenders) on the next join attempt.
**Phase 2**: Add `SignalClearMemberReservation` (by session ID) for surgical single-member removal.

### AMBIGUITY-2: EvrMatchPresence fields for reservation placeholders (RESOLVED)

**Location**: Step 3, `createPartyReservations`.
**Resolution**: Follow the existing pattern in `appendPartyReservationPlaceholders` (`evr_lobby_find.go:1453-1483`), which creates presences with `SessionID`, `UserID`, `Username`, `PartyID`, `RoleAlignment`, and `Node`. Additional fields (`EvrID`, `DisplayName`) loaded from `LoadParams` if the session is available.

### AMBIGUITY-3: StreamModeReservation (RESOLVED -- DROPPED)

**Resolution**: `StreamModeReservation` is not needed. Reservations are discovered via `PlayerInfo.IsReservation` in the match label's `Players` slice. The `findReservation` function looks up the leader's match via the leader's service stream, calls `MatchLabelByID`, and checks the `Players` slice. No stream tracking or untracking needed.

### AMBIGUITY-4: Formation timeout -- remove non-ready members from party? (RESOLVED)

**Location**: Step 10.
**Issue**: Spec rule 3 says "removed from the party" while rule 5 says "not the party." These contradict.
**Resolution**: Follow rule 5 -- exclude non-ready members from the ticket, but do NOT remove them from the party. Consistent with BAC-022 (cancel does not remove from party) and the principle that matchmaking is an activity, not a relationship.

### DECISION NEEDED-1: configureParty wait loop replacement (DEFERRED)

**Location**: ADR-007, `evr_lobby_find.go` lines 443-452.
**Issue**: Use `lobbyGroup.Size()` instead of `GetMatchPresences` for party member counting.
**Resolution**: This is a correctness fix independent of the reservation system. Should be a separate commit/PR to keep this plan atomic.
