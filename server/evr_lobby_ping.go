package server

import (
	"context"
	"database/sql"
	"math/rand"
	"net"
	"sort"
	"time"

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
		return err
	}

	if len(activeEndpoints) == 0 {
		logger.Warn("No active endpoints to ping")

	}

	// Compact the list by ExternalIP
	seen := make(map[string]struct{}, len(activeEndpoints))
	for i := 0; i < len(activeEndpoints); i++ {
		id := activeEndpoints[i].GetExternalIP()
		if _, ok := seen[id]; ok {
			// Remove the duplicate
			activeEndpoints = append(activeEndpoints[:i], activeEndpoints[i+1:]...)
			i--
			continue
		}
		seen[id] = struct{}{}
	}

	candidates := make([]evr.Endpoint, 0, 16)
	if len(activeEndpoints) >= 16 {
		// Shuffle the candidates
		for i := len(activeEndpoints) - 1; i > 0; i-- {
			j := rand.Intn(i + 1)
			activeEndpoints[i], activeEndpoints[j] = activeEndpoints[j], activeEndpoints[i]
		}
		candidates = activeEndpoints[:16]
	} else {
		// Fill the candidates with the oldest entries, up to 16 entries
		// Add all the endpoints that are not in the cache
		entryAges := make([]int64, 0, len(latencyHistory))
		for _, endpoint := range activeEndpoints {
			if len(candidates) >= 16 {
				break
			}
			extIP := endpoint.GetExternalIP()
			if history, ok := latencyHistory[extIP]; !ok {
				candidates = append(candidates, endpoint)
				entryAges = append(entryAges, time.Time{}.Unix())
			} else {
				// Find the latest latency record
				var latest int64
				for ts := range history {
					if ts > latest {
						latest = ts
					}
				}
				entryAges = append(entryAges, latest)
			}
		}

		// Sort the candidates by age
		sort.SliceStable(activeEndpoints, func(i, j int) bool {
			return entryAges[i] < entryAges[j]
		})

		// Fill the candidates with the oldest entries, up to 16 entries
		for _, endpoint := range activeEndpoints {
			if len(candidates) >= 16 {
				break
			}
			candidates = append(candidates, endpoint)
		}

		// Remove duplicates from the candidates
		seen = make(map[string]struct{}, len(candidates))
		for i := 0; i < len(candidates); i++ {
			id := candidates[i].GetExternalIP()
			if _, ok := seen[id]; ok {
				// Remove the duplicate
				candidates = append(candidates[:i], candidates[i+1:]...)
				i--
				continue
			}
			seen[id] = struct{}{}
		}

		candidates = append(activeEndpoints, candidates...)
		if len(candidates) > 16 {
			candidates = candidates[:16]
		}
	}
	if err := SendEVRMessages(session,
		evr.NewLobbyPingRequest(350, candidates),
		//evr.NewSTcpConnectionUnrequireEvent(),
	); err != nil {
		return err
	}

	logger.Debug("Sent ping request", zap.Any("candidates", candidates))
	return nil
}
