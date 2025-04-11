package server

import (
	"encoding/json"
	"math/rand"
	"net"
	"slices"
	"sync"
	"time"
)

const (
	LatencyHistoryStorageCollection = "LatencyHistory"
	LatencyHistoryStorageKey        = "store"
)

var _ = VersionedStorable(&LatencyHistory{})

type LatencyHistoryItem struct {
	Timestamp time.Time     `json:"timestamp"`
	RTT       time.Duration `json:"rtt"`
}

type LatencyHistory struct {
	sync.RWMutex
	GameServerLatencies map[string][]LatencyHistoryItem `json:"game_server_latencies"`
	version             string
}

func NewLatencyHistory() *LatencyHistory {
	return &LatencyHistory{
		GameServerLatencies: make(map[string][]LatencyHistoryItem),
	}
}

func (h *LatencyHistory) StorageMeta() StorageMeta {
	version := "*"
	if h == nil && h.version != "" {
		version = h.version
	}

	return StorageMeta{
		Collection: LatencyHistoryStorageCollection,
		Key:        LatencyHistoryStorageKey,
		Version:    version,
	}
}

func (h *LatencyHistory) SetStorageVersion(version string) {
	h.version = version
}

func (h *LatencyHistory) String() string {
	h.RLock()
	defer h.RUnlock()
	data, err := json.Marshal(h)
	if err != nil {
		return ""
	}
	return string(data)
}

// Add adds a new RTT to the history for the given external IP
func (h *LatencyHistory) Add(extIP net.IP, rtt int, limit int, expiry time.Time) {
	h.Lock()
	defer h.Unlock()
	if h.GameServerLatencies == nil {
		h.GameServerLatencies = make(map[string][]LatencyHistoryItem)
	}

	history, ok := h.GameServerLatencies[extIP.String()]
	if !ok {
		history = make([]LatencyHistoryItem, 0)
		h.GameServerLatencies[extIP.String()] = history
	}

	// Add the new RTT to the history
	history = append(history, LatencyHistoryItem{
		Timestamp: time.Now(),
		RTT:       time.Duration(rtt) * time.Millisecond,
	})

	if len(history) > limit {
		// Remove the oldest entries
		history = history[len(history)-limit:]
	}

	// Remove 0's
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].RTT == 0 {
			history = slices.Delete(history, i, i+1)
		}
	}

	// Remove entries older than the expiry time
	for i := range history {
		if history[i].Timestamp.Before(expiry) {
			history = history[:i]
			break
		}
	}

	// Set the updated history back
	h.GameServerLatencies[extIP.String()] = history
}

// LatestRTTs returns the latest RTTs for all game servers
func (h *LatencyHistory) LatestRTTs() map[string]int {
	h.RLock()
	defer h.RUnlock()
	latestRTTs := make(map[string]int)
	for extIP, history := range h.GameServerLatencies {
		for i := len(history) - 1; i >= 0; i-- {
			if history[i].RTT > 0 {
				latestRTTs[extIP] = int(history[i].RTT.Milliseconds())
				break
			}
		}
	}
	return latestRTTs
}

// LatestRTT returns the latest RTT for a single external IP
func (h *LatencyHistory) LatestRTT(extIP net.IP) int {
	h.RLock()
	defer h.RUnlock()
	if history, ok := h.GameServerLatencies[extIP.String()]; !ok || len(history) == 0 {
		return 0
	} else {
		for i := len(history) - 1; i >= 0; i-- {
			if history[i].RTT > 0 {
				return int(history[i].RTT.Milliseconds())
			}
		}
		return 0
	}
}

// AverageRTT returns the average RTT for a single external IP
func (h *LatencyHistory) AverageRTT(extIP string, roundRTT bool) int {
	h.RLock()
	defer h.RUnlock()

	history, ok := h.GameServerLatencies[extIP]
	if !ok || len(history) == 0 {
		return 0
	}

	average := 0
	for _, l := range history {
		average += int(l.RTT.Milliseconds())
	}
	average /= len(history)

	if roundRTT {
		average = (average + 5) / 10 * 10
	}

	return average
}

// AverageRTTs returns the average RTTs for all game servers
func (h *LatencyHistory) AverageRTTs(roundRTTs bool) map[string]int {
	h.RLock()
	defer h.RUnlock()
	averageRTTs := make(map[string]int)
	for extIP, history := range h.GameServerLatencies {
		if len(history) == 0 {
			continue
		}
		average := 0
		for _, l := range history {
			average += int(l.RTT.Milliseconds())
		}
		average /= len(history)

		if roundRTTs {
			average = (average + 5) / 10 * 10
		}
		averageRTTs[extIP] = average
	}
	return averageRTTs
}

func (h *LatencyHistory) Get(extIP string) ([]LatencyHistoryItem, bool) {
	h.RLock()
	defer h.RUnlock()
	history, ok := h.GameServerLatencies[extIP]
	if !ok || len(history) == 0 {
		return nil, false
	}
	return history, true
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

		if !a.inCache && b.inCache {
			return -1
		}
		if a.inCache && !b.inCache {
			return 1
		}
		// sort older entries first
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
