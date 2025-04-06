package server

import (
	"encoding/json"
	"net"
	"time"
)

var _ = VersionedStorable(&LatencyHistory{})

type LatencyHistoryItem struct {
	Timestamp time.Time     `json:"timestamp"`
	RTT       time.Duration `json:"rtt"`
}

type LatencyHistory struct {
	GameServerLatencies map[string][]LatencyHistoryItem `json:"game_server_latencies"`
	version             string
}

func NewLatencyHistory() *LatencyHistory {
	return &LatencyHistory{
		GameServerLatencies: make(map[string][]LatencyHistoryItem),
	}
}

func (*LatencyHistory) StorageMeta() StorageMeta {
	return StorageMeta{
		Collection: StorageCollectionDeveloper,
		Key:        StorageKeyApplications,
	}
}

func (h *LatencyHistory) SetStorageVersion(version string) {
	h.version = version
}

func (h *LatencyHistory) String() string {
	data, err := json.Marshal(h)
	if err != nil {
		return ""
	}
	return string(data)
}

// Add adds a new RTT to the history for the given external IP
func (h *LatencyHistory) Add(extIP net.IP, rtt int, limit int, expiry time.Time) {
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
	history, ok := h.GameServerLatencies[extIP]
	if !ok || len(history) == 0 {
		return nil, false
	}
	return history, true
}
