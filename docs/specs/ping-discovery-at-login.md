# Spec: Ping Discovery at Login

- **ADR:** 0002
- **Status:** Draft
- **Author:** Metis + sprockee

## Summary

Move game server ping discovery from matchmaking-time to login-time. Split
internal and external IPs into separate ping targets to get per-address
reachability and latency. Pace the requests across a configurable window.

## Global Settings

Added to `EvrPipeline` config (environment variables):

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `PING_DISCOVERY_MAX_MESSAGES` | int | 8 | Max `LobbyPingRequest` messages per login |
| `PING_DISCOVERY_SPREAD_SECONDS` | int | 60 | Window over which to spread the messages |

The client accepts at most **16 endpoints per message** (protocol constant, not
configurable).

## Data Structures

### PingTarget (new, internal)

```go
// PingTarget maps a single pingable address back to its owning server.
type PingTarget struct {
    Address    net.IP       // The IP to ping (placed in Endpoint.ExternalIP)
    Port       uint16       // Server port
    ServerKey  string       // ExternalIP string of the owning server (for reverse lookup)
    IsInternal bool         // True if this address is the server's internal IP
}
```

### AddressReachability (extends LatencyHistory)

The existing `LatencyHistory` keys by IP string. No structural change needed —
internal IPs are simply additional keys. A helper method resolves the best
reachable address for a given server:

```go
// BestAddress returns the lowest-latency reachable address for a server,
// preferring internal if reachable.
func (h *LatencyHistory) BestAddress(externalIP, internalIP string) (ip string, rtt int, ok bool)
```

## Login Flow Changes

### 1. After login success (`loginRequest`)

After sending `LoginSuccess` + `GameSettings`, start a goroutine:

```go
go p.runPingDiscovery(session)
```

### 2. `runPingDiscovery`

```
func (p *EvrPipeline) runPingDiscovery(session *sessionWS):
    1. List all alive game server presences (guild + global streams)
    2. For each server with a valid endpoint:
       a. Add PingTarget{Address: externalIP, IsInternal: false}
       b. If server has an internal IP:
          Add PingTarget{Address: internalIP, IsInternal: true}
    3. Chunk targets into batches of 16
    4. Cap at max_messages batches
    5. For each batch (paced by spread_seconds / max_messages):
       a. Build []Endpoint — each target becomes:
          Endpoint{Internal: 0.0.0.0, External: target.Address, Port: target.Port}
       b. SendEVRMessages(session, true, NewLobbyPingRequest(275, endpoints))
       c. Sleep(spread_seconds / max_messages)
       d. Check session.Context().Done() between sleeps
```

### 3. Response handling (`lobbyPingResponse`)

Minimal changes. The existing handler already:
- Validates against `knownIPs` (which includes both internal and external IPs)
- Stores RTT by IP in `LatencyHistory`
- Handles `ExternalIP.IsUnspecified()` fallback to `InternalIP`

Since we put the target address in the external slot, the response will have it
in `result.ExternalIP` — the existing codepath stores it correctly.

The one addition: **register a reverse map** on the pipeline (or session params)
so that at join time we can resolve "client had 2ms to 192.168.1.5" → "that's
the internal IP of server with external 1.2.3.4".

### 4. `CheckServerPing` changes

Two options:

**Option A (minimal):** Keep `CheckServerPing` but skip it when the latency
history already has fresh entries for all candidate servers. Falls back to the
current behavior only when the cache is cold (player queued before discovery
finished).

**Option B (clean):** Remove `CheckServerPing` entirely. The matchmaker reads
from the warm `LatencyHistory`. If a server has no latency entry, it's treated
as unreachable for this player (same as today's unreachable-servers tracking).

Recommend **Option A** for the initial PR — safer rollout.

### 5. Join-time endpoint assembly

When building the `Endpoint` for `LobbySessionSuccess`:

```
func (p *EvrPipeline) buildJoinEndpoint(session, server) Endpoint:
    extIP := server.Endpoint.ExternalIP
    intIP := server.Endpoint.InternalIP

    // Check if client can reach the internal IP
    if intIP != nil {
        if _, _, ok := latencyHistory.BestAddress(extIP, intIP); ok {
            // Client can reach internal — include it
            return Endpoint{InternalIP: intIP, ExternalIP: extIP, Port: port}
        }
    }

    // Client cannot reach internal (or server has none) — external only
    return Endpoint{InternalIP: nil, ExternalIP: extIP, Port: port}
```

## Rollout

1. **Phase 1:** Add `runPingDiscovery` at login. Keep `CheckServerPing` as
   fallback. Log per-address reachability for observability. No behavior change
   to matchmaking or joins yet.

2. **Phase 2:** Wire `BestAddress` into join-time endpoint assembly. Internal IP
   is only included when the client demonstrated reachability. Monitor for LAN
   players losing internal paths (should not happen — they'll have RTT for both).

3. **Phase 3:** Remove or reduce `CheckServerPing` to a cache-freshness check.
   Matchmaking reads directly from the warm latency history.

## Testing

- **Unit:** `BestAddress` returns internal when both reachable and internal is
  lower; returns external-only when internal unreachable; returns nothing when
  neither reachable.
- **Integration:** Mock session receives N paced `LobbyPingRequest` messages
  with correct endpoint splitting. Verify reverse map correctly associates
  internal addresses with their servers.
- **Manual:** LAN player joins server, verify internal IP path is used.
  Remote player joins same server, verify internal IP is NOT included.

## Wire cost

- 60 servers × 2 addresses = 120 targets
- 120 / 16 = 8 messages (fits default `max_messages`)
- Each message: 8 bytes header + 16 × 12 bytes endpoints = 200 bytes
- Total: ~1.6 KB spread across 60 seconds
