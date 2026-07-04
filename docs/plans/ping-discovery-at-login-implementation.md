# Implementation Plan: Ping Discovery at Login

- **ADR:** 0002
- **Spec:** `docs/specs/ping-discovery-at-login.md`
- **Phases covered:** Phase 1 (discovery + fallback) and Phase 2 (conditional internal IP at join)
- **Phase 3 (remove CheckServerPing):** Deferred — noted at end

---

## Overview

The feature requires seven work items:

1. Add `PingTarget` struct and `buildPingTargets` helper
2. Add `BestAddress` method to `LatencyHistory`
3. Add config fields + env var loading to `EvrPipeline`
4. Implement `runPingDiscovery` goroutine
5. Update `lobbyPingResponse` to accept internal IPs in external slot
6. Wire `buildJoinEndpoint` into join-time endpoint assembly (Phase 2)
7. Update `CheckServerPing` to skip when cache is warm (Phase 1 fallback)

Plus two test files.

---

## File-by-File Changes

### 1. `server/evr_ping_discovery.go` (NEW)

This is the core new file. It contains the `PingTarget` type, the target-building logic, and the paced discovery goroutine.

```go
package server

import (
    "context"
    "encoding/json"
    "net"
    "os"
    "strconv"
    "time"

    "github.com/gofrs/uuid/v5"
    "github.com/heroiclabs/nakama/v3/server/evr"
    "go.uber.org/zap"
)

const (
    // EndpointsPerMessage is the maximum number of endpoints the EVR client
    // accepts in a single LobbyPingRequest. Protocol constant, not configurable.
    EndpointsPerMessage = 16

    // Default pacing values — overridden by environment variables.
    defaultPingDiscoveryMaxMessages    = 8
    defaultPingDiscoverySpreadSeconds  = 60

    // pingDiscoveryRTTMax is the RTT ceiling sent in discovery ping requests.
    pingDiscoveryRTTMax = 275
)

// PingDiscoveryConfig holds the pacing configuration loaded from environment
// variables at pipeline construction time.
type PingDiscoveryConfig struct {
    MaxMessages   int // Max LobbyPingRequest messages per login
    SpreadSeconds int // Window over which to spread the messages
}

// LoadPingDiscoveryConfig reads PING_DISCOVERY_MAX_MESSAGES and
// PING_DISCOVERY_SPREAD_SECONDS from the runtime environment map. Missing or
// unparseable values fall back to defaults.
func LoadPingDiscoveryConfig(vars map[string]string) PingDiscoveryConfig {
    cfg := PingDiscoveryConfig{
        MaxMessages:   defaultPingDiscoveryMaxMessages,
        SpreadSeconds: defaultPingDiscoverySpreadSeconds,
    }
    if v, ok := vars["PING_DISCOVERY_MAX_MESSAGES"]; ok {
        if n, err := strconv.Atoi(v); err == nil && n > 0 {
            cfg.MaxMessages = n
        }
    }
    if v, ok := vars["PING_DISCOVERY_SPREAD_SECONDS"]; ok {
        if n, err := strconv.Atoi(v); err == nil && n > 0 {
            cfg.SpreadSeconds = n
        }
    }
    return cfg
}

// PingTarget maps a single pingable address back to its owning game server.
type PingTarget struct {
    Address    net.IP // The IP to ping (placed in Endpoint.ExternalIP)
    Port       uint16 // Server port
    ServerKey  string // ExternalIP string of the owning server (reverse lookup key)
    IsInternal bool   // True if this address is the server's internal IP
}

// buildPingTargets enumerates all alive game server presences and produces a
// flat list of PingTarget entries. Each server with an internal IP yields two
// targets (one external, one internal-in-external-slot); servers without an
// internal IP yield one target.
//
// The guildGroups map keys are the group IDs the player belongs to. Presences
// are queried for each guild plus the global (uuid.Nil) stream.
func (p *EvrPipeline) buildPingTargets(logger *zap.Logger, guildGroups map[string]struct{}) []PingTarget {
    seen := make(map[string]struct{}) // dedup by "address:port"
    var targets []PingTarget

    addPresences := func(subject string) {
        presences, err := p.nk.StreamUserList(StreamModeGameServer, subject, "", "", false, true)
        if err != nil {
            logger.Warn("failed to list game server presences for ping discovery",
                zap.String("subject", subject), zap.Error(err))
            return
        }
        for _, presence := range presences {
            gp := &GameServerPresence{}
            if err := json.Unmarshal([]byte(presence.GetStatus()), gp); err != nil {
                continue
            }
            if !gp.Endpoint.IsValid() {
                continue
            }

            extIP := gp.Endpoint.ExternalIP
            port := gp.Endpoint.Port
            serverKey := extIP.String()

            // External target (always)
            extKey := extIP.String() + ":" + strconv.Itoa(int(port))
            if _, dup := seen[extKey]; !dup {
                seen[extKey] = struct{}{}
                targets = append(targets, PingTarget{
                    Address:    extIP,
                    Port:       port,
                    ServerKey:  serverKey,
                    IsInternal: false,
                })
            }

            // Internal target (only if server has a genuine internal IP)
            intIP := gp.Endpoint.InternalIP
            if intIP != nil && !intIP.IsUnspecified() && isInternalIP(intIP) {
                intKey := intIP.String() + ":" + strconv.Itoa(int(port))
                if _, dup := seen[intKey]; !dup {
                    seen[intKey] = struct{}{}
                    targets = append(targets, PingTarget{
                        Address:    intIP,
                        Port:       port,
                        ServerKey:  serverKey,
                        IsInternal: true,
                    })
                }
            }
        }
    }

    for groupID := range guildGroups {
        addPresences(groupID)
    }
    addPresences(uuid.Nil.String())

    return targets
}

// runPingDiscovery is launched as a goroutine after login success. It sends
// paced LobbyPingRequest messages covering all alive game servers (split into
// separate internal/external targets) so the latency cache is warm by the time
// the player enters matchmaking.
//
// Lifecycle: the goroutine exits when the session context is cancelled
// (disconnect/logout) or when all messages have been sent.
func (p *EvrPipeline) runPingDiscovery(session *sessionWS) {
    ctx := session.Context()
    logger := session.Logger()

    params, ok := LoadParams(ctx)
    if !ok {
        logger.Warn("ping discovery: failed to load session params")
        return
    }

    // Build the set of guild group IDs for this player.
    guildGroupIDs := make(map[string]struct{}, len(params.guildGroups))
    for gid := range params.guildGroups {
        guildGroupIDs[gid] = struct{}{}
    }

    targets := p.buildPingTargets(logger, guildGroupIDs)
    if len(targets) == 0 {
        logger.Debug("ping discovery: no targets found")
        return
    }

    cfg := p.pingDiscoveryConfig

    // Chunk targets into batches of EndpointsPerMessage.
    batches := chunkPingTargets(targets, EndpointsPerMessage)

    // Cap at max_messages.
    if len(batches) > cfg.MaxMessages {
        batches = batches[:cfg.MaxMessages]
    }

    // Calculate interval between messages.
    interval := time.Duration(cfg.SpreadSeconds) * time.Second / time.Duration(len(batches))
    if interval < 1*time.Second {
        interval = 1 * time.Second
    }

    logger.Info("ping discovery: starting",
        zap.Int("targets", len(targets)),
        zap.Int("batches", len(batches)),
        zap.Duration("interval", interval),
    )

    for i, batch := range batches {
        // Check for session cancellation before each send.
        select {
        case <-ctx.Done():
            logger.Debug("ping discovery: session cancelled",
                zap.Int("batch", i), zap.Int("total", len(batches)))
            return
        default:
        }

        endpoints := make([]evr.Endpoint, len(batch))
        for j, t := range batch {
            endpoints[j] = evr.Endpoint{
                InternalIP: net.IPv4zero,
                ExternalIP: t.Address,
                Port:       t.Port,
            }
        }

        if err := SendEVRMessages(session, true, evr.NewLobbyPingRequest(pingDiscoveryRTTMax, endpoints)); err != nil {
            logger.Warn("ping discovery: failed to send batch",
                zap.Int("batch", i), zap.Error(err))
            return
        }

        // Sleep between batches (except after the last one).
        if i < len(batches)-1 {
            timer := time.NewTimer(interval)
            select {
            case <-ctx.Done():
                timer.Stop()
                logger.Debug("ping discovery: session cancelled during sleep",
                    zap.Int("batch", i))
                return
            case <-timer.C:
            }
        }
    }

    logger.Info("ping discovery: complete",
        zap.Int("batches_sent", len(batches)),
        zap.Int("targets_sent", min(len(targets), cfg.MaxMessages*EndpointsPerMessage)),
    )
}

// chunkPingTargets splits a slice of PingTarget into batches of at most size n.
func chunkPingTargets(targets []PingTarget, n int) [][]PingTarget {
    var batches [][]PingTarget
    for i := 0; i < len(targets); i += n {
        end := i + n
        if end > len(targets) {
            end = len(targets)
        }
        batches = append(batches, targets[i:end])
    }
    return batches
}
```

**Key design decisions:**

- `buildPingTargets` uses the existing `StreamUserList` + `GameServerPresence` pattern (same as `lobbyPingResponse` and `CheckServerPing`)
- Internal IPs are only included if they pass `isInternalIP()` — this reuses the existing validation in `evr_pipeline_gameserver.go`
- The dedup key is `"ip:port"` so the same server address is never pinged twice
- `runPingDiscovery` uses `time.NewTimer` + `select` for cancellation-safe sleep (not `time.Sleep`)
- The goroutine is self-terminating: it exits on context cancellation or after all batches are sent

**Gotchas:**
- The goroutine must not hold references to the `params` struct beyond what it needs — it copies `guildGroups` keys into a plain `map[string]struct{}` to avoid racing with lobby parameter updates
- If `SendEVRMessages` returns an error (session closed), the goroutine exits immediately

---

### 2. `server/evr_latencyhistory.go` (MODIFY)

Add the `BestAddress` method and a `HasRecentEntry` helper.

#### Add `BestAddress` method (after `AverageRTTs`)

```go
// BestAddress returns the lowest-latency reachable address for a game server
// with the given external and internal IPs. If the client has demonstrated
// reachability to the internal IP (has a non-zero RTT entry), and that RTT is
// lower than or equal to the external RTT, the internal IP is preferred.
//
// Returns the chosen IP string, its RTT in milliseconds, and whether any
// reachable address was found.
func (h *LatencyHistory) BestAddress(externalIP, internalIP string) (ip string, rtt int, ok bool) {
    h.RLock()
    defer h.RUnlock()

    extRTT := h.latestNonZeroRTTLocked(externalIP)
    intRTT := h.latestNonZeroRTTLocked(internalIP)

    switch {
    case extRTT > 0 && intRTT > 0:
        // Both reachable — prefer the lower latency.
        if intRTT <= extRTT {
            return internalIP, intRTT, true
        }
        return externalIP, extRTT, true
    case extRTT > 0:
        return externalIP, extRTT, true
    case intRTT > 0:
        return internalIP, intRTT, true
    default:
        return "", 0, false
    }
}

// latestNonZeroRTTLocked returns the most recent non-zero RTT (in ms) for the
// given IP key, or 0 if none found. Caller must hold at least RLock.
func (h *LatencyHistory) latestNonZeroRTTLocked(ipKey string) int {
    history, ok := h.GameServerLatencies[ipKey]
    if !ok || len(history) == 0 {
        return 0
    }
    for i := len(history) - 1; i >= 0; i-- {
        if history[i].RTT > 0 {
            return int(history[i].RTT.Milliseconds())
        }
    }
    return 0
}

// HasRecentEntry reports whether there is a non-zero latency entry for the
// given IP that is more recent than the cutoff time.
func (h *LatencyHistory) HasRecentEntry(ipKey string, cutoff time.Time) bool {
    h.RLock()
    defer h.RUnlock()
    history, ok := h.GameServerLatencies[ipKey]
    if !ok || len(history) == 0 {
        return false
    }
    for i := len(history) - 1; i >= 0; i-- {
        if history[i].RTT > 0 && history[i].Timestamp.After(cutoff) {
            return true
        }
    }
    return false
}
```

**Refactoring note:** The existing `LatestRTT(extIP net.IP) int` method duplicates the logic in `latestNonZeroRTTLocked`. After adding the locked version, consider making `LatestRTT` call it internally, but that's an optional cleanup — not required for this feature.

**Edge case:** `BestAddress` with `internalIP == ""` (server has no internal IP): `latestNonZeroRTTLocked("")` returns 0, so we fall through to external-only. No special handling needed.

---

### 3. `server/evr_pipeline.go` (MODIFY)

Add the `PingDiscoveryConfig` field to `EvrPipeline` struct and load it in `NewEvrPipeline`.

#### 3a. Add field to `EvrPipeline` struct (line ~91, before `loginAttemptCache`)

```go
// existing field:
loginAttemptCache LoginAttemptCache

// ADD:
pingDiscoveryConfig PingDiscoveryConfig
```

The struct block at lines 48-92 becomes:

```diff
 	remoteLogSem chan struct{} // limits concurrent RemoteLogSet processing
 
 	loginAttemptCache LoginAttemptCache
+	pingDiscoveryConfig PingDiscoveryConfig
 }
```

#### 3b. Load config in `NewEvrPipeline` constructor (line ~271, after `loginAttemptCache`)

Add to the struct literal at line 273:

```diff
 		loginAttemptCache: NewLocalLoginAttemptCache(),
+		pingDiscoveryConfig: LoadPingDiscoveryConfig(vars),
 	}
```

**Note:** `vars` is already available in scope — it's `config.GetRuntime().Environment` from line 101.

---

### 4. `server/evr_pipeline_login.go` (MODIFY)

Launch `runPingDiscovery` after login success.

#### 4a. Add goroutine launch after final `SendEvr` (line ~201)

The current `loginRequest` ends with:

```go
messagesToSend := []evr.Message{
    evr.NewLoginSuccess(session.id, request.XPID),
    unrequireMessage,
    gameSettings,
}
return session.SendEvr(messagesToSend...)
```

Change to:

```go
messagesToSend := []evr.Message{
    evr.NewLoginSuccess(session.id, request.XPID),
    unrequireMessage,
    gameSettings,
}
if err := session.SendEvr(messagesToSend...); err != nil {
    return err
}

// Start background ping discovery after login messages are sent.
// The goroutine is tied to session.Context() and self-terminates on
// disconnect or completion.
go p.runPingDiscovery(session)

return nil
```

**Gotcha:** The `params` must already be stored (`StoreParams` is called at line 147, before this point) so that `runPingDiscovery` can load them from the session context. This is already the case.

**Gotcha:** The `guildGroups` map is populated during `processLoginRequest` -> `initializeSession`, which completes before we reach line 196. Verified: `processLoginRequest` is called at line 133, returns at line 145.

---

### 5. `server/evr_pipeline_matchmaker.go` (MODIFY)

Update `lobbyPingResponse` to include internal IPs in the `knownIPs` allowlist. This is already partially done — the current code at lines 137-139 adds internal IPs:

```go
if ip := gp.Endpoint.InternalIP; ip != nil {
    knownIPs[ip.String()] = struct{}{}
}
```

**Verification:** The existing `lobbyPingResponse` handler already:
1. Checks `result.ExternalIP.IsUnspecified()` and falls back to `result.InternalIP` (line 159-161)
2. Includes internal IPs in `knownIPs` (lines 137-139)
3. Keys into `latencyHistory.Add(ip, ...)` using whatever IP is in the result (line 170)

Since our discovery ping puts the target address (internal or external) in the `Endpoint.ExternalIP` slot, the response will have the target in `result.ExternalIP`. The existing code at line 159 checks `result.ExternalIP.IsUnspecified()` — since we set it to the actual IP (not 0.0.0.0), this branch is skipped and `ip = result.ExternalIP` is used. The address is then validated against `knownIPs` (which includes internal IPs) and stored correctly.

**No code change needed in this handler.** The existing code handles discovery responses correctly.

---

### 6. `server/evr_lobby_find.go` (MODIFY)

Update `CheckServerPing` to skip when the latency cache is warm (Phase 1 fallback behavior).

#### 6a. Add warm-cache check at the beginning of `CheckServerPing` (after line ~946)

After loading `latencyHistory` at line 946, add:

```go
latencyHistory := params.latencyHistory.Load()

// Phase 1 fallback: if ping discovery has already warmed the cache for a
// sufficient fraction of servers, skip the blocking ping request. The
// matchmaker will use the cached latencies directly.
discoveryCutoff := time.Now().Add(-5 * time.Minute) // same window as preJoinPingMaxAge
```

Then, after building `hostIPs` (line 986), before the sort and send:

```go
// Count how many candidate servers already have fresh latency data from
// login-time ping discovery.
cachedCount := 0
for _, ip := range hostIPs {
    if latencyHistory.HasRecentEntry(ip, discoveryCutoff) {
        cachedCount++
    }
}

// If all candidate servers have fresh data, skip the blocking ping request.
// The matchmaker reads directly from the warm latency history.
if cachedCount == len(hostIPs) && len(hostIPs) > 0 {
    logger.Debug("CheckServerPing: all candidates have fresh latency data, skipping ping request",
        zap.Int("cached", cachedCount), zap.Int("total", len(hostIPs)))
    return nil
}

logger.Debug("CheckServerPing: cache miss, sending ping request",
    zap.Int("cached", cachedCount), zap.Int("total", len(hostIPs)))
```

This preserves the existing 16-endpoint blocking ping as a fallback for players who queue before discovery finishes.

**Gotcha:** The `HasRecentEntry` call takes a read lock on LatencyHistory. This is fine — `CheckServerPing` is already on the matchmaking goroutine, and LatencyHistory uses RWMutex for concurrent access.

---

### 7. `server/evr_match_label.go` (MODIFY) — Phase 2

Change `GetEntrantConnectMessage` (and/or `GetEndpoint`) to accept per-client endpoint customization.

The current flow is:
1. `LobbyJoinEntrants` calls `label.GetEntrantConnectMessage(role, ...)` at `evr_lobby_joinentrant.go:253`
2. `GetEntrantConnectMessage` calls `GetEndpoint()` which returns `s.GameServer.Endpoint`
3. The same endpoint goes to all entrants

For Phase 2, we need per-entrant endpoint customization. The cleanest approach is to **not change the match label** but instead override the endpoint in `LobbyJoinEntrants` after getting the connection settings.

#### 7a. `server/evr_lobby_joinentrant.go` (MODIFY)

In `LobbyJoinEntrants`, after getting `connectionSettings` at line 253, add per-client endpoint override:

```go
connectionSettings := label.GetEntrantConnectMessage(e.RoleAlignment, e.DisableEncryption, e.DisableMAC)

// Phase 2: Per-client endpoint assembly. If the client has demonstrated
// reachability to the server's internal IP (from login-time ping discovery),
// include the internal IP in the endpoint. Otherwise, send external-only.
if label.GameServer != nil {
    endpoint := buildJoinEndpoint(sessionCtx, label.GameServer.Endpoint)
    connectionSettings.Endpoint = endpoint
}
```

The `buildJoinEndpoint` function goes in the new `evr_ping_discovery.go` file (or a small addition to `evr_lobby_joinentrant.go`):

#### 7b. Add `buildJoinEndpoint` to `server/evr_ping_discovery.go`

```go
// buildJoinEndpoint constructs the Endpoint that will be sent to the client in
// LobbySessionSuccess. If the client has demonstrated reachability to the
// server's internal IP (via ping discovery), the internal IP is included so the
// client can use the lower-latency path. Otherwise, the internal slot is set to
// nil (serialized as 0.0.0.0, the client's "skip this address" value).
//
// This implements Phase 2 of ADR 0002.
func buildJoinEndpoint(ctx context.Context, serverEndpoint evr.Endpoint) evr.Endpoint {
    params, ok := LoadParams(ctx)
    if !ok {
        // No session params — return the endpoint as-is (external + internal).
        return serverEndpoint
    }

    lh := params.latencyHistory.Load()
    if lh == nil {
        return serverEndpoint
    }

    extIP := serverEndpoint.ExternalIP
    intIP := serverEndpoint.InternalIP

    // If the server has no internal IP, nothing to decide.
    if intIP == nil || intIP.IsUnspecified() {
        return serverEndpoint
    }

    // Check if the client demonstrated reachability to the internal IP.
    _, intRTT, _ := lh.BestAddress(extIP.String(), intIP.String())

    if intRTT > 0 {
        // Client can reach internal IP — include it.
        return evr.Endpoint{
            InternalIP: intIP,
            ExternalIP: extIP,
            Port:       serverEndpoint.Port,
        }
    }

    // Client cannot reach internal IP — external only.
    return evr.Endpoint{
        InternalIP: nil,
        ExternalIP: extIP,
        Port:       serverEndpoint.Port,
    }
}
```

**Where exactly in `evr_lobby_joinentrant.go`:**

The loop that processes each entrant starts around line 204. Each entrant's connection settings are generated at line 253. The endpoint override should go immediately after, before the protobuf envelope construction at line 262.

Specifically, the change at lines 253-262:

```diff
 	connectionSettings := label.GetEntrantConnectMessage(e.RoleAlignment, e.DisableEncryption, e.DisableMAC)
 
+	// Phase 2 (ADR 0002): Per-client endpoint assembly — include the
+	// server's internal IP only if this client demonstrated reachability.
+	if label.GameServer != nil {
+		connectionSettings.Endpoint = buildJoinEndpoint(sessionCtx, label.GameServer.Endpoint)
+	}
+
 	// Quest (standalone) clients use a different encoder flag bit layout (shifted by 1).
 	if ServiceSettings().UseQuestEncoderFlags {
```

**Important context:** `sessionCtx` is the entrant's session context (obtained earlier in the function from the session registry). It contains that specific player's `SessionParameters` and therefore their `latencyHistory`. This means each entrant gets their own endpoint decision.

**Edge case:** If the latency history has no data for either IP (player queued before discovery), `BestAddress` returns `ok=false`. The function returns external-only, which is the safe default — the same as current behavior.

**Edge case:** The protobuf envelope at line 269 reads `connectionSettings.Endpoint.String()`. Our override sets the Endpoint field before that line, so it picks up the customized endpoint correctly.

---

### 8. `server/evr_ping_discovery_test.go` (NEW)

```go
package server

import (
    "net"
    "testing"
    "time"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestBestAddress_BothReachable_PrefersInternal(t *testing.T) {
    h := NewLatencyHistory()
    h.Add(net.ParseIP("1.2.3.4"), 35, 25, time.Time{})   // external: 35ms
    h.Add(net.ParseIP("192.168.1.5"), 2, 25, time.Time{}) // internal: 2ms

    ip, rtt, ok := h.BestAddress("1.2.3.4", "192.168.1.5")
    require.True(t, ok)
    assert.Equal(t, "192.168.1.5", ip)
    assert.Equal(t, 2, rtt)
}

func TestBestAddress_BothReachable_PrefersLower(t *testing.T) {
    h := NewLatencyHistory()
    h.Add(net.ParseIP("1.2.3.4"), 10, 25, time.Time{})     // external: 10ms
    h.Add(net.ParseIP("192.168.1.5"), 50, 25, time.Time{})  // internal: 50ms (worse)

    ip, rtt, ok := h.BestAddress("1.2.3.4", "192.168.1.5")
    require.True(t, ok)
    assert.Equal(t, "1.2.3.4", ip)
    assert.Equal(t, 10, rtt)
}

func TestBestAddress_OnlyExternalReachable(t *testing.T) {
    h := NewLatencyHistory()
    h.Add(net.ParseIP("1.2.3.4"), 35, 25, time.Time{}) // external only

    ip, rtt, ok := h.BestAddress("1.2.3.4", "192.168.1.5")
    require.True(t, ok)
    assert.Equal(t, "1.2.3.4", ip)
    assert.Equal(t, 35, rtt)
}

func TestBestAddress_NeitherReachable(t *testing.T) {
    h := NewLatencyHistory()

    _, _, ok := h.BestAddress("1.2.3.4", "192.168.1.5")
    assert.False(t, ok)
}

func TestBestAddress_NoInternalIP(t *testing.T) {
    h := NewLatencyHistory()
    h.Add(net.ParseIP("1.2.3.4"), 35, 25, time.Time{})

    ip, rtt, ok := h.BestAddress("1.2.3.4", "")
    require.True(t, ok)
    assert.Equal(t, "1.2.3.4", ip)
    assert.Equal(t, 35, rtt)
}

func TestBestAddress_EqualRTT_PrefersInternal(t *testing.T) {
    h := NewLatencyHistory()
    h.Add(net.ParseIP("1.2.3.4"), 20, 25, time.Time{})
    h.Add(net.ParseIP("192.168.1.5"), 20, 25, time.Time{})

    ip, _, ok := h.BestAddress("1.2.3.4", "192.168.1.5")
    require.True(t, ok)
    // When equal, internal is preferred (intRTT <= extRTT)
    assert.Equal(t, "192.168.1.5", ip)
}

func TestHasRecentEntry(t *testing.T) {
    h := NewLatencyHistory()
    h.Add(net.ParseIP("1.2.3.4"), 35, 25, time.Time{})

    assert.True(t, h.HasRecentEntry("1.2.3.4", time.Now().Add(-1*time.Minute)))
    assert.False(t, h.HasRecentEntry("1.2.3.4", time.Now().Add(1*time.Minute)))
    assert.False(t, h.HasRecentEntry("5.6.7.8", time.Now().Add(-1*time.Minute)))
}

func TestChunkPingTargets(t *testing.T) {
    targets := make([]PingTarget, 35)
    for i := range targets {
        targets[i] = PingTarget{
            Address:   net.ParseIP("1.2.3.4"),
            Port:      uint16(6792 + i),
            ServerKey: "1.2.3.4",
        }
    }

    batches := chunkPingTargets(targets, 16)
    assert.Len(t, batches, 3)
    assert.Len(t, batches[0], 16)
    assert.Len(t, batches[1], 16)
    assert.Len(t, batches[2], 3)
}

func TestChunkPingTargets_ExactMultiple(t *testing.T) {
    targets := make([]PingTarget, 32)
    for i := range targets {
        targets[i] = PingTarget{Address: net.ParseIP("1.2.3.4"), Port: uint16(i)}
    }

    batches := chunkPingTargets(targets, 16)
    assert.Len(t, batches, 2)
    assert.Len(t, batches[0], 16)
    assert.Len(t, batches[1], 16)
}

func TestChunkPingTargets_Empty(t *testing.T) {
    batches := chunkPingTargets(nil, 16)
    assert.Nil(t, batches)
}

func TestLoadPingDiscoveryConfig_Defaults(t *testing.T) {
    cfg := LoadPingDiscoveryConfig(map[string]string{})
    assert.Equal(t, 8, cfg.MaxMessages)
    assert.Equal(t, 60, cfg.SpreadSeconds)
}

func TestLoadPingDiscoveryConfig_Override(t *testing.T) {
    cfg := LoadPingDiscoveryConfig(map[string]string{
        "PING_DISCOVERY_MAX_MESSAGES":   "12",
        "PING_DISCOVERY_SPREAD_SECONDS": "30",
    })
    assert.Equal(t, 12, cfg.MaxMessages)
    assert.Equal(t, 30, cfg.SpreadSeconds)
}

func TestLoadPingDiscoveryConfig_InvalidFallsBackToDefault(t *testing.T) {
    cfg := LoadPingDiscoveryConfig(map[string]string{
        "PING_DISCOVERY_MAX_MESSAGES":   "not_a_number",
        "PING_DISCOVERY_SPREAD_SECONDS": "-5",
    })
    assert.Equal(t, 8, cfg.MaxMessages)
    assert.Equal(t, 60, cfg.SpreadSeconds)
}
```

---

### 9. `server/evr_latencyhistory_test.go` (NEW or add to existing)

There's no existing test file for `evr_latencyhistory.go`. Create one:

```go
package server

import (
    "net"
    "testing"
    "time"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestLatencyHistory_Add_And_LatestRTT(t *testing.T) {
    h := NewLatencyHistory()
    ip := net.ParseIP("1.2.3.4")
    h.Add(ip, 35, 25, time.Time{})

    got := h.LatestRTT(ip)
    assert.Equal(t, 35, got)
}

func TestLatencyHistory_BestAddress(t *testing.T) {
    // Tests are in evr_ping_discovery_test.go — this file tests the base
    // LatencyHistory methods. BestAddress tests are grouped with discovery.
}

func TestLatencyHistory_HasRecentEntry_Boundary(t *testing.T) {
    h := NewLatencyHistory()
    h.Add(net.ParseIP("10.0.0.1"), 15, 25, time.Time{})

    // Entry was just added — it's after any past cutoff.
    require.True(t, h.HasRecentEntry("10.0.0.1", time.Now().Add(-1*time.Second)))

    // Future cutoff — entry is before it.
    require.False(t, h.HasRecentEntry("10.0.0.1", time.Now().Add(1*time.Hour)))
}
```

---

## Integration Points Summary

### Data flow: Login -> Discovery -> Matchmaking -> Join

```
loginRequest (evr_pipeline_login.go)
  └─ go p.runPingDiscovery(session) (evr_ping_discovery.go)
       └─ buildPingTargets() — enumerates servers, splits int/ext
       └─ sends N paced LobbyPingRequest messages
            └─ client responds with LobbyPingResponse
                 └─ lobbyPingResponse (evr_pipeline_matchmaker.go)
                      └─ latencyHistory.Add(ip, rtt, ...) — stores by IP string
                           (internal IPs are now additional keys)

CheckServerPing (evr_lobby_find.go)
  └─ checks HasRecentEntry for each candidate
  └─ if all cached → skip (return nil)
  └─ if cache miss → send blocking 16-endpoint ping (existing behavior)

LobbyJoinEntrants (evr_lobby_joinentrant.go)
  └─ buildJoinEndpoint(sessionCtx, serverEndpoint)
       └─ latencyHistory.BestAddress(ext, int)
            └─ if internal reachable → Endpoint{int, ext, port}
            └─ else → Endpoint{nil, ext, port}
```

### Reverse Map

The spec mentions a "reverse map (address → server)." In this implementation, the reverse map is **implicit**: each `PingTarget` carries a `ServerKey` (the external IP string), but we don't need to store this map persistently. At join time, `buildJoinEndpoint` already has the `serverEndpoint` from the match label — it knows both the external and internal IPs. It just queries the latency history for both IPs to decide which to include.

The `PingTarget.ServerKey` field is useful for logging and debugging (e.g., "internal IP 192.168.1.5 belongs to server 1.2.3.4") but isn't needed for the join-time decision.

### Interaction with Unreachable Servers

The `unreachableServers` tracking (`server/evr_unreachable_servers.go`) is keyed by external IP. Ping discovery does not modify this — it only adds entries to `latencyHistory`. If a server's external IP is in `unreachableServers`, `CheckServerPing` already skips it (line 982-983). The discovery goroutine does **not** filter by unreachable servers — it pings everything. This is intentional: a server may have become reachable again, and the latency data is additive, not a judgment.

### Interaction with Pre-Join Ping

The pre-join ping system (`evr_lobby_prejoin_ping.go`) queries `latencyHistory.LatestEntry(extIP)` to check freshness. With discovery warming the cache, pre-join pings will increasingly find fresh data and skip the blocking ping. No changes needed to `validatePreJoinPing` — it naturally benefits from the warm cache.

---

## Phase 3 (Deferred)

**Remove or reduce `CheckServerPing`:**

Once ping discovery is proven stable in production:
1. Remove the 16-endpoint blocking ping from `CheckServerPing`
2. Keep only the warm-cache check
3. If any server lacks data, treat as unreachable for this matchmaking attempt (don't block)
4. This makes matchmaking fully non-blocking for ping

**Not implemented in this PR** — requires observability data from Phase 1/2 to confirm the cache is reliably warm.

---

## Files Changed Summary

| File | Action | Description |
|------|--------|-------------|
| `server/evr_ping_discovery.go` | **NEW** | PingTarget, PingDiscoveryConfig, buildPingTargets, runPingDiscovery, buildJoinEndpoint, chunkPingTargets |
| `server/evr_latencyhistory.go` | MODIFY | Add BestAddress, latestNonZeroRTTLocked, HasRecentEntry |
| `server/evr_pipeline.go` | MODIFY | Add pingDiscoveryConfig field + load in constructor |
| `server/evr_pipeline_login.go` | MODIFY | Launch go p.runPingDiscovery(session) after login success |
| `server/evr_lobby_find.go` | MODIFY | Add warm-cache skip in CheckServerPing |
| `server/evr_lobby_joinentrant.go` | MODIFY | Call buildJoinEndpoint for per-client endpoint |
| `server/evr_ping_discovery_test.go` | **NEW** | Tests for BestAddress, HasRecentEntry, chunkPingTargets, config loading |
| `server/evr_latencyhistory_test.go` | **NEW** | Tests for base LatencyHistory methods |

**No changes needed to:**
- `server/evr/match_ping_request.go` — Endpoint struct unchanged
- `server/evr/match_ping_response.go` — response handling unchanged
- `server/evr_pipeline_matchmaker.go` — lobbyPingResponse already handles internal IPs correctly
- `server/evr_pipeline_gameserver.go` — isInternalIP reused as-is
- `server/evr_match_label.go` — GetEndpoint/GetEntrantConnectMessage unchanged (override happens downstream)
- `server/evr_lobby_prejoin_ping.go` — naturally benefits from warm cache, no changes
