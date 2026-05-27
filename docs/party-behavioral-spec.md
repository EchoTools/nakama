# Party System Behavioral Specification

**Date:** 2026-05-27
**Status:** Draft — based on operator domain knowledge + production analysis
**Refs:** Issue #456, POR `citadel/teth/artifacts/2026-05-27-party-fix-por.md`

## Context

The party system is entirely server-side. Clients have zero awareness of parties,
leaders, followers, or convergence. A client sends `LobbyFindSessionRequest` and
receives either `LobbySessionSuccess` (with connection info) or
`LobbySessionFailure`. That is the entire client contract.

Parties are coordinated via Discord (party groups). Players say "ready?" on voice
chat, then press the button within 1-2 seconds of each other. The server must
turn N independent `LobbyFindSessionRequest` messages into a coordinated group
experience — same match, same team — with no client-side cooperation.

The social lobby is the staging area. Players must be in the same social lobby
for in-game voice chat. The social lobby is where "ready?" happens.

## Why Parties Are a Giant Race Condition

Every step involves independent clients sending requests at slightly different
times, through different network paths, to a server that must serialize their
state into a consistent outcome. The current code handles this with implicit
state inference (tracker streams, match labels, boolean flags, cycle counters)
rather than explicit state management. The result: 16+ PRs in 6 weeks, each
fixing one race while introducing another.

## The Two Flows

### Flow A: Social Lobby Convergence

**Purpose:** Get all party members into the same social lobby so they have
in-game voice chat and can coordinate.

```
Trigger: Leader hits matchmaking (or any party member hits "play")

Step 1: Server checks — is the whole party in this social lobby?
Step 2: If NO → Server creates or finds a new social lobby
        → Places 5-minute slot reservations for all active party members
        → Moves leader to that lobby
Step 3: Other party members send LobbyFindSessionRequest (or hit play)
        → Server sees their reservation
        → Sends them to the leader's lobby
Step 4: All members are now in the same social lobby
        → In-game voice works
        → Players can coordinate
```

**Rules:**

1. Social lobby full is NORMAL, not an error. Log at info level.
   The reservation system exists to prevent this, but if it happens
   (lobby filled before reservations, reservation expired), the follower
   should wait or be directed to a lobby with space.

2. Followers must already be in a social lobby (or on the main menu)
   to receive the "go to leader's lobby" instruction. If a follower is
   on a transition screen (loading, between states), they CANNOT follow.
   They will be stuck. The server must not send a follow instruction to
   a client that can't act on it.

3. The social lobby is a staging area, not a destination. Players don't
   care which specific social lobby they're in — they care that they're
   all in the SAME one.

### Flow B: Arena/Combat Matchmaking

**Purpose:** Place all party members into the same competitive match on the
same team.

```
Trigger: All party members are in the same social lobby.
         Leader (or all members) send LobbyFindSessionRequest for Arena/Combat.

Step 1: Server receives LobbyFindSessionRequest from party members
Step 2: Non-leaders enter a HOLDING PATTERN (waiting for leader's ticket)
Step 3: Grace period: 10 seconds
        → Wait for all known party members to have sent their request
        → Poll every 200ms: has party size reached expected?
Step 4: Leader submits ONE matchmaking ticket with all present members
Step 5: Matchmaker finds a match
Step 6: ALL party members are told to join (LobbySessionSuccess)
Step 7: All members connect to the game server
        → Slot reservations ensure they have time (30-60s)
Step 8: All members are in the match, on the same team
```

**Rules:**

1. ONE TICKET. The leader submits one matchmaking ticket that includes
   all party members. Followers do NOT submit their own tickets. Followers
   do NOT enter a follow/poll/retry path. There is one ticket and everyone
   is on it.

2. The grace period (10 seconds) waits for all party members to register
   their intent. The real-world timing is: someone says "ready?" and
   everyone clicks within 1-2 seconds. 10 seconds is massive headroom.

3. If a member doesn't register within the grace period, the leader
   submits the ticket with whoever IS present. Late arrivals enter the
   degraded path (see "Late Arrivals" below).

4. The matchmaker places the ENTIRE party as a unit. Team assignment
   (`repairSplitTicketTeams`) ensures all party members are on the
   same team.

## Ticket Immutability

**Once a matchmaking ticket is submitted, it is IMMUTABLE.**

You cannot add a player to an in-flight ticket. The matchmaker has already
started evaluating it. Adding a member would change the party size, skill
range, and matching criteria mid-evaluation.

**If a new member needs to be added after ticket submission:**

1. CANCEL the existing ticket
2. CANCEL all members' matchmaking (they will see an error)
3. Submit a NEW ticket with the full party
4. Members retry (the error is expected and recoverable)

**A visible retry is better than a silent split.**

The current code tries to avoid ticket cancellation by having late arrivals
"follow" the leader into whatever match the leader was placed in. This is
the entire FOLLOWING / FOLLOW_POLLING / RELEASED code path. It exists because
the system tries to be clever about not disrupting the existing ticket.
The correct answer is: cancel and rebuild.

## Late Arrivals

A "late arrival" is a party member who sends their `LobbyFindSessionRequest`
after the leader's grace period has expired and the ticket is already submitted.

### Current Behavior (broken)

```
Late arrival → TryFollowPartyLeader
  → Is leader in a match? Try to join it.
  → Match full? Enter pollFollowPartyLeader (retry loop)
  → Retry budget exhausted? Release to solo matchmaking
  → Player is now solo. Party is split. Player doesn't know.
```

### Correct Behavior

```
Late arrival → Check: is there an active ticket for this party?
  → YES: Cancel the ticket. Notify all members. Rebuild with everyone.
  → NO (leader already placed): 
    → Is the match joinable? Join it (slot reservation should exist).
    → Match full? This is a genuine failure. 
      → Create a new social lobby convergence. Try again next round.
      → Do NOT release to solo Arena/Combat matchmaking.
```

The key principle: **a party member should NEVER be silently released to
solo matchmaking.** If the party can't stay together, the correct response
is to regroup in social — not to queue the player alone into a competitive
match where they'll face opponents without their teammates.

## Leader Management

### Leader Selection
The first party member to join the party group becomes the leader.
If the leader leaves, the oldest remaining member is promoted.

### Leader Inactivity
If the leader does not hit matchmaking within X seconds (TBD — needs
tuning based on real social lobby session times):
1. Eject the leader from the leader role
2. Promote the next member
3. Party continues with new leader
4. Ex-leader remains in the party but is now a follower

### Leader Rejoins After Ticket
If the ex-leader (or any player) hits matchmaking after a ticket is
already submitted:
1. They join the party as a follower
2. They are NOT on the current ticket
3. Per ticket immutability: cancel and rebuild, or wait for next round

## State Machine

### Per-Member States

```
IDLE                 Not in any party or matchmaking flow
SOCIAL_CONVERGING    Heading to leader's social lobby (reservation active)
SOCIAL_READY         In leader's social lobby, waiting for matchmaking
HOLDING              Sent LobbyFindSessionRequest, waiting for leader's ticket
MATCHMAKING          On the leader's matchmaking ticket (only leader is "active")
JOINING              Match found, connecting to game server (reservation active)
IN_MATCH             In the match, playing
RETURNING            Match ended, heading back to social lobby
CRASHED              Disconnected, reconnect reservation active (27s on Quest)
```

### Per-Party Derived State

The party state is a function of member states:

```
FORMING       At least one member in SOCIAL_CONVERGING
STAGED        All members in SOCIAL_READY
QUEUING       Leader in MATCHMAKING, others in HOLDING
PLACING       All members in JOINING
ACTIVE        All members in IN_MATCH
RETURNING     At least one member in RETURNING
SPLIT         Members in incompatible states (some IN_MATCH, some RELEASED)
```

### Legal Transitions (per-member)

```
IDLE → SOCIAL_CONVERGING          JoinPartyGroup, reservation created
SOCIAL_CONVERGING → SOCIAL_READY  Joined leader's social lobby
SOCIAL_READY → HOLDING            Sent LobbyFindSessionRequest (non-leader)
SOCIAL_READY → MATCHMAKING        Leader submits ticket (leader only)
HOLDING → MATCHMAKING             Leader's ticket includes this member
MATCHMAKING → JOINING             Match found, join started
JOINING → IN_MATCH                Successfully connected to game server
JOINING → SOCIAL_READY            Join failed — return to social, regroup
IN_MATCH → RETURNING              Match ended naturally
IN_MATCH → CRASHED                Client disconnected
RETURNING → SOCIAL_READY          Back in social lobby, ready for next round
CRASHED → IN_MATCH                Reconnected within 27s (reservation honored)
CRASHED → IDLE                    Reconnect failed (reservation expired)
```

### Illegal Transitions (must be prevented)

```
HOLDING → IN_MATCH                Can't join match without being on a ticket
SOCIAL_CONVERGING → MATCHMAKING   Can't matchmake without being in social lobby
MATCHMAKING → IDLE                Can't silently drop from matchmaking
JOINING → IDLE                    Can't silently abandon a join attempt
IN_MATCH → MATCHMAKING            Can't matchmake while in a match
```

### Transitions That Should NOT Exist

```
FOLLOWING (eliminated)            One ticket model — no need to follow
FOLLOW_POLLING (eliminated)       One ticket model — no need to poll
RELEASED (eliminated)             Never silently release to solo
```

The FOLLOWING / FOLLOW_POLLING / RELEASED states exist in the current code
because the system allows the leader to go ahead without the full party.
With proper ticket immutability and the 10-second grace period, these
states are unnecessary. If the party can't get on one ticket, the answer
is to cancel and regroup — not to have followers chase the leader.

## Social Lobby Convergence Rules

1. When any party member hits matchmaking, the server ensures a social
   lobby exists with reservations for all active party members.

2. Reservations last 5 minutes. If a member doesn't join within 5 minutes,
   the reservation expires and their slot is released.

3. If the reserved social lobby is full (reservations exhausted or raced),
   the server creates a new lobby and moves the party there.

4. Social lobby convergence is a PRECONDITION for matchmaking, not a
   concurrent process. The party must be socially converged before the
   leader can submit a ticket.

5. "Socially converged" means: all active party members are in the same
   social lobby, confirmed by presence tracking.

## Error Handling

### Errors That Are Normal
- Social lobby full (info log, find another)
- Matchmaking ticket cancelled due to late arrival (expected retry)
- Grace period expired with partial party (submit with who's here)

### Errors That Are Failures
- Join failed: match full AFTER reservation was created (reservation bug)
- Join failed: match not found (match terminated during join)
- Player stuck on transition screen (server sent instruction client can't execute)

### Errors That Are Bugs
- Player on solo ticket while party is active (ticket should include party)
- Party members on different teams (repairSplitTicketTeams failed)
- Player released to solo matchmaking (should never happen silently)
- Follower in poll loop (shouldn't be in follow path at all)

## Production Baseline (May 25-26)

From lifecycle reconstructor analysis of 5,603 parties:

| Outcome | Count | Rate |
|---------|-------|------|
| CONVERGED | 2,670 | 47.7% |
| SPLIT | 98* | 1.7% |
| ABANDONED | 2,835 | 50.6% |

*Undercount — raw logs show ~238 failed priority joins across 112 users.
Correlator misses ~60% of splits due to orphaned follower events.

Target: SPLIT rate < 0.1% (fewer than 6 per day at current volume).

## Implementation Priority

1. **Grace period 3s → 10s** (one line, immediate impact)
2. **Social lobby full → info not warn** (log level change)
3. **Embed state machine as observer** (log transitions, catch illegal ones)
4. **Ticket cancellation on late arrival** (the real fix)
5. **Eliminate FOLLOWING/POLLING path for Arena/Combat** (simplification)
6. **Add RETURNING state** (prevent false "leader disappeared" during match transition)
7. **Behavioral AC tests** against transition rules above
