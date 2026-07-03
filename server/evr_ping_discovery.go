package server

import (
	"encoding/json"
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

// pingEndpoint returns the endpoint to ping for a single game server. Per ADR
// 0002, the pre-connect ping carries BOTH the external and internal address in
// one endpoint (req 3), so the client pings both and at least one returns a
// valid RTT. The internal slot is kept only when it is a genuine private/
// internal address (req 2); a publicly-routable address is zeroed to nil so it
// is never placed in the internal slot.
func pingEndpoint(ep evr.Endpoint) evr.Endpoint {
	intIP := ep.InternalIP
	if intIP != nil && (intIP.IsUnspecified() || !isInternalIP(intIP)) {
		intIP = nil
	}
	return evr.Endpoint{
		InternalIP: intIP,
		ExternalIP: ep.ExternalIP,
		Port:       ep.Port,
	}
}

// buildPingEndpoints enumerates all alive game server presences and produces one
// paired endpoint per server (external + private internal), deduplicated by
// external IP. Each entry carries both addresses so the client validates them
// together (ADR 0002 req 3).
//
// The guildGroups map keys are the group IDs the player belongs to. Presences
// are queried for each guild plus the global (uuid.Nil) stream.
func (p *EvrPipeline) buildPingEndpoints(logger *zap.Logger, guildGroups map[string]struct{}) []evr.Endpoint {
	seen := make(map[string]struct{}) // dedup by external "address:port"
	var endpoints []evr.Endpoint

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

			key := gp.Endpoint.ExternalIP.String() + ":" + strconv.Itoa(int(gp.Endpoint.Port))
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			endpoints = append(endpoints, pingEndpoint(gp.Endpoint))
		}
	}

	for groupID := range guildGroups {
		addPresences(groupID)
	}
	addPresences(uuid.Nil.String())

	return endpoints
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

	endpoints := p.buildPingEndpoints(logger, guildGroupIDs)
	if len(endpoints) == 0 {
		logger.Debug("ping discovery: no endpoints found")
		return
	}

	cfg := p.pingDiscoveryConfig

	// Chunk endpoints into batches of EndpointsPerMessage.
	batches := chunkEndpoints(endpoints, EndpointsPerMessage)

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
		zap.Int("endpoints", len(endpoints)),
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

		if err := SendEVRMessages(session, true, evr.NewLobbyPingRequest(pingDiscoveryRTTMax, batch)); err != nil {
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
		zap.Int("endpoints_sent", min(len(endpoints), cfg.MaxMessages*EndpointsPerMessage)),
	)
}

// buildJoinEndpoint constructs the Endpoint that will be sent to the client in
// LobbySessionSuccess. Per ADR 0002, the client is handed BOTH the external and
// the (private) internal address so it can validate them at connect time and use
// whichever it can reach — at least one is valid (req 3). The internal slot is
// included only when it holds a genuine private/internal address (req 2); a
// publicly-routable or unspecified internal IP is stripped to nil (serialized as
// 0.0.0.0, the client's "skip this address" value).
func buildJoinEndpoint(serverEndpoint evr.Endpoint) evr.Endpoint {
	return pingEndpoint(serverEndpoint)
}

// chunkEndpoints splits a slice of endpoints into batches of at most size n.
func chunkEndpoints(endpoints []evr.Endpoint, n int) [][]evr.Endpoint {
	var batches [][]evr.Endpoint
	for i := 0; i < len(endpoints); i += n {
		end := i + n
		if end > len(endpoints) {
			end = len(endpoints)
		}
		batches = append(batches, endpoints[i:end])
	}
	return batches
}
