package server

import (
	"container/list"
	"hash/fnv"
	"strconv"
	"strings"
	"sync"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

// systemInfoMetricTagFields is the SystemInfo-derived subset of the login metric
// tags, each bounded by SEC-4 (allow-listed strings + bucketed ints).
//
// F4: cpu_model, gpu_model and driver_version were REMOVED. With no in-repo
// ground-truth allow-list they bucketed to metricTagOther for 100% of traffic — a
// permanently-constant tag is pure cardinality overhead and a misleading dashboard
// dimension, never a signal. Their raw values are still retained forensically in
// the login-history record (not in metrics); when OPS has a real allow-list they
// can be reintroduced here with populated sets.
var systemInfoMetricTagFields = []string{
	"network_type", "headset_type",
	"total_memory", "num_logical_cores", "num_physical_cores",
}

// loginMetricFingerprintFields is the FULL, ordered set of attacker-controlled tag
// keys on the login_success metric: the SystemInfo subset above plus the
// LoginProfile-derived fields (build_number/app_id/publisher_lock) and their
// MetricsTags duplicates (device_type ≡ headset_type, build_version ≡ build_number).
// It is the single source of truth for BOTH the per-player system fingerprint
// (systemInfoFingerprint) and the beyond-cap collapse (boundSystemsPerPlayer), so a
// single account cannot mint new series by varying ANY of these fields. Order is
// load-bearing: the fingerprint hash depends on it, so do not reorder.
var loginMetricFingerprintFields = []string{
	"network_type", "headset_type",
	"total_memory", "num_logical_cores", "num_physical_cores",
	"build_number", "app_id", "publisher_lock",
	"device_type", "build_version",
}

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
	// networkTypeAllowlist is seeded with the only value evidenced in-repo.
	// OPS: add other engine-reported network types (e.g. wired/ethernet/offline)
	// as they are observed.
	networkTypeAllowlist = newTagAllowlist(
		"WiFi",
	)

	// publisherLockAllowlist bounds the free-form client PublisherLock string
	// (LoginProfile.publisher_lock — SEC-4). "rad15_live" is the value carried in
	// the login-history fixture (evr_profile_cache_test.go:177); "echovrce" is the
	// server-side publisher lock (evr_profile_cache.go:156). Any other value buckets
	// to metricTagOther, so a client varying publisher_lock each login cannot mint
	// unbounded series.
	publisherLockAllowlist = newTagAllowlist("rad15_live", "echovrce")
)

// knownBuildSet is the set of legitimate client build numbers (evr.KnownBuilds:
// StandaloneBuildNumber / PCVRBuild). BuildNumber is attacker-controlled
// (LoginProfile.buildversion); emitting it raw as build_number/build_version lets
// one account mint unbounded series by varying it each login. Unknown builds bucket
// to metricTagOther.
var knownBuildSet = func() map[evr.BuildNumber]struct{} {
	set := make(map[evr.BuildNumber]struct{}, len(evr.KnownBuilds))
	for _, b := range evr.KnownBuilds {
		set[b] = struct{}{}
	}
	return set
}()

// boundBuildNumberTag emits an allow-listed build number verbatim, else
// metricTagOther. Cardinality is bounded to len(evr.KnownBuilds)+1.
func boundBuildNumberTag(bn evr.BuildNumber) string {
	if _, ok := knownBuildSet[bn]; ok {
		return strconv.FormatInt(int64(bn), 10)
	}
	return metricTagOther
}

// knownAppIDSet is the set of legitimate client application IDs (evr_authenticate.go:
// NoOvrAppId / QuestAppId / PcvrAppId). AppId is attacker-controlled
// (LoginProfile.appid); bucket unknown values to metricTagOther.
var knownAppIDSet = map[uint64]struct{}{
	NoOvrAppId: {},
	QuestAppId: {},
	PcvrAppId:  {},
}

// boundAppIDTag emits an allow-listed app ID verbatim, else metricTagOther.
// Cardinality is bounded to len(knownAppIDSet)+1.
func boundAppIDTag(appID uint64) string {
	if _, ok := knownAppIDSet[appID]; ok {
		return strconv.FormatUint(appID, 10)
	}
	return metricTagOther
}

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

// SEC-4 int tags: MemoryTotal / NumLogicalCores / NumPhysicalCores are raw
// attacker-controlled int64s from the login payload. Emitting them verbatim as
// metric tags is the same unbounded-cardinality hole the string allow-lists
// close — one account varying memory_total each login mints unlimited Prometheus
// series. These are bucketed to a small fixed set (unknown/out-of-range ->
// metricTagOther), the identical approach used for the string fields.

const bytesPerGiB = 1 << 30

// memoryTiersGiB are the coarse GB buckets MemoryTotal snaps to. MemoryTotal is
// in BYTES on the wire: the login-history fixture reports DedicatedGPUMemory =
// 10737418240 (exactly 10 GiB, an RTX 3080's VRAM) alongside MemoryTotal =
// 16777216000 (~15.6 GiB, a 16 GB machine), so the field is byte-denominated.
var memoryTiersGiB = []int64{4, 8, 12, 16, 24, 32, 48, 64, 96, 128}

// memoryBandGiB is the plausible-RAM band (inclusive). A rounded value outside it
// buckets to metricTagOther; inside it snaps to the nearest tier.
const (
	memoryBandLowGiB  = 3
	memoryBandHighGiB = 192
)

// boundMemoryTotalTag buckets attacker-controlled MemoryTotal (bytes) into a
// coarse GB tier string, or metricTagOther when it rounds outside the plausible
// band. Cardinality is bounded to len(memoryTiersGiB)+1 regardless of input.
func boundMemoryTotalTag(memoryTotalBytes int64) string {
	if memoryTotalBytes <= 0 {
		return metricTagOther
	}
	gib := (memoryTotalBytes + bytesPerGiB/2) / bytesPerGiB // round to nearest GiB
	if gib < memoryBandLowGiB || gib > memoryBandHighGiB {
		return metricTagOther
	}
	nearest := memoryTiersGiB[0]
	best := absInt64(gib - nearest)
	for _, t := range memoryTiersGiB[1:] {
		if d := absInt64(gib - t); d < best {
			best, nearest = d, t
		}
	}
	return strconv.FormatInt(nearest, 10)
}

// core-count clamp bounds: real CPUs report a small integer count. Out of range
// (including 0 and negatives) buckets to metricTagOther.
const (
	minPlausibleCores = 1
	maxPlausibleCores = 64
)

// boundCoreCountTag emits an in-range core count verbatim, else metricTagOther.
// Cardinality is bounded to (maxPlausibleCores-minPlausibleCores+1)+1.
func boundCoreCountTag(cores int64) string {
	if cores < minPlausibleCores || cores > maxPlausibleCores {
		return metricTagOther
	}
	return strconv.FormatInt(cores, 10)
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// systemInfoTagCardinalityBound returns the maximum number of distinct values a
// given SEC-4 metric tag may take: allow-list / bucket-set size + 1 for the
// metricTagOther sentinel. headset_type also allows the "Unknown" empty-input
// sentinel.
func systemInfoTagCardinalityBound(field string) int {
	switch field {
	case "network_type":
		return len(networkTypeAllowlist) + 1
	case "headset_type", "device_type":
		return len(canonicalHeadsetTypes) + 1
	case "total_memory":
		return len(memoryTiersGiB) + 1
	case "num_logical_cores", "num_physical_cores":
		return (maxPlausibleCores - minPlausibleCores + 1) + 1
	case "build_number", "build_version":
		return len(knownBuildSet) + 1
	case "app_id":
		return len(knownAppIDSet) + 1
	case "publisher_lock":
		return len(publisherLockAllowlist) + 1
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
// Emitting the raw values as metric tags would let one authenticated account mint
// an unbounded number of distinct Prometheus series (one per unique tuple) by
// randomizing SystemInfo on repeated logins -> metrics-backend memory pressure.
// Every SystemInfo-derived tag written here is bounded: strings to allow-lists,
// ints (memory/cores) to bucket sets. No SystemInfo field is emitted raw.
func addSystemInfoMetricTags(tags map[string]string, si evr.SystemInfo) {
	tags["network_type"] = networkTypeAllowlist.normalize(si.NetworkType)
	tags["headset_type"] = boundHeadsetMetricTag(si.HeadsetType)
	tags["total_memory"] = boundMemoryTotalTag(si.MemoryTotal)
	tags["num_logical_cores"] = boundCoreCountTag(si.NumLogicalCores)
	tags["num_physical_cores"] = boundCoreCountTag(si.NumPhysicalCores)
}

// systemInfoMetricTags builds a fresh map of the bounded SEC-4 tags. It is a
// thin wrapper over addSystemInfoMetricTags retained for tests; the login path
// uses addSystemInfoMetricTags to write into its existing tags map.
func systemInfoMetricTags(si evr.SystemInfo) map[string]string {
	tags := make(map[string]string, len(systemInfoMetricTagFields))
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

// systemInfoFingerprint hashes the already-bounded login tag tuple into a single
// value identifying a "system". Every attacker-controlled login_success tag
// (loginMetricFingerprintFields — SystemInfo subset plus build_number/app_id/
// publisher_lock and their device_type/build_version duplicates) participates, so a
// player who varies ANY of these fields walks the same capped fingerprint space.
// Non-attacker keys (websocket_auth, is_vr, error, ...) are excluded so churn on
// them does not manufacture new systems. Two logins that bucket identically per
// field share a fingerprint and therefore do not add cardinality.
func systemInfoFingerprint(tags map[string]string) uint64 {
	h := fnv.New64a()
	for _, f := range loginMetricFingerprintFields {
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
	for _, f := range loginMetricFingerprintFields {
		tags[f] = metricTagOther
	}
}
