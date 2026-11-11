# ADR 0003: Game servers behind NAT register through a fixed ping port and a runtime hello; players reach them direct, punched, or relayed, chosen by nakama per join

- **Status:** Proposed
- **Date:** 2026-11-11
- **Relates to:** ADR 0001 (internal slot must be private or empty), ADR 0002 (paired internal/external endpoint), the nakama and nevr-runtime issues on game servers behind NAT (links added at filing)
- **Code cited at:** nakama `cb9f062ea`, nevr-runtime `3b13b55e0` (both origin/main). nakama paths are under `server/`; nevr-runtime paths under `src/`.

## Context

A community game server registers over its websocket and announces one UDP port: the broadcaster's bound
port, or the UPnP-mapped port when UPnP is on and the mapping succeeds (nevr-runtime
`runtime/server/gameserver_serverdb.cpp:330`, `:336-359`, `:369`, sent at `:378`; the reconnect path builds its own
envelope at `runtime/server/gameserver_callbacks.cpp:981`, `:1014`, `:1030`). nakama takes the external IP from the
registration request's client IP (`evr_pipeline_gameserver.go:188`): the first `x-forwarded-for` entry when that
header is present, otherwise the socket peer (`api.go:591-597`). An explicit operator `ip:port` overrides it
(`:280-299`).

Before accepting the server, nakama sleeps 2 s (`:351`) and pings the game port up to 3 times, 500 ms timeout,
500 ms pause after a failure (`:358-370`). If no ping is acknowledged, it logs "Broadcaster could not be reached",
DMs the operator and returns `BroadcasterRegistration_Failure` (`:371-379`), which is `result=10` on the wire
(`evr/broadcaster_registration_failure.go:16-26`).

Every ping leaves from a **fresh ephemeral port**: `BroadcasterHealthcheck` dials with `Port: 0`
(`evr_pipeline_gameserver.go:847-850`, `:857`), and each retry calls it again. The 5 s monitor
(`:445-447`, through `BroadcasterRTTcheck`, `:939`, `:957`), the continuous health check (`HealthCheckStart`,
`Port: 0` at `:726`, `DialUDP` at `:740`, started at `:488-490`) and the operator tools
(`evr_discord_appbot.go:1679`, `:1711`) all ping the same way.

### Observed, and expected

**Observed (the only failure seen; from nakama's registration log and the operator's report, not linked here):** on
2026-10-31 one community server behind double NAT with a VPN overlay failed registration with `result=10`. nakama's ping to its game port timed out on every try. A port forward on the
inner router could not help, because the host controls only that layer.

**Everything else in this ADR is expected from how NATs behave, not observed:**

- A NAT that filters unsolicited inbound UDP drops nakama's ping, because nothing inside has sent to that source.
- A NAT that remaps the game's port makes the announced port wrong, and the runtime cannot learn the mapped one.
- A server that did register (for example with the testing-only `novalidation` tag, `evr_pipeline_gameserver.go:31`,
  `:348`, `:381-401`) would still drop every player's first packet. The pre-join ping is expected to fail the same
  way. Failures are logged at `evr_lobby_prejoin_ping.go:415`; the check runs unless configured off
  (`evr_global_settings.go:294`, checked at `evr_lobby_prejoin_ping.go:188`).

### Why a hello alone does not fix registration

Two NAT properties decide reachability (RFC 4787): how the **mapping** is chosen (endpoint-independent,
address-dependent, or address-and-port-dependent, "symmetric" in RFC 3489's older terms), and how inbound packets are **filtered**
(endpoint-independent, address-dependent, or address-and-port-dependent, RFC 3489's "port-restricted").

A runtime that sends a UDP "hello" from the game socket to nakama opens a filter entry for exactly
`nakama_ip:hello_port`. On a port-restricted NAT, nakama's ping arrives from `nakama_ip:<fresh port>`, a remote port
the server never sent to, and is dropped (`evr_pipeline_gameserver.go:849`). A hello therefore helps only on NATs
that filter by address alone. On port-restricted NATs, the ping has to leave from the port the hello was sent to.

### What the player path has to work with

- **The client and the server get different success messages.**
  - The game server gets the protobuf `SNSLobbySessionSuccessV5Message` (`evr_lobby_joinentrant.go:415-436`, sent at
    `:446`). Its server-side runtime decodes it and re-encodes it for the game
    (`runtime/server/gameserver_callbacks.cpp:218-229`).
  - The client gets the game's own binary `SNSLobbySessionSuccessv5` about 150 ms later
    (`evr_lobby_joinentrant.go:463-465`).
- **Neither message has room for relay data.** The protobuf message has no field for relay data or credentials: the
  build site sets every field it has (`:417-434`; nevr-api pinned at `go.mod:8`). The client's frame is a fixed
  binary layout (`evr/match_session_success.go:84-112`). The runtime's translation for older clients rejects a tail of
  any other length (nevr-runtime `runtime/compat/legacy_codec.cpp:830`, used at `:842-850`).
- **The runtime does not read the endpoint.** On the client, the runtime's lobby decoder stops at the group id
  (`runtime/compat/lobby_frame.h:15`).
- **The endpoint is a pair with one port.** It is `{InternalIP, ExternalIP, Port}` (`evr/match_ping_request.go:80-84`).
  The client is handed both addresses and picks one (ADR 0002; `evr_ping_discovery.go:60-71`, `:210-219`).
- **Latency is keyed by external IP.** Latency history is stored and read by external IP alone
  (`evr_latencyhistory.go:265`, `:498`).
- **The game sends from one UDP socket.** It uses plain `sendto`/`recvfrom` on one socket. Anything that has to share
  its NAT mapping must come from that socket (`runtime/combat/veilnet.cpp:16-22`). VeilNet already shows that
  foreign datagrams can be taken out in a `recvfrom` hook before the game sees them (`:1031`, `:1043-1047`).
- **`evr_cgnat.go` is not networking.** It detects shared IPs for alt-account detection (`evr_cgnat.go:18-30`) and
  plays no part here.

## Decision drivers

1. **Fix the observed failure first.** A server behind filtering NAT, double NAT included, must be able to register
   without a port forward.
2. **"Registered" must still mean "playable".** The health check exists to keep unreachable servers out of the pool.
   A server that only nakama can reach must not be offered to players.
3. **No regression for servers that work today.** That covers forwarded, UPnP and public servers, and old runtimes.
4. **Prefer changes that need no player-side update.** PC and Quest clients update slowly, and the Quest send path
   is reconstructed code (`reconstruction/r15/csysnet_send_to.cpp:1-4`).
5. **Every NAT class gets a defined outcome.** Symmetric and CGNAT included: joinable by some path, or refused with a
   message that says why.
6. **Not an open relay or reflector.** Any new UDP port answers only known peers, never more bytes than it received.
7. **Bandwidth spent on relaying is the operator's money.** It should be spent only where nothing else works.

## Considered alternatives

### A. Port forwarding and UPnP only (status quo)

- **What it is:** document port forwarding; the runtime's UPnP path already exists (`gameserver_serverdb.cpp:341-350`).
- **Rejected:** it cannot fix the observed case. With double NAT, the host controls only the inner layer. It does
  nothing for CGNAT or for routers with UPnP off. Kept as the first thing an operator should try, and it continues to
  work unchanged.

### B. Hole punching only: fixed backend ping port, runtime hello, punch toward players

- **What it is:**
  - nakama pings from one fixed port.
  - The runtime sends a hello from the game socket to that port; nakama reads the reflexive (post-NAT) port from it.
  - At join, the server punches toward the player's reflexive address.
- **Strengths:** it fixes registration for every NAT class, because the mapping is learned from the fixed port
  itself. Players cost no bandwidth and take no detour.
- **Rejected as the whole answer:**
  - Punching to a player needs the player's reflexive port. That means a client-side hello from the client's game
    socket, which is a client runtime change on PC and Quest.
  - It fails whenever either side's mapping is address-and-port-dependent.
  - Published measurements: RFC 5128 §4 reports UDP hole punching working for more than 80% of NATs in a 2005
    survey, and Trautwein et al. (arXiv 2510.27500) measure 70% ± 7.1% of attempts succeeding. Our own rate is
    unmeasured.
  - With punching alone, a share of servers and players would have no path at all.

### C. Relay only (TURN-style)

- **What it is:** every player of a NAT'd server goes through a relay.
  - Standard TURN (RFC 8656) puts an allocating TURN client inside the game process: either every player, which is a
    client change, or the server.
  - On the server, the runtime would wrap and unwrap every game datagram in both directions in its `sendto`/`recvfrom`
    hooks, and present each peer's address to the game as the source.
- **Strengths:** it works for every NAT class.
- **Rejected as the whole answer:**
  - Every packet of every relayed match crosses the operator's host, for servers whose NAT would have allowed a
    direct path.
  - Players take a latency detour that matchmaking would then have to absorb.
  - TURN framing on the server's hot path is more runtime surface than the problem needs (see 4 below).

### D. Both, with relay as the fallback (chosen, in the form below)

- **Registration:** alternative B's fixed port and hello.
- **Players:**
  - direct when the server is open;
  - punched when both ends predict it will work;
  - relayed otherwise.
- **Path choice:** nakama chooses per join, before the success messages are sent.

## Decision

### 1. nakama pings from one fixed UDP port; the runtime says hello from the game socket first

- **The ping port.** nakama binds one UDP socket on a fixed port, a global setting
  `broadcaster_ping_port` (proposed default 6790; not 3478, which belongs to STUN).
  - Every ping leaves from that socket: registration (`evr_pipeline_gameserver.go:361`), the monitor (`:447`),
    `HealthCheckStart` (`:718`) and the operator tools (`evr_discord_appbot.go:1679`, `:1711`).
  - `HealthCheckStart` has to move too. It returns on its first read error (`evr_pipeline_gameserver.go:817-822`),
    so on a NAT'd server it would stop after one timeout and record nothing more.
  - The socket is unconnected, with one reader goroutine.
  - Replies are matched by the 8-byte ping token already in the request (`:868-878`), and by source address equal to
    the pinged address. A waiter registers its token before sending.
  - If the bind fails, nakama falls back to an ephemeral port and logs an error; it does not stop registration.
  - The deployment must preserve the source port through any container NAT in front of nakama. That is a deploy-time
    check, captured on the host interface.
- **The hello.**
  - When it is enabled (see 2), the runtime sends a small hello from the game's own UDP socket to
    `nakama:broadcaster_ping_port`, right before each registration envelope (`gameserver_serverdb.cpp:376-378`,
    `gameserver_callbacks.cpp:1030`).
  - The hello carries a magic value, a version byte and the `server_id`. It repeats every 10 s as a keepalive.
  - nakama never answers a hello, so the port is not a reflector.
- **Reading the reflexive port.**
  - nakama keeps hellos for about 30 s, keyed by (source IP, `server_id`).
  - At registration, during the existing 2 s wait (`evr_pipeline_gameserver.go:351`), it accepts a hello only if the
    source IP equals the registration's client IP (`:188`; see Context for how it is resolved).
  - It then sets `externalPort` to the hello's observed source port, logs "reflexive port X (announced Y)", and pings
    from the fixed port.
  - An explicit operator `ip:port` (`:280-299`) bypasses the hello path.

**Why fixed port plus reflection, not STUN to a separate service:**

- The mapping nakama needs is the one toward nakama's own ping port. Learning it from that port is correct even for
  a symmetric NAT, whereas a STUN answer from another port would describe a different mapping.
- No receive path is needed on the game socket.
- No plugin init-order problem arises.

### 2. Classify at registration; a server that only nakama can reach is not offered to players

A server is **open** only if a ping from an address its game socket has never sent to is acknowledged. nakama's
own fresh-port ping cannot prove this once a hello exists, because an address-filtering NAT now admits anything from
nakama's IP. So the probe comes from a **second public address**: the relay host (see 4), on nakama's request.

| Result | Meaning | Registration |
|---|---|---|
| Ping from a never-contacted address is acknowledged | open (forwarded, UPnP, public, full cone) | registered as today |
| Only the fixed-port ping after a hello is acknowledged | `nat-reflexive` | registered with tag `nat-reflexive`; matchmaking and join skip it until a player path (3) exists for it |
| No ping is acknowledged | no UDP path to nakama | `result=10` as today, with a message naming the fixed port and saying a hello was or was not seen |

- **Mapping class.** The runtime also says hello to the relay host. If the two observed source ports are equal, the
  mapping is endpoint-independent; if they differ, it is address-and-port-dependent. That class is stored on the
  presence.
- **Rollout.** Until the second address exists, the hello is **off by default** and an operator enables it in
  config.
  - Servers that don't enable it behave exactly as today, so a hello cannot turn today's visible failure into a silent
    "registered but unreachable".
  - An enabled server is always tagged `nat-reflexive`.

### 3. nakama chooses the player path per join: punched first where predicted, relay otherwise

The client is handed exactly one endpoint, in one success frame that has no field for alternatives, and the game
owns its connect logic. So "try direct, then fall back" cannot happen inside one join without a second success
message or rewriting the game's destination on every client. **nakama therefore predicts the path before sending the
success messages:**

| Server | Player | Path |
|---|---|---|
| open | any | **direct**, as today |
| `nat-reflexive` | same public IP as the server (behind the same NAT) | **direct**, using the private internal address (ADR 0002) |
| `nat-reflexive`, endpoint-independent mapping | sent a client hello with endpoint-independent mapping | **punched** |
| anything else | any | **relay** |

- **A failed punch is remembered.** If the server does not report the player connected (`evr_lobby_joinentrant.go:395-397`)
  within a timeout, nakama marks that (server, player IP) pair, and the next join of that pair uses the relay.
- **Punch-through first, per join, where predicted.** It costs no bandwidth and adds no detour.
- **Relay first in build order.**
  - The relay needs no client change, so every old PC and Quest client gets a path.
  - It covers every NAT class, including the observed double NAT, whose outer layer's class is unknown.
  - Punching needs a client hello from the client's game socket on both platforms, helps only pairs where both
    mappings are endpoint-independent, and has an unmeasured success rate here.
  - Once punching ships, it moves traffic off the relay pair by pair.

### 4. The relay is a plain UDP forwarder with one port per (match, player), not TURN on the wire

- **Ports.** The relay host holds a bounded port range. On join, nakama allocates one relay port per player.
  - Each player needs a distinct port, because the game tells peers apart by source address: a shared port would
    merge players into one peer.
- **The server's side.**
  - The server's runtime sends a hello from the game socket to that relay port. The relay learns the server's mapping
    for that port from the hello's source, which is correct even for a symmetric NAT.
  - The server's success message leaves first (`evr_lobby_joinentrant.go:446`), and the client's follows after
    150 ms (`:463-465`), so the server's hello has a head start.
- **What the relay forwards.** It carries datagrams between the server's learned address and the player's address,
  and nothing else.
- **Why not TURN.** TURN would put RFC 8656 framing into the game's hot path on whichever side allocates. Here the
  game sees an ordinary peer address, and the runtime's only new job is a hello.
- **Relay host.** It runs on a public address distinct from nakama's ping address, which gives 2 its second probe
  address. It also answers EVR pings itself, so clients can measure their latency to it.

### 5. Relay credentials: none go to the client; the server's allocation rides a new protobuf field

- **The client carries no credential.**
  - The relay admits a player by the IP nakama already knows from that player's session, pushed by nakama to the relay
    with the allocation. That is the same IP-only permission TURN's CreatePermission grants (RFC 8656 §9).
  - Admission expires with the match, or after an idle timeout.
  - The client therefore needs no new field. Its fixed binary frame stays as it is, and old-client translation keeps
    working (`legacy_codec.cpp:830`).
- **The server gets its allocation in the success message.** The relay address and port, plus a one-time token its
  hello must carry, go in a **new field on the protobuf `SNSLobbySessionSuccessV5Message`**. That is an nevr-api
  change, appended after the existing fields, never reusing a retired field number.
  - The server runtime already decodes this message before the game sees it (`gameserver_callbacks.cpp:218-229`).
  - An older runtime ignores the unknown field. nakama must never pick the relay path for a server that did not
    declare relay support at registration.
- **nakama is the only allocator.** The relay accepts allocations only over an authenticated channel from nakama.
  There is no public credential-minting endpoint.

### 6. Symmetric NAT and CGNAT

| Case | Registration | Players |
|---|---|---|
| Server, symmetric (either layer of a double NAT) | works: the mapping is learned from the fixed port itself | relay only; punching is never offered |
| Server behind CGNAT | works. The external IP is the CGN's public address (the registration's client IP) and the port comes from the hello. CGNs that follow RFC 6888 REQ-1 must meet RFC 4787's UDP requirements, including endpoint-independent mapping (RFC 4787 REQ-1), but non-conforming CGNs exist | by measured class: punched or relay |
| Player, symmetric or CGNAT, or a client with no hello | n/a | relay (or direct to an open server) |
| Double NAT | behaves as its stricter layer; the hello sees the outermost mapping | as the stricter layer |
| No outbound UDP at all | `result=10`, with the reason named | n/a |

- **Classes are measured, not inferred from address ranges.** nakama's CGNAT range check (`evr_pipeline_gameserver.go:40-42`)
  serves only the ADR 0001 internal-slot rule. A VPN overlay address in 100.64/10 stays in the internal slot as
  today, and the reflexive address always comes from the observed source, never from an interface address.

### 7. How a client is told which path to use

**Only through the existing `Endpoint` in its success frame.** No new field is added.

| Path | `ExternalIP:Port` | Internal slot |
|---|---|---|
| direct | the server's registered (reflexive, where learned) address | the server's private internal address (ADR 0002) |
| punched | the server's registered (reflexive, where learned) address | the server's private internal address (ADR 0002) |
| relay | the player's relay address and port | **empty** |

- **Why the internal slot is empty for relay.** The endpoint has one `Port` for both addresses
  (`evr/match_ping_request.go:80-84`), and the relay port is not the server's LAN port. A player on the server's LAN
  is caught first by the same-public-IP row in 3.
- **Where the substitution happens.** It is per entrant at `evr_lobby_joinentrant.go:403-405`. It is also what
  `rememberLobbySessionSuccess` keeps for a duplicate-join re-send (`:469`).

## Consequences

- **The observed failure is addressed by 1 and 2 alone.** A server behind double NAT registers as `nat-reflexive`
  with no forward, and is not offered to players until 3-5 exist. That is a smaller, honest step: the operator sees
  "registered, waiting for a player path", not a false "playable".
- **Servers that work today are untouched.** The fixed port changes only the source port of nakama's pings, and
  the hello is off by default until the second probe address exists.
- **Errors on the shared socket are not per server.** Linux passes fatal errors such as an ICMP port-unreachable to
  an unconnected UDP socket too, and the error may belong to an earlier packet sent to a different server (udp(7),
  "Errors"). Today each ping has its own connected socket, so a "could not read ping response" error
  (`evr_pipeline_gameserver.go:893`) belongs to that server. On the one fixed socket, the single reader must treat
  a socket error as belonging to no waiter, keep reading, and let each waiter time out by its own token (`:891`).
  Registration already treats both as failure (`:362-365`).
- **Latency for relay-fronted servers is a sum.** Latency history is keyed by external IP (`evr_latencyhistory.go:265`),
  so the relay's address cannot stand in for each server it fronts. The expected RTT is client→relay (one shared key
  for the relay, measured by the client's ping) plus relay→server (measured by the relay over the server's standing
  hello). Ping discovery (`evr_ping_discovery.go:79-110`) and the pre-join check (`evr_lobby_prejoin_ping.go:181-195`)
  must learn to read that sum for `nat-reflexive` servers, otherwise every relay-fronted server fails pre-join.
- **New protocol surface.**
  - Hello format: nakama and the runtime.
  - Classification fields on the game server presence.
  - One protobuf field (nevr-api).
  - The relay's control channel.
  - A client hello later, for punching.
- **New runtime work on the server.**
  - Sending from the game's socket; obtaining that socket reliably is an open question for the runtime issue.
  - The hello on both registration paths.
  - The per-join relay hello.
  - Taking hello traffic out of `recvfrom` if anything ever answers it. Nothing does in this design.
- **Bandwidth.** Every relayed player's traffic crosses the relay host twice (in and out). Echo VR traffic per player
  is unmeasured here. The relay must count bytes per allocation and expose them, so the punch rollout can be judged
  by how much it takes off the relay.
- **Abuse surface.**
  - The ping port never answers anything but pings it sent, by token and source.
  - The relay forwards only between two admitted addresses per port, with a rate cap per port.
  - Players are admitted by IP, so other users behind the same CGN IP could reach an allocated relay port. They
    would reach only the game server, which already authenticates its peers with the session keys in the success
    message (`evr_lobby_joinentrant.go:427-433`).
- **Players whose UDP source IP differs from their websocket IP fail relay admission.** Split tunnels and dual stack
  can cause this. It is expected, not observed; the relay logs refused sources per allocation so it can be measured.

## What would change this decision

- **The relay's cost is refused, or measured too high.** Then only 1-2 and punching ship. Symmetric and
  CGNAT-without-EIM servers stay `nat-reflexive` and unjoinable, and say so to the operator.
- **The game tolerates a second success message or a destination change mid-join.** Then a true try-direct-then-relay
  fallback inside one join would replace nakama's per-join prediction in 3.
- **The game tolerates extra trailing bytes in the client's success frame, and the runtime reads the endpoint.**
  Credentials could then reach the client. That would only matter if the relay moved to client-side TURN.
- **Measured punch success for this player base is low.** If, say, under half of predicted pairs connect, punching is
  not worth a client update, and the relay stays the only `nat-reflexive` path.
- **nakama gains a second public address of its own.** The openness probe in 2 could then come from nakama, and the
  hello could default on before the relay host exists.
- **More failures like 2026-10-31 are observed, and a forward would have fixed them.** If the next ones are on single
  NAT, operator documentation for forwarding and UPnP (alternative A) is the cheaper fix to ship first. This ADR's
  phases stay in place for double NAT and CGNAT.
