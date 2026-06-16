package server

import (
	"context"
	"encoding/json"
	"net"
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
	defaultPingDiscoveryMaxMessages   = 8
	defaultPingDiscoverySpreadSeconds = 60

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
