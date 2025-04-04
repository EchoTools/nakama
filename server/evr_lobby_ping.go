package server

import (
	"math/rand"
	"net"
	"slices"
	"time"

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

func sortPingCandidatesByLatencyHistory(hostIPs []string, latencyHistory *LatencyHistory) {

	// Shuffle the candidates
	for i := len(hostIPs) - 1; i > 0; i-- {
		j := rand.Intn(i + 1)
		hostIPs[i], hostIPs[j] = hostIPs[j], hostIPs[i]
	}

	type item struct {
		hostIP          string
		latestTimestamp time.Time
		inCache         bool
		rtt             time.Duration
	}

	index := make([]item, len(hostIPs))
	for _, hostIP := range hostIPs {
		// Get the latest timestamp and RTT for each host IP
		item := item{
			hostIP: hostIP,
		}
		if history, inCache := latencyHistory.Get(hostIP); inCache {
			item.inCache = true
			latest := history[len(history)-1]
			item.latestTimestamp = latest.Timestamp
			item.rtt = latest.RTT
		}
	}

	// Sort the active endpoints
	slices.SortStableFunc(index, func(a, b item) int {

		if a.inCache && !b.inCache {
			return -1
		}
		if !a.inCache && b.inCache {
			return 1
		}

		if a.latestTimestamp.Before(b.latestTimestamp) {
			return -1
		}

		if a.latestTimestamp.After(b.latestTimestamp) {
			return 1
		}

		if a.rtt < b.rtt {
			return -1
		}
		if a.rtt > b.rtt {
			return 1
		}

		return 0
	})
}
