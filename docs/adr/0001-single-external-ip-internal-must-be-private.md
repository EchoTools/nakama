# ADR 0001: Single external IP per game server; internal endpoint slot must be private or empty

- **Status:** Accepted
- **Date:** 2026-06-03
- **Issues:** #465 (implements), #469 (supersedes)

## Context

The endpoint sent to a client in `LobbySessionSuccess` carries two address slots — `InternalIP` and `ExternalIP` (`server/evr/match_ping_request.go`). **The EVR client attempts both addresses simultaneously by design.**

Some dual-homed hosts placed a *second public IP* in the `InternalIP` slot. Because that address is publicly **routable**, the client tries it; if it ICMP-rejects or times out, the player gets **"server connection failed."**

We evaluated and rejected the alternatives:

- **Per-client address filtering at join time (#469):** not achievable. The pre-join `LobbyPingResponse` returns a single `PingMilliseconds` for the internal+external *pair* (`server/evr/match_ping_response.go`), not per-address, and the EVR client is closed-source, so the server cannot obtain per-address reachability to filter on.
- **Dropping unreachable addresses from nakama's vantage:** wrong for the internal slot. Nakama is not on a LAN player's network, so it cannot validate the internal IP — and **no-hairpin LAN players legitimately depend on the internal IP** to reach a server on their own network. A nakama-side healthcheck would strip the very address those players need.

The root cause is narrow: a **routable (public) address sitting in the internal slot.** A genuine private (RFC1918) internal address is non-routable, so off-LAN clients fast-fail/skip it and connect via external, while LAN players use it directly.

## Decision

1. **Game servers are supported with a single external IP only.** The external address is the one nakama observes at registration (`session.ClientIP()`), which is inherently the real public address.
2. **The `InternalIP` slot MUST be a genuine private/internal address or empty.** Permitted: RFC1918 (`10/8`, `172.16/12`, `192.168/16`), CGNAT (`100.64/10`), loopback, link-local. A public IP is never permitted in the internal slot.
3. **Enforced at game-server registration** (`server/evr_pipeline_gameserver.go`). A non-private internal IP is normalized to empty (the server still registers and serves via its external IP) with an operator warning. *(Reject-vs-normalize is the one open call — see Consequences.)*
4. **`Endpoint.IsValid()` / `MarshalJSON` treat the internal slot as optional** — external IP + port are sufficient. An empty internal serializes as `0.0.0.0` (the client's "skip this address" value).

## Consequences

- The "routable bad address in the internal slot" failure mode becomes **structurally impossible** — there is no public-but-unreachable address for the client to trip on.
- Remote clients connect via external (the internal is non-routable and skipped); LAN players (no hairpin) keep the internal path.
- Hosts with two *public* IPs must designate one as external; the second is not advertised. This is acceptable — single-external-IP is our supported topology.
- **#469 (per-client endpoint filtering) is unnecessary and closed.** This ADR implements #465.
- **Open call for the implementing PR:** normalize (drop the bad internal, keep the server online — *recommended, no surprise outages*) vs. reject the registration outright (forces config fix but can take a currently-misconfigured live server offline on deploy).
