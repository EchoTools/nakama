# Spec: Reservation-Based Party System

## Problem

The current party follow system uses tracker stream reads to determine where the leader is, then followers poll/chase the leader. The tracker streams can be stale, ambiguous (can't distinguish "leaving" from "staying"), and race-prone. This has caused:

- Infinite matchmaking (BigDuckII 2026-06-18)
- False-positive convergence (players told they're "already in leader's match" when they're not)
- Failed production deploy attempting to fix the above

The `isFollowerInLeaderDestination` pure function and its skipped test (`evr_lobby_find_follower_test.go:57`) document the exact ambiguity: when `followerMatchID == leaderMatchID == currentMatchID`, the system cannot distinguish "leaving this match" from "already in this match."

## Principle

**Reservations, not tracker reads.** The leader explicitly reserves slots for party members. Followers search for their reservation, not the leader's location. No ambiguity, no stale data, no race conditions.

**Game server is the authority.** "Player is in match" is true only when the game server confirms it (via `lobbyEntrantConnected`). All state flows from game server events.

---

## Existing Infrastructure -- Ground Truth

### Reservation Data Structures

**`slotReservation`** (`evr_match_label.go:22-25`):

```go
type slotReservation struct {
    Presence *EvrMatchPresence
    Expiry   time.Time
}
```

**`reconnectReservation`** (`evr_match_label.go:27-32`):

```go
type reconnectReservation struct {
    Presence     *EvrMatchPresence
    Expiry       time.Time
    UserID       string
    DeferPenalty bool
}
```

**`EvrMatchPresence`** (`evr_match_presence.go:17-41`) -- the presence stored in reservations contains: `SessionID`, `UserID`, `EvrID`, `PartyID`, `RoleAlignment`, `Username`, `DisplayName`, `Node`, `SupportedFeatures`, and more.

**`EntrantMetadata`** (`evr_match.go:209-212`):

```go
type EntrantMetadata struct {
    Presence     *EvrMatchPresence
    Reservations []*EvrMatchPresence
}
```

- `Presences()` method (`evr_match.go:218-220`) returns `[primary, ...reservations]`.
- `ToMatchMetadata()` (`evr_match.go:222-233`) serializes to `map[string]string{"entrants": "<json>"}` for `MatchRegistry.JoinAttempt`.

### Storage Location

Reservations live on `MatchLabel` (the match's authoritative in-memory state), not in any external store:

- `reservationMap map[string]*slotReservation` -- keyed by **session ID** (`evr_match_label.go:67`)
- `reconnectReservations map[string]*reconnectReservation` -- keyed by **user ID** (`evr_match_label.go:68`)
- Both initialized in `MatchInit` (`evr_match.go:166-167`)

### Size Computation -- Reservations Already Counted

`rebuildCache()` (`evr_match_label.go:381-596`) computes `Size` as:

```
Size = len(actual presences) + len(active slot reservations) + len(active reconnect reservations)
```

- **Line 424**: `s.Size = len(presences)` where `presences` includes all three categories.
- **`PlayerCount`** (lines 430-435) counts only actual presences from `presenceMap` (not reservations). This is intentional: search queries show real occupancy, while `Size` reflects reserved capacity.
- **`OpenSlots()`** (`evr_match_label.go:234-236`): `MaxSize - Size` -- automatically reduces available slots when reservations exist.
- **`Players` slice** includes reservation presences with `IsReservation: true` flag (line 469).

### Reservation Consumption in MatchJoinAttempt

`MatchJoinAttempt` (`evr_match.go:243-578`) processes reservations in this order:

1. **Line 272**: `rebuildCache()` -- clean expired reservations before any decisions.
2. **Lines 349-361**: Remove reservations for party members already in the match.
3. **Lines 408-428**: **Consume slot reservation BEFORE capacity check** -- `LoadAndDeleteReservationRaw(sessionID)` (line 416), fallback to `LoadAndDeleteReservationByUserIDRaw(userID)` (line 419). If found, inherits `PartyID` and `RoleAlignment` from the reservation.
4. **Lines 430-438**: Define `restoreSlotReservation()` closure for rollback on failure.
5. **Lines 441-455**: Capacity check against `OpenSlots()`. If over capacity despite a valid reservation, returns `ErrJoinRejectReasonReservationViolated` and restores the reservation.
6. **Lines 519-538**: Create new slot reservations for `meta.Reservations` entries:
   - 15-second expiry for arena/combat (`time.Second * 15`)
   - 5-minute expiry for social lobbies (`time.Minute * 5`)

### Reservation Expiry

Two expiry paths:

1. **`MatchLoop`** (`evr_match.go:1243-1281`): Every tick, iterates both maps and deletes expired entries. For `reconnectReservation` with `DeferPenalty`, applies early quit penalty on expiry.
2. **`rebuildCache()`** (`evr_match_label.go:403-421`): Inline cleanup of expired entries during cache rebuild.

### Existing Reservation Lookup Methods

All on `MatchLabel` (`evr_match_label.go:81-147`):

| Method                                        | Key          | Returns                   | Used By                                |
| --------------------------------------------- | ------------ | ------------------------- | -------------------------------------- |
| `LoadAndDeleteReservation(sessionID)`         | session ID   | `*EvrMatchPresence, bool` | Not currently used in MatchJoinAttempt |
| `LoadAndDeleteReservationByUserID(userID)`    | user ID scan | `*EvrMatchPresence, bool` | Not currently used in MatchJoinAttempt |
| `LoadAndDeleteReservationRaw(sessionID)`      | session ID   | `*slotReservation, bool`  | `MatchJoinAttempt` line 416            |
| `LoadAndDeleteReservationByUserIDRaw(userID)` | user ID scan | `*slotReservation, bool`  | `MatchJoinAttempt` line 419            |

All four delete the entry on hit and call `rebuildCache()`.

### How Reservations Are Currently Created

Three creation paths exist today:

1. **`LobbyJoinEntrants`** (`evr_lobby_joinentrant.go:78-81`): `entrants[0]` is the primary; `entrants[1:]` become `EntrantMetadata.Reservations`. These are processed by `MatchJoinAttempt` at lines 519-538. Reservations get 15s expiry (arena) or 5min expiry (social).

2. **`appendPartyReservationPlaceholders`** (`evr_lobby_find.go:1439-1475`): For social lobbies only, adds placeholder `EvrMatchPresence` entries for online party members not yet in the entrants list. Called at `evr_lobby_find.go:279` during the leader's `lobbyFind`. These placeholders flow through `LobbyJoinEntrants` as reservations.

3. **Match signal handler** (`evr_match.go:1768-1774`): When a match is configured via `SignalPrepareSession`, `settings.Reservations` are stored directly into `reservationMap`. Used for matchmaker-placed matches and post-match social lobby allocation (`evr_match.go:2188-2189`, 5-minute lifetime).

### `lobbyEntrantConnected` -- Current State

`evr_pipeline_lobby.go:13-144`. This is the game server's "player connected" confirmation. **Currently has zero party logic.** It:

1. Looks up each entrant's presence by entrant ID (line 36)
2. Updates four service stream presences (SessionID, LoginSessionID, UserID, EvrID) with the match ID as status (lines 57-59)
3. Updates guild group stream and removes matchmaking stream presences (lines 62-68)
4. Tracks on the match authoritative stream to trigger `MatchJoin` (lines 72-78)
5. Transitions lifecycle to `StateInMatch` for non-social matches (lines 86-89)
6. Sends accept/reject messages to the game server (lines 95-143)

No party member lookup, no reservation creation, no follower pulling.

### Current Follower Path -- What Gets Replaced

The follower path in `lobbyFind` (`evr_lobby_find.go:113-250`) currently:

1. **`isFollowerInActiveMatch`** (line 119, defined at 1090-1112): Checks if follower is in an arena/combat match via tracker. Returns early to prevent yanking them out.

2. **Lifecycle transition to `StateHolding`** (lines 125-127): Observer-only, no actual blocking.

3. **Late arrival ticket cancellation** (lines 141-145): If party has an active ticket without this session, cancels it.

4. **`isLeaderInArenaCombatMatch`** (line 154, defined at 1295-1337): If leader is in arena/combat, redirects follower to social. Uses tracker reads (matchmaking stream + service stream + MatchLabelByID).

5. **`TryFollowPartyLeader`** (line 168, defined at 1481-1637): The primary follow attempt. Uses 5 separate tracker reads to find leader's match, check if follower is already there, validate the match label. Only works for `ModeSocialPublic` matches (defense-in-depth at line 1601).

6. **`pollFollowPartyLeader`** (line 214, defined at 1640-1848): 3-second polling loop with an inner `isFollowerInLeaderMatch` closure that makes 2 tracker reads per iteration plus optional `MatchLabelByID`. Has `maxNonJoinableCycles = 3` timeout for non-social matches.

**All of these functions read the leader's tracker presence to determine where the leader is.** This is the exact pattern this spec eliminates.

### Party Management

- **`LobbyGroup`** (`evr_lobby_group.go:11-14`): Wraps `PartyHandler`. Methods: `ID()`, `GetLeader()`, `List()`, `Size()`.
- **Leader determination**: `lobbyGroup.GetLeader()` returns `*rtapi.UserPresence`. Leader is `PartyHandler.leader` (read under `RLock`), fallback to `expectedInitialLeader`.
- **Party join**: `JoinPartyGroup` (`evr_lobby_group.go:121-204`) calls `partyRegistry.GetOrCreateByGroupName`, joins if not member, tracks on party stream. Sets `lobbyParams.PartyID` from `lobbyGroup.ID()`.
- **Party stream**: `PresenceStream{Mode: StreamModeParty, Subject: partyUUID, Label: node}`. Enumerating members: `tracker.ListByStream(stream, true, true)`.
- **`params.currentPartyID`**: Set in `snsPartyTrackAndJoin` (`evr_pipeline_party.go:169`). Stored in `SessionParameters`. Cleared in `snsPartyLeaveCleanup` (`evr_pipeline_party.go:182-183`).
- **Sending messages to party**: `sendEVRMessageToPartyMembers` (`evr_pipeline_party.go:129`) lists stream presences and sends to each.

### Match Lifecycle States

`evr_match_lifecycle.go:20-49`:

```
StateIdle -> StateSocialConverging -> StateSocialReady -> StateHolding -> StateMatchmaking -> StateJoining -> StateInMatch -> StateReturning -> StateSocialReady (loop)
                                                                                                             |
                                                                                                       StateCrashed -> StateInMatch (reconnect) or StateIdle
```

Legal transitions are defined in `legalTransitions` (lines 86-121). Observer mode only -- illegal transitions are logged but applied.

---

## Design

### 0. Reservation Lifecycle — Event-Driven, No Polling

Reservations are created and cleared by concrete events, never by polling:

| Event                                    | Action                                                                                                                                        |
| ---------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
| Player joins party                       | Create reservation in leader's current match (if leader is in one)                                                                            |
| Leader's game server confirms connection | Create reservations for all party members not yet in the match                                                                                |
| Player's game server confirms connection | Reservation consumed (`LoadAndDeleteReservation` in `MatchJoinAttempt`)                                                                       |
| Player leaves party                      | Clear their reservation from leader's match                                                                                                   |
| Player disconnects/crashes               | Crash handler clears their reservation (same path as solo player crash reservations via `MatchLoop` expiry or `reconnectReservation` cleanup) |
| Leader leaves match                      | All party reservations in that match cleared                                                                                                  |
| Leader leaves party                      | All party reservations in leader's match cleared                                                                                              |

No event requires polling. Every state change is triggered by something that already happens in the system.

### 1. Leader Joins a Match -> Creates Reservations

**Trigger point**: `lobbyEntrantConnected` (`evr_pipeline_lobby.go:13`).

When the game server confirms a player is connected:

1. Look up the player's `SessionParameters` via `LoadParams(s.Context())` to get `currentPartyID`.
2. If `currentPartyID != uuid.Nil`, check if this player is the party leader via the party stream.
3. If leader, enumerate party members via `tracker.ListByStream(partyStream, true, true)`.
4. For each party member (excluding the leader):
   a. Check if the member has a `StreamModeMatchmaking` presence (they are currently matchmaking). If yes: they are waiting -- create a reservation AND signal them via `MatchSignal` or direct join. They will be pulled in by the matchmaker or pick up the reservation on their next find attempt.
   b. If the member is NOT matchmaking: create a reservation in this match for them. Their next `LobbyFindSessionRequest` will find it.
5. Reservations are created by calling `MatchSignal` with the list of member presences, which inserts them into `state.reservationMap` inside the match handler (single-threaded, no races).

**New function** -- add to `evr_pipeline_lobby.go`:

```
func (p *EvrPipeline) createPartyReservations(ctx context.Context, logger *zap.Logger, matchID MatchID, leaderSessionID uuid.UUID, partyID uuid.UUID)
```

- Enumerates party members from the party stream
- Builds `[]*EvrMatchPresence` placeholders for each non-leader member
- Sends a new `SignalCreatePartyReservations` signal to the match with the member list
- The match handler inserts them into `reservationMap` with 5-minute expiry

**New signal** -- add to `evr_match.go` signal handler:

```
case SignalCreatePartyReservations:
    // Receives list of EvrMatchPresence, creates slotReservation for each
```

**Why signal, not direct write**: `MatchLabel` is the match handler's state, accessed only within the match goroutine. External code must use `MatchSignal` to modify it safely. This is the existing pattern (`evr_match.go:1768-1774`).

### 2. Follower Matchmakes -> Searches for Reservation

**Trigger point**: `lobbyFind` (`evr_lobby_find.go:31`), early in the follower path (before line 113).

When a follower sends `LobbyFindSessionRequest`:

1. Before any of the current follower logic (`isFollowerInActiveMatch`, `TryFollowPartyLeader`, `pollFollowPartyLeader`), check: does a reservation exist for this player in any match?
2. **New function** -- add to `evr_lobby_find.go`:

```
func (p *EvrPipeline) findReservation(ctx context.Context, logger *zap.Logger, session *sessionWS, lobbyParams *LobbySessionParameters) (MatchID, bool)
```

This function queries for a reservation matching this session ID or user ID. Implementation options:

- **Option A -- Tracker-based**: The leader's `lobbyEntrantConnected` stores a "reservation pointer" on a new stream mode for the reserved player (e.g., `StreamModeReservation` with `Subject: memberSessionID`). The follower reads this stream to find their reservation match ID. Lightweight, no match registry scan.
- **Option B -- Signal-based scan**: Query the match registry for the leader's current match (from the leader's service stream), then check that specific match for a reservation. This is a targeted lookup, not a broad scan.

**Recommended**: Option A. Create a new stream mode `StreamModeReservation`. When `createPartyReservations` creates a reservation, it also tracks a presence for each reserved member on `PresenceStream{Mode: StreamModeReservation, Subject: memberSessionID}` with the match ID as status. The follower reads this single stream entry to find where their reservation is. When the reservation is consumed or cleared, untrack the stream.

3. If reservation found -> join that match directly via `lobbyJoin` (bypass all follow/poll logic).
4. If no reservation found -> enter a simplified hold state. The `lobbyEntrantConnected` handler will create one when the leader joins, and the follower will find it on the next poll cycle (or can be pushed via a notification if implemented).

The follower **never reads the leader's tracker service stream** to figure out where the leader is. The reservation IS the signal.

### 3. Social Lobby Reservations Persist

**Already partially implemented.** Current behavior:

- `appendPartyReservationPlaceholders` (`evr_lobby_find.go:1439-1475`) creates placeholder presences for party members when the leader joins a social lobby.
- These flow through `LobbyJoinEntrants` -> `MatchJoinAttempt` and get 5-minute expiry (`evr_match.go:532-534`).
- `rebuildCache()` includes them in `Size`, so `OpenSlots()` is reduced.

**What changes**:

1. Reservation creation for social lobbies also moves to `lobbyEntrantConnected`, but `appendPartyReservationPlaceholders` remains as a **belt-and-suspenders** for the initial join (before the game server confirms). The `lobbyEntrantConnected` reservations replace/refresh them once the game server confirms.

2. Reservation clearing rules (already partially handled by expiry):
   - **Reserved player joins** -> consumed by `LoadAndDeleteReservationRaw` in `MatchJoinAttempt` (already works).
   - **Leader leaves the lobby** -> `MatchLeave` already fires, but does not currently clear slot reservations for party members. **New behavior needed**: when the leader leaves, clear all `reservationMap` entries whose `Presence.PartyID` matches the leader's party ID. Also untrack `StreamModeReservation` for those members.
   - **Leader leaves the party** -> `snsPartyLeaveCleanup` (`evr_pipeline_party.go:175`). **New behavior**: look up leader's current match, signal it to clear party reservations.
   - **Reserved player leaves the party** -> `snsPartyLeaveCleanup`. **New behavior**: look up the match where their reservation exists (via `StreamModeReservation`), signal it to clear that specific reservation.

3. **Lobby full -> relocation**: If the social lobby is genuinely full and the leader starts matchmaking, the leader + party members who are matchmaking get a new lobby. Reservations for non-matchmaking members are created in the new lobby. This is already handled by the leader's `lobbyFind` path -- `newLobby` creates a new social lobby with `settings.Reservations` containing all entrants (`evr_lobby_find.go:604-611`).

### 4. Three-Phase Timeout Model

The current system uses a single long `MatchmakingTimeout` (30-420s) for everything — party formation, lobby convergence, AND finding opponents. This conflates local operations (your party members are online) with pool-dependent operations (finding opponents). The result: followers hold for minutes when they should resolve in seconds.

**Phase 1 — Party Formation (15 seconds)**

When the leader starts matchmaking:

1. Party members have 15 seconds to start matchmaking (send their own `LobbyFindSessionRequest`).
2. Members who are matchmaking within the timeout are included in the ticket.
3. Members who are NOT matchmaking when the timeout fires are **excluded from the ticket** (not removed from the party). The leader proceeds without them on the ticket. They stay in the party group and in their social lobby.
4. This is a hard cutoff — if you're not ready in 15 seconds, the ticket goes without you.

**Phase 2 — Social Lobby Convergence (10 seconds)**

When the leader joins a social lobby:

1. Party members have 10 seconds to find their reservation and join the lobby.
2. If the lobby is full, the leader relocates to a new lobby within this window. Reservations for orphaned members are created in the new lobby.
3. Members who don't arrive within 10 seconds remain in the party but are not blocking anything — the reservation persists until expiry (5 minutes for social) or until they join.

**Phase 3 — Matchmaking (existing timeout)**

Once the party is formed (Phase 1 complete) and all matchmaking members are on the ticket:

1. The ticket is submitted to the matchmaker AFTER the 15s formation period, not before.
2. The normal matchmaker timeout applies. This is "finding opponents," not "assembling the party."

**Cancellation Rules**

1. **Any member cancels matchmaking** → cancel ALL members' matchmaking. Remove the ticket. No partial tickets. This is simple because there's no polling — cancel is a single operation.
2. **Cancel does NOT remove from party.** The party is the social relationship. Matchmaking is the activity. Cancelling the activity doesn't end the relationship. The cancelled members stay in the party group and in the social lobby.
3. **Cancel during formation (before ticket submitted)** → same as above. Formation stops. Everyone goes back to social-ready state.
4. **Cancel during matchmaking (after ticket submitted)** → ticket removed from matchmaker. Everyone goes back to social-ready state.
5. **Party membership vs matchmaking membership:** Players join the PARTY GROUP when they set the group name in-game (social relationship). They join the TICKET when they queue for arena/combat (matchmaking activity). The 15s formation timeout removes them from the ticket, not the party. Cancel removes everyone from the ticket, not the party.
6. The matchmaker timeout is appropriately long because it depends on the player pool.

**Key principle**: party formation and convergence timeouts are SHORT (10-15s) because they're local operations between online players. Matchmaking timeout is LONG because it depends on external factors. These are three distinct phases, not one combined timeout.

### 5. Follower Matchmakes Before Leader

**Trigger**: Follower sends `LobbyFindSessionRequest`, `findReservation` returns false.

1. No reservation exists → follower enters `StateHolding`.
2. **Simplified hold**: poll the `StreamModeReservation` stream on a simple interval (much cheaper than the current 4+ tracker reads per iteration).
3. When the leader joins a match, `lobbyEntrantConnected` fires → `createPartyReservations` creates a reservation → the follower's poll finds it → joins.
4. **Bounded by Phase 1 timeout (15s)**: if no reservation appears within the party formation timeout, the follower is excluded from the ticket and matchmakes independently. They stay in the party group.

### 6. Party Member Not Matchmaking With Leader

When the leader starts matchmaking (arena/combat):

1. The Phase 1 formation timeout (15s) starts.
2. Members who are in an active arena/combat match will not start matchmaking within 15 seconds.
3. When the formation timeout fires, these members are **excluded from the ticket** (not removed from the party). The ticket goes without them.
4. When their match ends, they can rejoin the party fresh (late join flow → finds reservation if leader has created one).

This replaces the current `isFollowerInActiveMatch` skip-but-stay behavior. Removing them is cleaner — the party membership always reflects who is actually participating.

### 7. Late Join

**Trigger**: New member joins party via `snsPartyJoinRequest` (`evr_pipeline_party.go:253`) or `snsPartyRespondToInviteRequest` (`evr_pipeline_party.go:510`) while leader is already in a match.

1. After `snsPartyTrackAndJoin` succeeds, look up the leader's current match via the leader's `StreamModeService` presence.
2. If leader is in a match: signal the match to create a reservation for the new member. Track `StreamModeReservation` for the new member.
3. New member's next `LobbyFindSessionRequest` -> `findReservation` finds it -> joins.

**New code in `snsPartyJoinRequest`** and `snsPartyRespondToInviteRequest`:

```
func (p *EvrPipeline) createReservationForNewPartyMember(ctx context.Context, logger *zap.Logger, memberSession *sessionWS, partyID uuid.UUID)
```

- Find the party leader from the party stream
- Read leader's `StreamModeService` presence to get match ID
- If leader is in a social match, signal the match to create a reservation
- Track `StreamModeReservation` for the new member

### 8. Party Leader Removal

The leader must be removed from the party (not just demoted) when:

1. **Leader disconnects** -- session gone, can't lead. A new leader is elected from remaining members. The new leader inherits responsibility for reservations: clear the old leader's reservations, create new ones based on the new leader's current match (if any).
2. **Leader is kicked/suspended** -- enforcement action. Same as disconnect: remove, elect, re-reserve.
3. **Leader's match is terminated** (game server crash, server shutdown) -- leader is no longer in a match. Reservations in that match are now invalid. Clear them. The leader returns to matchmaking and the cycle restarts.

### 9. Party Membership Changes -> Cancel Matchmaking

**Any change to party membership MUST cancel all active matchmaking tickets AND update reservations.**

Matchmaking tickets are immutable -- they contain the exact set of players. Reservations reflect the current party. Both must stay in sync with membership.

Triggers and actions:

- **Member joins the party** -> cancel ticket, rebuild with new member. Add reservation for new member in leader's current match (if any).
- **Member leaves the party** -> cancel ticket, rebuild without them. Delete their reservation from leader's current match (if any). The slot opens for other players.
- **Member is kicked from the party** -> same as leave.
- **Leader changes** (disconnect, kick, promotion) -> cancel ticket, new leader rebuilds. Clear old leader's reservations. New leader creates reservations based on their current match.

The invariant: the set of reservations in the leader's current match ALWAYS equals the set of party members who are not yet in that match. No stale reservations, no missing reservations. Every party change updates both the ticket and the reservations atomically.

This is already partially implemented (`cancelTicketForLateArrival` in `evr_lobby_find.go:1350`, `MatchmakerRemoveAll` on `LobbyGroup` at `evr_lobby_group.go:69`, `SignalTicketRebuild` at `evr_lobby_group.go:99`). The reservation system makes this cleaner: the leader always owns the ticket and the reservations. When membership changes, the leader cancels and recreates both.

---

## Gap Resolutions (from behavior matrix review)

Decisions on the 8 gaps flagged in `party-behavior-matrix.md`:

**#64 — Reverse follow (leader joins follower's social lobby):** No-op. If the leader joins a lobby the follower is already in, the follower is already there. No reservation needed.

**#65 — `lobbyFindOrCreateSocial` tracker-read priority join:** Replace with `findReservation`. The secondary tracker-read path in `lobbyFindOrCreateSocial` (lines 785-888) should use the reservation system, not read the leader's service stream.

**#66 — Leader wait loop counting match presences:** Remove the match-presence counting entirely. The leader operates off party membership (`lobbyGroup.Size()`) only. The party stream is the source of truth for who's in the party. The match's `GetMatchPresences` gives wrong counts when followers have already left (documented in `TestExpectedFollowerCount_FromPartyStream`).

**#68 — `monitorMatchmakingStream` goroutine for followers:** Whatever is most elegant. Followers using `findReservation` may not need the matchmaking stream monitor — the reservation check is their cancel/proceed mechanism.

**#69 — `lobbyEntrantConnected` accessing `currentPartyID`:** Reuse existing code. The session lookup chain (`sessionRegistry.Get(sessionID)` → `LoadParams(s.Context())` → `currentPartyID`) already has precedent in the party handlers. No new patterns needed.

**#70 — Post-match social lobby allocation interaction:** No-op or reservation refresh. `allocatePostMatchSocialLobby` already creates reservations before anyone joins. `lobbyEntrantConnected` just refreshes/extends them when the leader connects. No conflict.

**#71 — Crash interaction with party reservations:** The crash handler should NOT create a reconnect reservation in the current (dying) match. The goal is to get to the leader, not stay in the match. The leader's social lobby reservation is the right destination. If the party member crashes and the leader has already created a reservation for them in the social lobby, that reservation persists. When the crash reconnect window expires, the player reconnects and finds the leader's reservation.

---

## Match Label Size Reporting

**Change from current behavior.** Currently `rebuildCache()` includes reservations in `Size` implicitly. This hides information — consumers can't tell if `Size = 8` means 8 real players or 5 players + 3 reservations.

**New explicit fields:**

- `PlayerCount` — real players connected to the game server (already exists, `evr_match_label.go:430-435`)
- `ReservationCount` — slots held for party members not yet arrived (new field)
- `OpenSlots()` = `MaxSize - PlayerCount - ReservationCount` (modified)

Consumers:

- "Can someone join?" → check `OpenSlots()`
- "How many people are playing?" → `PlayerCount`
- "Total committed capacity?" → `PlayerCount + ReservationCount`
- Search queries for backfill → use `PlayerCount` (real occupancy)
- Join gate → use `OpenSlots()` (real + reserved)

`Size` should equal `PlayerCount` only. Reservations are separate. No implicit math.

---

## Implementation Details

### New Stream Mode

Add `StreamModeReservation` to the stream mode constants (likely in `evr_pipeline.go` or wherever `StreamModeService`, `StreamModeMatchmaking`, etc. are defined).

```
StreamModeReservation = <next available uint8>
```

**Presence format**:

- Stream: `PresenceStream{Mode: StreamModeReservation, Subject: memberSessionID, Label: StreamLabelMatchService}`
- Status: match ID string (same format as service stream)
- Tracked by: the pipeline on behalf of the leader
- Untracked when: reservation consumed, cleared, or expired

### New Signal: SignalCreatePartyReservations

Add `SignalCreatePartyReservations` to signal opcodes (defined with other `Signal*` constants).

Signal payload: JSON array of `*EvrMatchPresence` entries for reservation.

In the match signal handler (`evr_match.go`, within the existing `switch` on signal opcode):

```
case SignalCreatePartyReservations:
    var members []*EvrMatchPresence
    // unmarshal from signal data
    for _, m := range members {
        if _, exists := state.presenceMap[m.GetSessionId()]; exists {
            continue // already in match
        }
        if _, exists := state.reservationMap[m.GetSessionId()]; exists {
            continue // already reserved
        }
        state.reservationMap[m.GetSessionId()] = &slotReservation{
            Presence: m,
            Expiry:   time.Now().Add(5 * time.Minute),
        }
    }
    state.rebuildCache()
```

### New Signal: SignalClearPartyReservations

Add `SignalClearPartyReservations` for cleanup scenarios.

Signal payload: party ID (uuid string).

```
case SignalClearPartyReservations:
    partyID := uuid.FromStringOrNil(signalData)
    for sid, r := range state.reservationMap {
        if r.Presence.PartyID == partyID {
            delete(state.reservationMap, sid)
        }
    }
    state.rebuildCache()
```

### Modified Functions

| Function                         | File:Line                     | Change                                                                              |
| -------------------------------- | ----------------------------- | ----------------------------------------------------------------------------------- |
| `lobbyEntrantConnected`          | `evr_pipeline_lobby.go:13`    | After accepting entrants, call `createPartyReservations` for leader entrants        |
| `lobbyFind` (follower branch)    | `evr_lobby_find.go:113-250`   | Add `findReservation` check before existing follower logic; if found, join directly |
| `snsPartyJoinRequest`            | `evr_pipeline_party.go:253`   | After successful join, call `createReservationForNewPartyMember`                    |
| `snsPartyRespondToInviteRequest` | `evr_pipeline_party.go:510`   | Same as above                                                                       |
| `snsPartyLeaveRequest`           | `evr_pipeline_party.go:322`   | Clear reservation for departing member                                              |
| `snsPartyKickRequest`            | `evr_pipeline_party.go:430`   | Clear reservation for kicked member                                                 |
| `MatchLeave`                     | `evr_match.go:660+`           | When leader leaves, clear all party reservations                                    |
| `MatchSignal`                    | `evr_match.go` signal handler | Add `SignalCreatePartyReservations` and `SignalClearPartyReservations` cases        |

### Functions That Become Unnecessary

These functions exist solely to support the tracker-read follow pattern. With reservations as the coordination mechanism, they are no longer needed for the primary flow. They can be **deprecated and eventually removed** once the reservation path is proven stable.

| Function                         | File:Line                             | Reason                                                 |
| -------------------------------- | ------------------------------------- | ------------------------------------------------------ |
| `TryFollowPartyLeader`           | `evr_lobby_find.go:1481-1637`         | Replaced by `findReservation`                          |
| `pollFollowPartyLeader`          | `evr_lobby_find.go:1640-1848`         | Replaced by simple reservation poll                    |
| `isFollowerAlreadyInLeaderMatch` | `evr_lobby_find.go:1238-1285`         | Reservation presence check is authoritative            |
| `isFollowerInLeaderDestination`  | `evr_lobby_find_follower_test.go:199` | The ambiguity it documents is eliminated               |
| `isLeaderHeadingToSocial`        | `evr_lobby_find.go:1030-1078`         | Mode info can be embedded in reservation stream status |
| `intendedSocialTargetMatchID`    | `evr_lobby_find.go:1205-1227`         | Reservation IS the intended target                     |

### Functions That Stay

| Function                              | File:Line                     | Reason                                                    |
| ------------------------------------- | ----------------------------- | --------------------------------------------------------- |
| `cancelTicketForLateArrival`          | `evr_lobby_find.go:1350-1401` | Still needed for ticket immutability                      |
| `appendPartyReservationPlaceholders`  | `evr_lobby_find.go:1439-1475` | Keep as belt-and-suspenders for initial social lobby join |
| `currentSocialLobbyForSession`        | `evr_lobby_find.go:1139-1197` | Fast-path no-op detection for re-sent requests            |
| `LoadAndDeleteReservationRaw`         | `evr_match_label.go:118-127`  | Core reservation consumption in MatchJoinAttempt          |
| `LoadAndDeleteReservationByUserIDRaw` | `evr_match_label.go:133-147`  | Fallback for session ID changes                           |

**Removed from "keep" list:**

- `isFollowerInActiveMatch` — replaced by the Phase 1 formation timeout (15s). Members not matchmaking within the timeout are excluded from the ticket (not the party). No need to check if they're in an active match.
- `isLeaderInArenaCombatMatch` — the reservation system handles this: leader joins arena → creates reservations → followers find them. The mode information is in the reservation, not a separate tracker check.

**Also removed (per #66 gap resolution):**

- Leader wait loop using `GetMatchPresences` to count followers — replaced by `lobbyGroup.Size()` from the party stream. The party stream is the source of truth for who's in the party.

---

## Edge Cases

### 1. Session ID Change (Follower Disconnects and Reconnects)

A follower may disconnect and reconnect with a new session ID. The reservation in the match is keyed by the old session ID.

**Already handled**: `LoadAndDeleteReservationByUserIDRaw` (`evr_match_label.go:133-147`) scans by user ID as a fallback. However, the `StreamModeReservation` presence is also keyed by the old session ID. When the follower reconnects, the pipeline must re-track the reservation stream with the new session ID, OR the reservation lookup must also check by user ID.

**Recommendation**: When a player reconnects and re-joins the party, `snsPartyTrackAndJoin` fires. At that point, check if a reservation exists for their user ID (via `StreamModeReservation` on the old session ID tracked by the leader). If so, update the stream to the new session ID.

### 2. Leader Leaves Match Before Followers Arrive

Leader joins social lobby -> reservations created -> leader leaves before followers arrive.

**Current behavior**: Reservations expire (5-minute timeout). Followers attempting to join get rejected (match may have new players occupying the reserved slots if expiry ran).

**With reservation system**: When the leader leaves, `MatchLeave` fires. If the leader has party members with active reservations, signal the match to clear those reservations (`SignalClearPartyReservations`). Untrack `StreamModeReservation` for those members. Followers' `findReservation` will return false, and they will enter the hold state. When the leader joins a new match, the cycle restarts.

### 3. Multiple Leaders (Leader Promotion Mid-Flow)

If the leader is promoted to a different player mid-matchmaking (via `snsPartyPassOwnershipRequest` at `evr_pipeline_party.go:471`):

1. Old leader's reservations should be cleared in their current match.
2. New leader needs to create reservations in THEIR current match (if they have one).
3. Active matchmaking tickets must be cancelled (already handled by ticket rebuild).

### 4. Race: Follower Joins Before `lobbyEntrantConnected` Fires

Timeline:

1. Leader calls `LobbyJoinEntrants` -> match handler adds leader + creates initial reservations via `EntrantMetadata.Reservations`.
2. Follower's `findReservation` runs before `lobbyEntrantConnected` fires.

**Not a problem**: Reservations are already created in step 1 via `MatchJoinAttempt` (lines 519-538). The `lobbyEntrantConnected`-based reservation creation is an additional/refresh path for members who were NOT included in the initial `LobbyJoinEntrants` call (e.g., they were not matchmaking yet when the leader joined). The `StreamModeReservation` tracking happens at that point.

For the initial join, `appendPartyReservationPlaceholders` (`evr_lobby_find.go:1439-1475`) already creates placeholder reservations in the entrants list. These flow through `MatchJoinAttempt` and create reservations immediately, before `lobbyEntrantConnected` fires.

### 5. Game Server Crash

If the game server crashes:

1. Match ends -> all presences removed, reservations destroyed.
2. Service stream presences for all players become stale until untracked.
3. `StreamModeReservation` presences become stale.

**Solution**: When a player's match terminates (detected by `MatchLeave` or session disconnect handling), untrack their `StreamModeReservation` entries. The follower's `findReservation` will return false, and they will re-enter the hold state.

### 6. Party Size > Available Slots

If the party has 4 members but the social lobby only has 2 open slots:

- Current behavior: `TryFollowPartyLeader` checks `OpenPlayerSlots() < requiredSlots` and either gives up or polls.
- With reservations: `createPartyReservations` should only create reservations for members that fit. If the lobby is full, the leader should initiate relocation to a new lobby. This is already handled by `lobbyFind`'s social lobby path -- when the leader matchmakes from a full lobby, `newLobby` creates a new social lobby with all entrants.

### 7. Reservation Created for Offline Member

If a party member's session has died but they have not been cleaned up from the party stream yet:

- `createPartyReservations` will create a reservation for a dead session.
- The reservation will expire (5 minutes) without being consumed.
- **Mitigation**: Before creating a reservation, check `sessionRegistry.Get(memberSessionID)` returns a live session. If nil, skip that member.

### 8. Concurrent lobbyFind Calls

The follower's client may re-send `LobbyFindSessionRequest` while a previous `lobbyFind` is still running (this happens on the client's normal message cycle). Currently handled by `isFollowerAlreadyInLeaderMatch` fast-path at line 72.

With reservations: `findReservation` is idempotent (read-only query). If a reservation is found, `lobbyJoin` will either succeed (first call) or be rejected as `ErrJoinRejectReasonDuplicateJoin` and treated as a no-op (`evr_lobby_joinentrant.go:95-97`).

---

## Migration Notes

### In-Flight Parties During Deploy

During a rolling deploy:

1. **Old server processes**: Will continue using the tracker-read follow pattern. No `StreamModeReservation` presences exist.
2. **New server processes**: Will try `findReservation` first, find nothing (old leaders don't create reservation streams), and fall through to the existing follow path (which should be preserved as fallback during the transition period).

**Strategy**: Keep the existing follow path (`TryFollowPartyLeader`, `pollFollowPartyLeader`) as a fallback behind the `findReservation` check. During deploy:

```go
if matchID, found := p.findReservation(ctx, logger, session, lobbyParams); found {
    // New path: join via reservation
    return p.lobbyJoin(ctx, logger, session, lobbyParams, matchID)
}
// Fallback: old tracker-read path (remove after stabilization)
if p.TryFollowPartyLeader(ctx, logger, session, lobbyParams, lobbyGroup) {
    return nil
}
```

### Rollback Safety

If the reservation system causes issues:

1. Remove the `findReservation` check from `lobbyFind`.
2. Remove the `createPartyReservations` call from `lobbyEntrantConnected`.
3. The existing follow path remains intact.
4. `StreamModeReservation` presences will expire naturally (session disconnect cleanup).
5. Match signal handlers for new signals are no-ops if never called.

No data migration needed -- all state is in-memory (tracker streams, match labels).

---

## Testing

Each scenario above is a test case. Tests must exercise the actual reservation flow, not isolated functions with mocks.

### Existing Test Coverage

| Test File                             | What It Covers                                                                                                                               | Status                                       |
| ------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------- |
| `evr_lobby_follow_test.go`            | `TryFollowPartyLeader` and `pollFollowPartyLeader` -- 40+ tests covering every early return path, timeout, leadership change, duo desync bug | **Covers the OLD path being replaced**       |
| `evr_lobby_find_follower_test.go`     | `isFollowerInLeaderDestination` -- 8 tests documenting the TOCTOU ambiguity, including the skipped test at line 57                           | **Documents the problem this spec solves**   |
| `evr_lobby_configure_party_test.go`   | `configureParty` -- follower not on matchmaking stream, party stream cleanup                                                                 | **Relevant, needs expansion**                |
| `evr_reservation_integration_test.go` | Reservation lifecycle with classification, cleanup, preemption, no-show auto-vacate                                                          | **Covers match-level reservation mechanics** |
| `evr_matchmaker_reservation_test.go`  | Matchmaker-level reservation building and assembly                                                                                           | **Covers matchmaker integration**            |
| `evr_party_system_test.go`            | Party handler creation, join, leave, matchmaker integration                                                                                  | **Covers party mechanics**                   |
| `evr_lobby_capacity_test.go`          | Lobby capacity and slot calculations                                                                                                         | **Covers capacity math**                     |

### New Test Cases

1. **Leader joins social -> reservations created for all party members -> follower finds reservation -> joins**
   - Create party with leader + 2 followers
   - Leader calls `lobbyFind` -> enters social lobby
   - Verify `lobbyEntrantConnected` creates reservations (check `reservationMap` and `StreamModeReservation`)
   - Follower calls `lobbyFind` -> `findReservation` returns true -> joins directly
   - Verify no tracker-read follow path is entered

2. **Leader joins arena (via matchmaker) -> party members on ticket placed by matchmaker -> reservations for non-matchmaking members**
   - Leader + follower in party, both matchmaking
   - Matchmaker places both on same ticket
   - Verify matchmaker handles party grouping (existing behavior)
   - If a third member joins party late (not on ticket), verify reservation is created for them

3. **Follower matchmakes before leader -> holds -> leader joins -> reservation created -> follower finds it**
   - Create party
   - Follower sends `LobbyFindSessionRequest` before leader
   - Verify follower enters `StateHolding`
   - Leader sends `LobbyFindSessionRequest` -> joins social lobby
   - Verify `lobbyEntrantConnected` creates reservation for follower
   - Verify follower picks up reservation and joins

4. **Late join -> reservation created -> follower joins**
   - Leader in social lobby
   - New member joins party via `snsPartyJoinRequest`
   - Verify reservation created in leader's lobby for new member
   - New member's `lobbyFind` finds reservation -> joins

5. **Lobby full -> leader + members move to new lobby with reservations**
   - Fill a social lobby to capacity
   - Leader starts matchmaking -> `newLobby` creates a new lobby
   - Verify reservations for non-matchmaking party members in new lobby

6. **Leader leaves party -> reservations cleared**
   - Leader in social lobby with reservations for 2 followers
   - Leader calls `snsPartyLeaveRequest`
   - Verify all party reservations in the match are cleared
   - Verify `StreamModeReservation` untracked for followers

7. **Reserved player leaves party -> their reservation cleared**
   - Leader in social lobby with reservations
   - One follower calls `snsPartyLeaveRequest`
   - Verify only that follower's reservation is cleared
   - Other follower's reservation remains

8. **Member in active arena match when leader queues -> skipped (not kicked)**
   - Leader + follower in party
   - Follower in active arena match
   - Leader matchmakes
   - Verify follower is skipped (`isFollowerInActiveMatch` returns true)
   - No reservation created for follower in leader's arena match

9. **Session ID change -> reservation still found via user ID fallback**
   - Leader in social lobby with reservation for follower (session A)
   - Follower disconnects and reconnects with session B
   - Follower's `findReservation` uses user ID fallback
   - Verify join succeeds

10. **Leader disconnect -> new leader -> reservations rebuilt**
    - Leader in social lobby with reservations
    - Leader disconnects
    - New leader elected
    - Old reservations cleared
    - New leader's `lobbyEntrantConnected` creates new reservations (if new leader is in a match)

---

## Notes

- This codebase uses zap (not slog) and predates the Go addendum -- new code follows addendum principles within existing project conventions (e.g., `%w` error wrapping, context as first param, functional options, no `interface{}`).
- The `isFollowerInActiveMatch`, `TryFollowPartyLeader`, `pollFollowPartyLeader`, and `isFollowerAlreadyInLeaderMatch` functions become largely unnecessary once reservations are the coordination mechanism. Keep them as fallback during migration, remove after stabilization.
- The `isFollowerInLeaderDestination` pure function and its skipped test document the exact ambiguity this design eliminates.
- The documented SMELL at `evr_lobby_find.go:50-56` (race between leader's match-join and deferred matchmaking stream untrack) is eliminated: followers no longer read the leader's matchmaking stream to determine intent.
- All match state modifications (reservation creation/deletion) go through `MatchSignal`, respecting the match handler's single-goroutine execution model. No direct writes to `MatchLabel` from external goroutines.
- `StreamModeReservation` presences are a lightweight notification mechanism -- they do not carry the full reservation data, just the match ID. The actual reservation lives in the match's `reservationMap`.
