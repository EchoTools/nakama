# ADR 0002: Split internal and external IPs into separate ping targets; discover at login

- **Status:** Proposed
- **Date:** 2026-06-16
- **Supersedes:** The 16-endpoint matchmaking-time ping in `CheckServerPing`
- **Relates to:** ADR 0001 (internal slot must be private or empty)

## Context

The current ping flow has two problems:

1. **No per-address reachability.** The `Endpoint` wire format bundles `InternalIP`
   and `ExternalIP` into a single struct. The EVR client pings the pair and returns
   one RTT. The server cannot tell which address the client actually reached. ADR 0001
   noted this limitation and deferred per-client filtering (#469) as not achievable
   with the paired format.

2. **Ping happens at matchmaking time.** `CheckServerPing` fires when the player
   requests a match, sending up to 16 endpoints. This adds latency to the
   matchmaking flow and only covers a fraction of available servers. Players who
   queue immediately after login hit a cold cache.

Meanwhile, the EVR client handles at most **16 endpoints per `LobbyPingRequest`
message**, but accepts multiple messages. With ~60 game servers and two addresses
per server (~120 targets), 8 messages cover the full fleet.

## Decision

### 1. Split internal and external into separate ping targets

For each game server with both an internal and external IP, send **two** `Endpoint`
entries instead of one:

```
Endpoint{Internal: 0.0.0.0, External: <real_external>, Port: <port>}
Endpoint{Internal: 0.0.0.0, External: <real_internal>, Port: <port>}
```

The internal IP is placed **in the external slot** of its own entry. The client
pings whatever is in the external slot — it doesn't know or care whether the
address is public or private. It sends a UDP packet and measures RTT.

- If the client is on the same LAN: both entries return an RTT.
- If the client is remote: only the real external returns an RTT; the private
  address times out silently.

The server maintains a reverse map from address → server so it can reassemble the
correct `Endpoint` at join time: include the internal IP only if the client
returned an RTT for it.

This completely sidesteps the client behavior unknown from ADR 0001 — there is no
`0.0.0.0` in the external slot, no reliance on client-side skip logic, and no
closed-source behavior to guess about.

### 2. Discover at login, not at matchmaking time

Move ping discovery from `CheckServerPing` (matchmaking-time) to the login flow.
On login success, begin sending `LobbyPingRequest` messages for **all alive game
servers**, paced across a configurable window.

By the time the player enters matchmaking (typically 30-60s after login), the
reachability map is fully warm. The matchmaking-time ping becomes a cache check,
not a blocking network round-trip.

### 3. Pace with global settings

```yaml
ping_discovery:
  max_messages: 8          # PingRequest messages per login (16 endpoints each)
  spread_seconds: 60       # spread window — one message every ~7.5s
```

- **`max_messages`** caps the total burst. 8 × 16 = 128 targets (64 servers ×
  internal + external). Default covers current fleet with headroom.
- **`spread_seconds`** paces the messages to avoid slamming the client's UDP
  send queue on connect.
- **`endpoints_per_message`** is fixed at 16 by the client — not configurable.

Servers without an internal IP consume one slot instead of two.

## Consequences

- **Per-address reachability becomes known.** The server can make an informed
  decision about whether to include a server's internal IP at join time, instead
  of sending both and hoping.
- **#469 (per-client endpoint filtering) becomes achievable.** This ADR provides
  the reachability data that ADR 0001 said we could not obtain.
- **Matchmaking latency drops.** No blocking ping at match time — the cache is
  warm from login.
- **LAN players get optimal routing.** If a client can reach a server's internal
  IP at 2ms vs external at 35ms, the join endpoint uses the internal address.
- **Wire cost is trivial.** 8 messages × ~200 bytes = ~1.6KB total, spread
  across 60 seconds.
- **Latency history keying changes.** Currently keyed by external IP. Must change
  to key by address (any IP string), with a reverse map to the owning server.
  Existing latency history entries remain valid — external IPs are still tracked;
  internal IPs are additive.
- **The hardcoded `>= 16` cap in `CheckServerPing` is removed** in favor of the
  paced discovery system. `CheckServerPing` may be retained as a lightweight
  cache-freshness check for servers not yet pinged.
