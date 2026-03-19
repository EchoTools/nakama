# Party Group System

## What It Does

The party group system lets players queue for matches together. When players share a party group, they enter matchmaking as a single unit — the matchmaker treats the whole party as one ticket and places them into the same match on the same team.

## How Players Form a Party

Players join a party by setting the same **LobbyGroupName** in their matchmaking settings. This is a shared string identifier — any players with the same LobbyGroupName are in the same party. The server converts this name into a deterministic **PartyID** (a UUID), so every player with the same group name always resolves to the same party.

Parties have a maximum size of **4 players**.

The first player to start matchmaking becomes the **party leader**. Leadership matters because the leader's matchmaking request drives the entire party's ticket.

## What Happens When a Party Queues

### Leader Flow

1. The leader sends a `LobbyFindSessionRequest` to the server.
2. The server calls `JoinPartyGroup()`, which creates or joins the party. Since no one else is in the party yet, this player becomes the leader.
3. If the party is leaving a match (e.g., after a game ends), the server queries that match's player list to figure out how many party members to expect. It then waits (up to 30 seconds, polling every 500ms) for those members to also start matchmaking before proceeding.
4. Once the expected members have joined (or the timeout expires), the leader verifies that all party members are on the matchmaking stream and kicks any that aren't following.
5. The leader submits a single matchmaker ticket that includes all party members. The matchmaker treats this as one group and will place them together.

### Non-Leader Flow

1. A non-leader sends their own `LobbyFindSessionRequest`.
2. The server calls `JoinPartyGroup()` — since the party already exists, they join it and are marked as a non-leader.
3. Instead of matchmaking independently, the non-leader enters a **follow loop** (`PartyFollow`). This loop polls every 3 seconds and watches what the leader is doing:
   - **Leader is still matchmaking**: Keep waiting.
   - **Leader found a match**: Check if the non-leader was also placed in that match (they should be, since they were on the same ticket). If so, done.
   - **Leader is in a social lobby**: Join the leader's lobby directly.
   - **Leader changed**: If leadership transferred to this player, cancel and re-enter as the new leader.

## Party Leadership

- The first player to call `JoinPartyGroup()` becomes the leader.
- If the leader leaves the party (disconnects, exits matchmaking), leadership automatically transfers to the oldest remaining member.
- Leadership changes cancel any pending matchmaking for non-leaders — they get a "party leader has changed" error and need to re-queue.

## Matchmaking Paths

The system has two matchmaking paths based on party size at ticket submission time:

| Situation | Path | Behavior |
|-----------|------|----------|
| Party with 2+ members | Party matchmaker | All members submitted as one ticket via `PartyHandler.MatchmakerAdd()`. Matchmaker places them together. |
| Solo player or party of 1 | Solo matchmaker | Player submitted individually via `session.matchmaker.Add()`. |

This distinction matters because the party path enforces that all members get the same match assignment, while the solo path has no such constraint.

## Social Lobby Behavior

Social lobbies (`ModeSocialPublic`) bypass matchmaking entirely. When the leader is in a social lobby:

- The leader uses a find-or-create flow — join an existing social lobby or create a new one.
- Non-leaders detect (via the follow loop) that the leader joined a social lobby and join it directly, no matchmaker involved.

## Party Lifecycle

| Event | What Happens |
|-------|-------------|
| First player starts matchmaking with a LobbyGroupName | Party created, player becomes leader |
| Additional players start matchmaking with same LobbyGroupName | They join the existing party as non-leaders |
| Leader submits matchmaker ticket | All current party members included in one ticket |
| Match found | All party members placed in the same match |
| Leader leaves/disconnects | Leadership transfers to oldest member; pending matchmaking cancelled |
| Last member leaves | Party destroyed, removed from registry, stream tracking cleaned up |
| Any membership change during matchmaking | Active matchmaker tickets cancelled (ensures stale tickets don't persist) |

## The "Solo Leader" Race Condition (Fixed)

Previously, the leader checked whether other party members had already joined (`lobbyGroup.Size() > 1`) to decide whether to wait. This check was broken: party members don't join the `LobbyGroup` until they submit their own `LobbyFindSessionRequest`, which usually happens *after* the leader has already checked. The leader would see size=1, proceed to the solo matchmaker path, get matched alone, and leave the non-leader stranded.

**Fix**: Instead of checking current group size, the leader now queries the match being left (`GetMatchPresences()`) to count how many players share the same PartyID. It then waits for that many members to join before proceeding. This works because match presence data reflects who was actually in the party during the previous game, regardless of matchmaking timing.

## Storage

Party group membership is stored per-user in the **Matchmaker** storage collection under their matchmaking settings. The `group_id` field contains the LobbyGroupName. This is indexed (`ActivePartyGroupIndex`) for efficient lookups.

Players set their LobbyGroupName through their matchmaking settings — updating the setting is what puts them in or removes them from a party group. Setting it to an empty string means solo queue.
