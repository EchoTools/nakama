package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/heroiclabs/nakama-common/runtime"
)

// The game's matchmaker queue API (echovr.exe CR15NetMatchmakerQueue, cr15netmatchmakerqueue.h).
// On every find of seven matchmaker types the game POSTs, on its API connection:
//
//	ready_at_dawn/join_queue?access_token=..&environment=..&queue_id=..           (Join 0x1401788f0)
//	ready_at_dawn/poll_queue_position?access_token=..&environment=..&queue_id=..  (Heartbeat 0x140170990)
//	ready_at_dawn/leave_queue?access_token=..&environment=..&queue_id=..          (Leave 0x1401792b0)
//
// and reads `status` ("queued" -> 1, "accepted" -> 2, "none" -> 0), `queue_position` and
// `estimated_wait_time` (seconds) from the JSON root (UpdateRequestInProgress 0x1401c4290). The matchmaking
// screen's time remaining is estimated_wait_time; with these requests unanswered it stays 0.
//
// This server has no queue of its own to read a position from: matchmaking runs through the lobby find. So
// the routes answer only what is knowable: "queued" with an estimate, and no queue_position (absent, which
// the game reads as unknown). The estimate is the median of how long recent queues lasted (each completed
// by the game's own leave_queue), minus the time already waited, never below 0; defaultQueueEstimate until
// enough samples exist.
const (
	matchmakerQueuePath       = "/ready_at_dawn/{action:join_queue|poll_queue_position|leave_queue}"
	defaultQueueEstimate      = 60 * time.Second
	queueEstimateSampleWindow = 20
	queueEstimateMinSamples   = 3
	queueEntryTTL             = 30 * time.Minute
	queueMaxEntries           = 10000
	// A completed wait outside this range is not a measurement of a queue (a join/leave pair sent back to
	// back, or an entry that sat until its TTL) and is not sampled.
	queueMinSample = 1 * time.Second
	queueMaxSample = 30 * time.Minute
	// queue_id comes from the request: bounded in length and charset, and only this many distinct ids keep
	// samples (the oldest is dropped), so junk ids cannot grow the estimator.
	queueIDMaxLength = 64
	queueMaxSampled  = 64
)

// validMatchmakerQueueID: 1..queueIDMaxLength of [A-Za-z0-9_.:-].
func validMatchmakerQueueID(id string) bool {
	if id == "" || len(id) > queueIDMaxLength {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '.', c == ':', c == '-':
		default:
			return false
		}
	}
	return true
}

type matchmakerQueueKey struct {
	tokenDigest string
	queueID     string
}

// MatchmakerQueueEstimator holds the joins in flight and the recent completed waits per queue id.
type MatchmakerQueueEstimator struct {
	mu      sync.Mutex
	now     func() time.Time
	joined  map[matchmakerQueueKey]time.Time
	samples map[string][]time.Duration
	// sampledOrder lists the queue ids in `samples`, oldest first (first sample).
	sampledOrder []string
}

func NewMatchmakerQueueEstimator(now func() time.Time) *MatchmakerQueueEstimator {
	if now == nil {
		now = time.Now
	}
	return &MatchmakerQueueEstimator{
		now:     now,
		joined:  make(map[matchmakerQueueKey]time.Time),
		samples: make(map[string][]time.Duration),
	}
}

func matchmakerQueueKeyFor(accessToken, queueID string) matchmakerQueueKey {
	digest := sha256.Sum256([]byte(accessToken))
	return matchmakerQueueKey{tokenDigest: hex.EncodeToString(digest[:]), queueID: queueID}
}

func (e *MatchmakerQueueEstimator) evictLocked(now time.Time) {
	for k, at := range e.joined {
		if now.Sub(at) > queueEntryTTL {
			delete(e.joined, k)
		}
	}
	for len(e.joined) >= queueMaxEntries {
		var oldestKey matchmakerQueueKey
		var oldest time.Time
		first := true
		for k, at := range e.joined {
			if first || at.Before(oldest) {
				oldestKey, oldest, first = k, at, false
			}
		}
		delete(e.joined, oldestKey)
	}
}

func (e *MatchmakerQueueEstimator) medianLocked(queueID string) time.Duration {
	s := e.samples[queueID]
	if len(s) < queueEstimateMinSamples {
		return defaultQueueEstimate
	}
	sorted := append([]time.Duration(nil), s...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
}

// Join records the first join of (token, queue) and returns the seconds the game should expect to wait.
// A poll for an unknown entry (the server restarted, the entry expired) is a join.
func (e *MatchmakerQueueEstimator) JoinOrPoll(accessToken, queueID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	key := matchmakerQueueKeyFor(accessToken, queueID)
	at, ok := e.joined[key]
	if !ok {
		e.evictLocked(now)
		e.joined[key] = now
		at = now
	}
	remaining := e.medianLocked(queueID) - now.Sub(at)
	if remaining < 0 {
		remaining = 0
	}
	return int(remaining / time.Second)
}

// Leave ends the wait and, when the game had joined, records how long it lasted.
func (e *MatchmakerQueueEstimator) Leave(accessToken, queueID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	key := matchmakerQueueKeyFor(accessToken, queueID)
	at, ok := e.joined[key]
	if !ok {
		return
	}
	delete(e.joined, key)
	wait := now.Sub(at)
	if wait < queueMinSample || wait > queueMaxSample {
		return
	}
	existing, known := e.samples[queueID]
	if !known {
		for len(e.sampledOrder) >= queueMaxSampled {
			delete(e.samples, e.sampledOrder[0])
			e.sampledOrder = e.sampledOrder[1:]
		}
		e.sampledOrder = append(e.sampledOrder, queueID)
	}
	s := append(existing, wait)
	if len(s) > queueEstimateSampleWindow {
		s = s[len(s)-queueEstimateSampleWindow:]
	}
	e.samples[queueID] = s
}

type matchmakerQueueResponse struct {
	Status            string `json:"status"`
	EstimatedWaitTime *int   `json:"estimated_wait_time,omitempty"`
}

// NewMatchmakerQueueHandler answers the game's three queue requests; the action is the {action} path variable
// of matchmakerQueuePath.
func NewMatchmakerQueueHandler(estimator *MatchmakerQueueEstimator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		accessToken, queueID := q.Get("access_token"), q.Get("queue_id")
		if accessToken == "" || !validMatchmakerQueueID(queueID) {
			http.Error(w, "access_token and a valid queue_id are required", http.StatusBadRequest)
			return
		}
		var resp matchmakerQueueResponse
		switch mux.Vars(r)["action"] {
		case "join_queue", "poll_queue_position":
			wait := estimator.JoinOrPoll(accessToken, queueID)
			resp = matchmakerQueueResponse{Status: "queued", EstimatedWaitTime: &wait}
		case "leave_queue":
			estimator.Leave(accessToken, queueID)
			resp = matchmakerQueueResponse{Status: "none"}
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// RegisterMatchmakerQueueHTTP registers the three queue routes on the runtime's HTTP router.
func RegisterMatchmakerQueueHTTP(initializer runtime.Initializer, estimator *MatchmakerQueueEstimator) error {
	return initializer.RegisterHttp(matchmakerQueuePath, NewMatchmakerQueueHandler(estimator), http.MethodPost, http.MethodGet)
}
