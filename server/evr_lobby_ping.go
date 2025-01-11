package server

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"net"
	"slices"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

const (
	LatencyHistoryStorageCollection = "LatencyHistory"
	LatencyHistoryStorageKey        = "store"
)

type endpointCompact struct {
	externalIP string
	internalIP string
	port       uint16
}

func (e *endpointCompact) ExternalIP() net.IP {
	return net.ParseIP(e.externalIP)
}

func (e *endpointCompact) InternalIP() net.IP {
	return net.ParseIP(e.internalIP)
}

func (e *endpointCompact) Port() uint16 {
	return e.port
}

func (e *endpointCompact) Endpoint() evr.Endpoint {
	return evr.Endpoint{
		ExternalIP: e.ExternalIP(),
		InternalIP: e.InternalIP(),
		Port:       e.Port()}
}

func PingGameServers(ctx context.Context, logger *zap.Logger, session Session, db *sql.DB, activeEndpoints []evr.Endpoint) error {
	latencyHistory, err := LoadLatencyHistory(ctx, logger, db, session.UserID())
	if err != nil {
		return fmt.Errorf("Error loading latency history: %v", err)
	}

	if len(activeEndpoints) == 0 {
		logger.Warn("No active endpoints to ping")

	}

	hostIPs := make([]string, 0, len(activeEndpoints))
	hostMap := make(map[string]evr.Endpoint, len(activeEndpoints))
	for _, endpoint := range activeEndpoints {
		ip := endpoint.GetExternalIP()
		hostIPs = append(hostIPs, ip)
		hostMap[ip] = endpoint
	}

	// Remove Duplicates
	slices.Sort(hostIPs)
	hostIPs = slices.Compact(hostIPs)

	// Sort the candidates by latency history
	sortPingCandidatesByLatencyHistory(hostIPs, latencyHistory)

	candidates := make([]evr.Endpoint, 0, len(hostIPs))

	for i := 0; i < len(hostIPs) && i < 16; i++ {
		candidates = append(candidates, hostMap[hostIPs[i]])
	}

	if err := SendEVRMessages(session, false, evr.NewLobbyPingRequest(350, candidates)); err != nil {
		return fmt.Errorf("Error sending ping request: %v", err)
	}

	logger.Debug("Sent ping request", zap.Any("candidates", candidates))
	return nil
}

func sortPingCandidatesByLatencyHistory(hostIPs []string, latencyHistory map[string]map[int64]int) {

	// Shuffle the candidates
	for i := len(hostIPs) - 1; i > 0; i-- {
		j := rand.Intn(i + 1)
		hostIPs[i], hostIPs[j] = hostIPs[j], hostIPs[i]
	}

	// Sort the active endpoints
	slices.SortStableFunc(hostIPs, func(a, b string) int {

		// by whether the endpoint is in the cache
		var aHistory, bHistory map[int64]int
		var aInCache, bInCache bool
		var aOldest, bOldest int64

		aHistory, aInCache = latencyHistory[a]
		bHistory, bInCache = latencyHistory[b]

		if !aInCache && bInCache {
			return -1
		}

		if aInCache && !bInCache {
			return 1
		}

		if !aInCache && !bInCache {
			return 0
		}

		for ts := range aHistory {
			if ts < aOldest {
				aOldest = ts
			}
		}

		for ts := range bHistory {
			if ts < bOldest {
				bOldest = ts
			}
		}

		if aOldest < bOldest {
			return -1
		}

		if aOldest > bOldest {
			return 1
		}

		return 0
	})
}
