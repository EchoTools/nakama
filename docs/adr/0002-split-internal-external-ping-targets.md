# ADR 0002: Pair internal and external IPs in the pre-connect ping; probe only external at connect

- **Status:** Accepted
- **Date:** 2026-06-16 (revised 2026-07-03)
- **Relates to:** ADR 0001 (internal slot must be private or empty), issue #465

## Context

A game server registers with two addresses: an `ExternalIP` (routable, public)
and an optional `InternalIP` (a LAN address for clients that share the server's
network). Both travel together in the `Endpoint` wire struct, and the EVR client
uses them to reach the server.

The problem this ADR addresses is **correctness of reachability**, not latency:

1. **A publicly-routable address must never sit in the internal slot.** If the
   internal slot holds an address that is reachable from the public internet, a
   remote client tries it, and when the two addresses disagree about which host
   answers — or one silently blackholes — the client's connect path stalls
   (issue #465, ADR 0001). The internal slot is only meaningful as a private,
   non-routable address; anything else is misconfiguration.

2. **The pre-connect ping must probe both addresses so at least one is valid.**
   A server may be reachable by its external address, its internal address, or
   both, depending on where the client sits. If the ping omits one of them, a
   client that could only reach the omitted address is left with no valid RTT and
   the server looks unreachable.

Note on latency: an earlier framing treated the matchmaking-time ping's ~2-second
cost as the motivating problem. That cost is not an issue and is **not** the
reason for this design. This ADR is about handing the client correct, complete
reachability information — not about shaving seconds off matchmaking.

"Internal" here means an address a remote client on the public internet cannot
route to: RFC1918 (`10/8`, `172.16/12`, `192.168/16`), RFC6598 CGNAT
(`100.64/10`), loopback, and link-local. "RFC1918" is used loosely below as a
shorthand for this whole non-routable set — the operative test is *external
addressability*, per requirement 2.

## Decision

### 1. On server connect, nakama probes only the external IP

When a game server connects — registration validation and the continuous health
check — nakama pings **only** the server's external IP. The internal IP is a LAN
address that nakama itself generally cannot reach, so probing it would produce
spurious "unreachable" failures. All health-check call sites route their target
through a single `serverProbeTarget(endpoint)` helper that returns the external
IP, keeping the invariant in one place.

### 2. At registration, zero an internal IP that is not private

You cannot have both an internal IP and an external IP that are both externally
addressable. At registration, an internal IP that is **not** a private/
non-routable address (RFC1918 / CGNAT / loopback / link-local) is zeroed to
empty; the server still registers and serves via its external IP, and the
operator is warned that their `internal_ip` was misconfigured. A private internal
IP is kept; an empty one stays empty. This is enforced by
`normalizeInternalIP` / `isInternalIP` at the top of the registration handler.

### 3. The pre-connect ping includes both the external and internal IP

The pre-connect ping (login-time discovery, matchmaking-time `CheckServerPing`,
and pre-join validation) sends **one endpoint per server carrying BOTH the
external and the private internal address**:

```
Endpoint{Internal: <private_internal_or_empty>, External: <real_external>, Port: <port>}
```

The client pings both addresses in the pair and reports; at least one comes back
valid regardless of where the client sits. A LAN client reaches the internal
address; a remote client reaches only the external and silently skips the private
internal. Because requirement 2 guarantees the internal slot is private-or-empty,
the internal address is never one that stalls a remote client.

This **reverses** the earlier "split into separate single-address targets"
approach. Splitting each server into two independent ping entries added
complexity (a reverse address→server map, per-address latency keys) to recover
per-address reachability that the paired format already handles: the client
validates the pair and the server hands the same pair back at join time, letting
the client choose at connect.

### 4. Pace login-time discovery with global settings

Login-time discovery pre-warms the reachability cache so matchmaking reads
warm data instead of blocking. The EVR client accepts at most 16 endpoints per
`LobbyPingRequest` message, so the server chunks the fleet and paces the messages:

```yaml
ping_discovery:
  max_messages: 8          # LobbyPingRequest messages per login (16 endpoints each)
  spread_seconds: 60       # spread window — one message every ~7.5s
```

- **`max_messages`** caps the total burst (8 × 16 = 128 servers of headroom).
- **`spread_seconds`** paces the messages to avoid slamming the client's UDP
  send queue on connect.
- Endpoints-per-message is fixed at 16 by the client — not configurable.

## Consequences

- **The internal slot is guaranteed private-or-empty** by the time any endpoint
  reaches a client, so a remote client is never handed a routable address it will
  stall on (issue #465 / ADR 0001).
- **At least one address per server is always valid** in the pre-connect ping and
  the join endpoint, because both are handed to the client and the client picks
  what it can reach.
- **LAN clients get the internal path** without the server having to guess: the
  client validates the pair at connect and uses the internal address when it can
  reach it.
- **The design is simpler than the split.** No reverse address→server map and no
  per-internal-address latency keys; the join endpoint is just the server's
  endpoint with a non-private internal stripped (`buildJoinEndpoint` =
  `pingEndpoint`).
- **The client returns one RTT per paired endpoint, keyed by external IP.** The
  server therefore does not learn a separate internal-address latency. This is an
  accepted trade of the paired format: the client, which knows which address it
  actually reached, makes the choice at connect — nakama does not need per-address
  latency to route correctly.
