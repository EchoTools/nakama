package server

import (
	"math/rand"
	"net"
	"slices"

	"github.com/heroiclabs/nakama/v3/server/evr"
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
