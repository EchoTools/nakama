package server

import (
	"container/list"
	"hash/fnv"
	"strings"
	"sync"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

// sec4TagFields is the fixed, ordered set of attacker-controlled SystemInfo
// string fields bounded by SEC-4. It is the single source of truth for both the
// per-player system fingerprint (systemInfoFingerprint) and the beyond-cap
// collapse (boundSystemsPerPlayer). Order is load-bearing: the fingerprint hash
// depends on it, so do not reorder.
var sec4TagFields = []string{"cpu_model", "gpu_model", "network_type", "driver_version", "headset_type"}

// metricTagOther is the sentinel bucket for any client-supplied SystemInfo
// value that is not on the bounded allow-list. Bucketing unknown values here
// (rather than emitting the raw client string) is what bounds metrics
// cardinality for SEC-4: a randomized login payload cannot mint an unbounded
// number of distinct Prometheus series.
const metricTagOther = "other"

// tagAllowlist is a case-sensitive (after strings.TrimSpace) set of known-good
// values for a single client-controlled metric tag.
//
// OPS: these sets are the single, data-driven place to extend coverage. When a
// legitimate value starts showing up bucketed as "other" in the metrics
// backend, add the exact string to the corresponding set below and redeploy.
// Adding values never weakens the cardinality bound — the set is finite and
// curated — it only sharpens the signal.
type tagAllowlist map[string]struct{}

func newTagAllowlist(values ...string) tagAllowlist {
	set := make(tagAllowlist, len(values))
	for _, v := range values {
		set[v] = struct{}{}
	}
	return set
}

// normalize returns the trimmed value if it is on the allow-list, otherwise
// metricTagOther. Empty input also buckets to metricTagOther.
func (a tagAllowlist) normalize(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return metricTagOther
	}
	if _, ok := a[v]; ok {
		return v
	}
	return metricTagOther
}

var (
	// cpuModelAllowlist / gpuModelAllowlist / driverVersionAllowlist are seeded
	// empty on purpose: there is no production ground-truth list of legitimate
	// values in-repo. Until OPS populates these from real telemetry, every value
	// buckets to metricTagOther — the SEC-4 cardinality bound holds immediately,
	// at the cost of signal on these fields until the list is filled.
	cpuModelAllowlist      = newTagAllowlist()
	gpuModelAllowlist      = newTagAllowlist()
	driverVersionAllowlist = newTagAllowlist()

	// networkTypeAllowlist is seeded with the only value evidenced in-repo.
	// OPS: add other engine-reported network types (e.g. wired/ethernet/offline)
	// as they are observed.
	networkTypeAllowlist = newTagAllowlist(
		"WiFi",
	)
)

// canonicalHeadsetTypes is the bounded universe of headset tag values: the
// canonical names produced by headsetMappings, plus normalizeHeadsetType's
// "Unknown" empty-input sentinel. boundHeadsetMetricTag uses this to bucket any
// unrecognized headset to metricTagOther for metrics, without changing the
// shared normalizeHeadsetType (still used by the forensic login-history record
// and DeviceType(), which must keep the real reported string).
var canonicalHeadsetTypes = func() map[string]struct{} {
	set := make(map[string]struct{}, len(headsetMappings)+1)
	for _, canonical := range headsetMappings {
		set[canonical] = struct{}{}
	}
	set["Unknown"] = struct{}{}
	return set
}()

// boundHeadsetMetricTag normalizes a headset string and, crucially, buckets
// anything that is not a known canonical headset to metricTagOther.
// normalizeHeadsetType alone falls through to the raw client string (SEC-4);
// this closes that hole for the metrics path only.
func boundHeadsetMetricTag(raw string) string {
	hs := normalizeHeadsetType(raw)
	if _, ok := canonicalHeadsetTypes[hs]; ok {
		return hs
	}
	return metricTagOther
}

// systemInfoTagCardinalityBound returns the maximum number of distinct values a
// given SEC-4 metric tag may take: allow-list size + 1 for the metricTagOther
// sentinel. headset_type also allows the "Unknown" empty-input sentinel.
func systemInfoTagCardinalityBound(field string) int {
	switch field {
	case "cpu_model":
		return len(cpuModelAllowlist) + 1
	case "gpu_model":
		return len(gpuModelAllowlist) + 1
	case "network_type":
		return len(networkTypeAllowlist) + 1
	case "driver_version":
		return len(driverVersionAllowlist) + 1
	case "headset_type":
		return len(canonicalHeadsetTypes) + 1
	default:
		return 0
	}
}

// addSystemInfoMetricTags writes the client-controlled portion of the login
// metric tags directly into the caller's existing tags map, with every
// attacker-controlled string bounded to its allow-list (unknown values bucket
// to metricTagOther). Writing in place avoids an intermediate map allocation on
// the login path.
//
// SEC-4: these fields are attacker-controlled JSON from the login payload.
// Emitting the raw strings as metric tag values would let one authenticated
// account mint an unbounded number of distinct Prometheus series (one per
// unique tuple) by randomizing SystemInfo on repeated logins ->
// metrics-backend memory pressure. See BUGS.md SEC-4.
func addSystemInfoMetricTags(tags map[string]string, si evr.SystemInfo) {
	tags["cpu_model"] = cpuModelAllowlist.normalize(si.CPUModel)
	tags["gpu_model"] = gpuModelAllowlist.normalize(si.VideoCard)
	tags["network_type"] = networkTypeAllowlist.normalize(si.NetworkType)
	tags["driver_version"] = driverVersionAllowlist.normalize(si.DriverVersion)
	tags["headset_type"] = boundHeadsetMetricTag(si.HeadsetType)
}

// systemInfoMetricTags builds a fresh map of the bounded SEC-4 tags. It is a
// thin wrapper over addSystemInfoMetricTags retained for tests; the login path
// uses addSystemInfoMetricTags to write into its existing tags map.
func systemInfoMetricTags(si evr.SystemInfo) map[string]string {
	tags := make(map[string]string, 5)
	addSystemInfoMetricTags(tags, si)
	return tags
}

// SEC-4 (per-player distinct-system cap): the allow-lists above bound each field
// independently, but the emitted metric series is the *tuple* of bounded fields.
// A single authenticated player who reconnects over and over cycling different
// allow-listed combinations can still walk that tuple space and churn Prometheus
// series (Andrew's review note on this file). boundSystemsPerPlayer caps how many
// distinct bounded tuples ("systems") one player may mint: beyond the cap, further
// new systems collapse to the metricTagOther sentinel — the same bucketing SEC-4
// already uses — so no additional series is created.
const (
	// maxDistinctSystemsPerPlayer is the per-player distinct-system cap. A real
	// player has a small handful of machines (a PC, a standalone headset) whose
	// bounded tuple changes only occasionally (GPU/driver upgrade). 8 gives ample
	// headroom for legitimate churn while making abuse (dozens+ of systems) collapse
	// to "other". Exceeding the cap is metrics-only: it never blocks a login, it only
	// buckets that login's SEC-4 tags to the sentinel.
	maxDistinctSystemsPerPlayer = 8
	// maxTrackedPlayersForSystemLimit bounds tracker memory via a player-level LRU.
	// Worst-case retained state is maxTrackedPlayersForSystemLimit *
	// maxDistinctSystemsPerPlayer uint64 fingerprints (~a few MB at these values).
	// Evicting the least-recently-active player is safe: on their next login they
	// simply start counting systems from zero again.
	maxTrackedPlayersForSystemLimit = 50000
)

// systemInfoFingerprint hashes the already-bounded SEC-4 tag tuple into a single
// value identifying a "system". Only sec4TagFields participate: non-SEC-4 keys
// (total_memory, cores, build_number) are intentionally excluded so churn on them
// does not manufacture new systems. Two logins that bucket identically per field
// share a fingerprint and therefore do not add cardinality.
func systemInfoFingerprint(tags map[string]string) uint64 {
	h := fnv.New64a()
	for _, f := range sec4TagFields {
		_, _ = h.Write([]byte(tags[f]))
		_, _ = h.Write([]byte{0}) // separator: avoid "ab"+"c" == "a"+"bc" collisions
	}
	return h.Sum64()
}

// systemFingerprintLimiter tracks, per player, the set of distinct system
// fingerprints seen, capped at maxPerPlayer. A player-level LRU bounds total
// memory at maxPlayers. It is safe for concurrent use by the login path.
type systemFingerprintLimiter struct {
	mu           sync.Mutex
	maxPerPlayer int
	maxPlayers   int
	order        *list.List               // front = most-recently-active; values are *playerSystems
	index        map[string]*list.Element // playerID -> element in order
}

// playerSystems is one player's bounded set of distinct system fingerprints.
type playerSystems struct {
	playerID string
	seen     map[uint64]struct{}
}

func newSystemFingerprintLimiter(maxPerPlayer, maxPlayers int) *systemFingerprintLimiter {
	return &systemFingerprintLimiter{
		maxPerPlayer: maxPerPlayer,
		maxPlayers:   maxPlayers,
		order:        list.New(),
		index:        make(map[string]*list.Element, maxPlayers),
	}
}

// allow reports whether player may emit a distinct metric series for fingerprint
// fp. An already-seen fingerprint always returns true. A new fingerprint returns
// true and is recorded while the player is under maxPerPlayer; once at the cap,
// new fingerprints return false and are NOT recorded, keeping the set bounded.
// Every access moves the player to the front of the LRU (active players resist
// eviction); a new player may evict the least-recently-active one.
func (l *systemFingerprintLimiter) allow(playerID string, fp uint64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	if el, ok := l.index[playerID]; ok {
		l.order.MoveToFront(el)
		ps := el.Value.(*playerSystems)
		if _, seen := ps.seen[fp]; seen {
			return true
		}
		if len(ps.seen) >= l.maxPerPlayer {
			return false
		}
		ps.seen[fp] = struct{}{}
		return true
	}

	// New player. Evict the least-recently-active one if at capacity.
	if l.order.Len() >= l.maxPlayers {
		if back := l.order.Back(); back != nil {
			evicted := l.order.Remove(back).(*playerSystems)
			delete(l.index, evicted.playerID)
		}
	}
	ps := &playerSystems{playerID: playerID, seen: map[uint64]struct{}{fp: {}}}
	l.index[playerID] = l.order.PushFront(ps)
	return true
}

// trackedPlayers returns the number of players currently held. Test-support only.
func (l *systemFingerprintLimiter) trackedPlayers() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.order.Len()
}

// loginSystemFingerprintLimiter is the process-wide limiter for the login path.
var loginSystemFingerprintLimiter = newSystemFingerprintLimiter(maxDistinctSystemsPerPlayer, maxTrackedPlayersForSystemLimit)

// boundSystemsPerPlayer applies the per-player distinct-system cap on top of the
// per-field SEC-4 allow-list bounding already written into tags. If the player has
// exceeded maxPerPlayer distinct systems, every SEC-4 field collapses to
// metricTagOther so this login mints no new series. An empty playerID (identity
// unavailable) is not tracked — pooling unrelated players under one key would
// over-collapse — so its tags pass through unchanged.
func boundSystemsPerPlayer(l *systemFingerprintLimiter, tags map[string]string, playerID string) {
	if playerID == "" {
		return
	}
	if l.allow(playerID, systemInfoFingerprint(tags)) {
		return
	}
	for _, f := range sec4TagFields {
		tags[f] = metricTagOther
	}
}
