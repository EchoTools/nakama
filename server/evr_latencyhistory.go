package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	LatencyHistoryStorageCollection = "LatencyHistory"
	LatencyHistoryStorageKey        = "store"
)

const (
	// latencyRetryMaxAttempts bounds writeWithRetry so a persistently contended
	// object cannot spin forever. Includes the initial attempt.
	latencyRetryMaxAttempts = 5
	// latencyRetryBaseBackoff is the base unit of jittered backoff between
	// retry attempts. Kept small: contention here is brief (per-user object).
	latencyRetryBaseBackoff = 5 * time.Millisecond
)

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

func (h *LatencyHistory) StorageMeta() StorableMetadata {
	return StorableMetadata{
		Collection:      LatencyHistoryStorageCollection,
		Key:             LatencyHistoryStorageKey,
		PermissionRead:  0,
		PermissionWrite: 0,
		Version:         h.version,
	}
}

func (h *LatencyHistory) SetStorageMeta(meta StorableMetadata) {
	h.Lock()
	defer h.Unlock()
	h.version = meta.Version
}

// writeWithRetry writes the history, retrying on optimistic-concurrency
// (version-check) conflicts only. On such a conflict it re-reads the current
// stored object and REPLACES h's contents and version with it — the concurrent
// winner's object is the truth — then invokes reapply to re-apply this caller's
// pending samples onto that fresh object, and writes again. This makes a user's
// concurrent sessions lossless: neither the winner's nor this caller's new
// entries are clobbered.
//
// The replacement must be wholesale. Unmarshalling straight into h would merge
// rather than adopt (encoding/json keeps existing keys of a non-nil map), so
// per-IP entries the winner had pruned would be resurrected on every retry —
// including addresses that the caller's game-server allowlist rejects.
//
// The pattern is only sound because LatencyHistory.Add appends to per-server
// lists — a commutative mutation. Do NOT generalize it to types whose updates
// overwrite fields, where last-retrier-wins silently drops the other writer's
// mutation.
//
// reapply MUST be idempotent across attempts and MUST re-apply the same pending
// mutation onto the (now-refreshed) h each time it is called. It is NOT invoked
// before the first attempt — the caller is responsible for having applied its
// mutation to h once before calling. Any pruning/limit/expiry logic that must
// stay bounded after a merge should live inside reapply.
//
// Only version-conflict errors are retried; any other error (including
// non-conflict storage failures) is returned immediately without re-reading.
// Attempts are bounded by latencyRetryMaxAttempts with small jittered backoff.
func (h *LatencyHistory) writeWithRetry(ctx context.Context, nk runtime.NakamaModule, userID string, reapply func() error) error {
	var lastErr error
	for attempt := 0; attempt < latencyRetryMaxAttempts; attempt++ {
		err := StorableWrite(ctx, nk, userID, h)
		if err == nil {
			return nil
		}
		if !isVersionConflictError(err) {
			// Not an OCC conflict: do not re-read or retry.
			return err
		}
		lastErr = err

		// Attempts are spent. Re-reading now would cost a round-trip whose
		// result is never written, and would leave the caller's (session-shared)
		// history holding merged state that was never persisted.
		if attempt == latencyRetryMaxAttempts-1 {
			break
		}

		// Jittered backoff before re-reading for the next attempt.
		backoff := time.Duration(attempt+1) * latencyRetryBaseBackoff
		// rand is fine here: this is contention jitter, not security.
		jitter := time.Duration(rand.Int63n(int64(latencyRetryBaseBackoff)))
		select {
		case <-ctx.Done():
			return fmt.Errorf("LatencyHistory.writeWithRetry: %w", ctx.Err())
		case <-time.After(backoff + jitter):
		}

		// Conflict: a concurrent writer advanced the version. Adopt the winner's
		// object wholesale (read into a fresh value so a failed read cannot
		// leave h half-cleared), then re-apply this caller's pending mutation
		// onto it before writing again.
		fresh := NewLatencyHistory()
		if rerr := StorableRead(ctx, nk, userID, fresh, false); rerr != nil {
			return fmt.Errorf("LatencyHistory.writeWithRetry: re-read after conflict: %w", rerr)
		}
		h.GameServerLatencies = fresh.GameServerLatencies
		h.SetStorageMeta(fresh.StorageMeta())
		if rerr := reapply(); rerr != nil {
			return fmt.Errorf("LatencyHistory.writeWithRetry: re-apply after conflict: %w", rerr)
		}
	}
	return fmt.Errorf("LatencyHistory.writeWithRetry: exhausted %d attempts: %w", latencyRetryMaxAttempts, lastErr)
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

	// Remove entries older than the expiry time.
	// Entries are in chronological order (oldest first), so find the first
	// non-expired entry and discard everything before it.
	expired := len(history)
	for i := range history {
		if !history[i].Timestamp.Before(expiry) {
			expired = i
			break
		}
	}
	history = history[expired:]

	// Set the updated history back
	h.GameServerLatencies[extIP.String()] = history
}

// LatestRTTs returns the latest RTTs for all game servers.
// If since is non-zero, only entries recorded after since are considered.
func (h *LatencyHistory) LatestRTTs(since ...time.Time) map[string]int {
	h.RLock()
	defer h.RUnlock()

	var cutoff time.Time
	if len(since) > 0 {
		cutoff = since[0]
	}

	latestRTTs := make(map[string]int)
	for extIP, history := range h.GameServerLatencies {
		for i := len(history) - 1; i >= 0; i-- {
			if history[i].RTT > 0 {
				if !cutoff.IsZero() && history[i].Timestamp.Before(cutoff) {
					break // entries are chronological; older ones precede this
				}
				latestRTTs[extIP] = int(history[i].RTT.Milliseconds())
				break
			}
		}
	}
	return latestRTTs
}

// LatestEntry returns the most recent non-zero latency item for a single external IP.
// Returns the item and true if found, or a zero item and false if not found.
func (h *LatencyHistory) LatestEntry(extIP string) (LatencyHistoryItem, bool) {
	h.RLock()
	defer h.RUnlock()
	history, ok := h.GameServerLatencies[extIP]
	if !ok || len(history) == 0 {
		return LatencyHistoryItem{}, false
	}
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].RTT > 0 {
			return history[i], true
		}
	}
	return LatencyHistoryItem{}, false
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

// AverageRTTs returns the average RTTs for all game servers.
// If since is non-zero, only entries recorded after since are included.
func (h *LatencyHistory) AverageRTTs(roundRTTs bool, since ...time.Time) map[string]int {
	h.RLock()
	defer h.RUnlock()

	var cutoff time.Time
	if len(since) > 0 {
		cutoff = since[0]
	}

	averageRTTs := make(map[string]int)
	for extIP, history := range h.GameServerLatencies {
		if len(history) == 0 {
			continue
		}
		total := 0
		count := 0
		for _, l := range history {
			if !cutoff.IsZero() && l.Timestamp.Before(cutoff) {
				continue
			}
			total += int(l.RTT.Milliseconds())
			count++
		}
		if count == 0 {
			continue
		}
		average := total / count

		if roundRTTs {
			average = (average + 5) / 10 * 10
		}
		averageRTTs[extIP] = average
	}
	return averageRTTs
}

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

	// Shuffle the candidates before sorting by latency.
	// math/rand is fine: ping-candidate ordering is non-security game logic.
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
	for i, hostIP := range hostIPs {
		// Get the latest timestamp and RTT for each host IP
		index[i] = item{
			hostIP: hostIP,
		}
		if history, inCache := latencyHistory.Get(hostIP); inCache {
			index[i].inCache = true
			latest := history[len(history)-1]
			index[i].latestTimestamp = latest.Timestamp
			index[i].rtt = latest.RTT
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

	// Apply sorted order back to hostIPs
	for i, item := range index {
		hostIPs[i] = item.hostIP
	}
}
