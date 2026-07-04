package server

import (
	"fmt"
	"math/rand"
	"sync"
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

// sec4Fields are the attacker-controlled SystemInfo strings that flow into
// login metric tags (BUGS.md SEC-4). F4 dropped cpu_model/gpu_model/driver_version
// (empty allow-lists → permanently "other"); the bounded string fields that remain
// are network_type and headset_type.
var sec4Fields = []string{"network_type", "headset_type"}

// randomSystemInfo returns a SystemInfo whose SEC-4 string fields are all
// unique per call, simulating an attacker randomizing the login payload.
func randomSystemInfo(rng *rand.Rand, i int) evr.SystemInfo {
	return evr.SystemInfo{
		CPUModel:      fmt.Sprintf("cpu-%d-%d", i, rng.Int63()),
		VideoCard:     fmt.Sprintf("gpu-%d-%d", i, rng.Int63()),
		NetworkType:   fmt.Sprintf("net-%d-%d", i, rng.Int63()),
		DriverVersion: fmt.Sprintf("drv-%d-%d", i, rng.Int63()),
		HeadsetType:   fmt.Sprintf("hmd-%d-%d", i, rng.Int63()),
	}
}

// TestSystemInfoMetricTagCardinality proves the SEC-4 property: N randomized
// login payloads must NOT produce N distinct metric-tag values per field. Each
// attacker-controlled field must be bounded by its allow-list size (+1 for the
// "other" sentinel).
func TestSystemInfoMetricTagCardinality(t *testing.T) {
	const n = 1000
	rng := rand.New(rand.NewSource(1))

	distinct := make(map[string]map[string]struct{}, len(sec4Fields))
	for _, f := range sec4Fields {
		distinct[f] = make(map[string]struct{})
	}

	for i := 0; i < n; i++ {
		tags := systemInfoMetricTags(randomSystemInfo(rng, i))
		for _, f := range sec4Fields {
			distinct[f][tags[f]] = struct{}{}
		}
	}

	for _, f := range sec4Fields {
		bound := systemInfoTagCardinalityBound(f)
		got := len(distinct[f])
		if got > bound {
			t.Errorf("field %q: %d distinct tag values from %d random inputs (bound %d) — unbounded cardinality (SEC-4)", f, got, n, bound)
		}
	}
}

// TestAddSystemInfoMetricTagsWritesInPlace proves addSystemInfoMetricTags writes
// the bounded SEC-4 tags directly into the caller's existing map (no intermediate
// map allocation) without clobbering pre-existing keys. SEC-4 bounding must be
// identical to systemInfoMetricTags.
func TestAddSystemInfoMetricTagsWritesInPlace(t *testing.T) {
	tags := map[string]string{"existing": "keep-me"}
	addSystemInfoMetricTags(tags, evr.SystemInfo{
		NetworkType: "WiFi",     // seeded allow-list value
		HeadsetType: "Quest 3",  // maps to canonical "Meta Quest 3"
		CPUModel:    "unlisted", // F4: no longer emitted as a metric tag
	})

	if got := tags["existing"]; got != "keep-me" {
		t.Errorf("pre-existing key clobbered: got %q, want %q", got, "keep-me")
	}
	if got := tags["network_type"]; got != "WiFi" {
		t.Errorf("network_type: known-good value not preserved: got %q, want %q", got, "WiFi")
	}
	if got := tags["headset_type"]; got != "Meta Quest 3" {
		t.Errorf("headset_type: known headset not normalized: got %q, want %q", got, "Meta Quest 3")
	}
	if _, ok := tags["cpu_model"]; ok {
		t.Errorf("cpu_model: F4-dropped tag must not be emitted, got %q", tags["cpu_model"])
	}
}

// TestBoundMemoryTotalTag proves MemoryTotal (attacker-controlled bytes) buckets
// to a coarse GB tier, with adversarial / out-of-band values collapsing to the
// metricTagOther sentinel.
func TestBoundMemoryTotalTag(t *testing.T) {
	cases := []struct {
		name  string
		bytes int64
		want  string
	}{
		{"rtx3080-fixture-16gb", 16777216000, "16"}, // real login-history capture
		{"8gib-exact", 8 * (1 << 30), "8"},
		{"12gib-exact", 12 * (1 << 30), "12"},
		{"16gib-exact", 16 * (1 << 30), "16"},
		{"32gib-exact", 32 * (1 << 30), "32"},
		{"128gib-exact", 128 * (1 << 30), "128"},
		{"4gib-exact", 4 * (1 << 30), "4"},
		{"2gib-below-band", 2 * (1 << 30), metricTagOther},
		{"mock-16384-bytes-tiny", 16384, metricTagOther},
		{"zero", 0, metricTagOther},
		{"negative", -1 << 40, metricTagOther},
		{"300gib-above-band", 300 * (1 << 30), metricTagOther},
		{"absurd-petabyte", 1 << 50, metricTagOther},
	}
	for _, c := range cases {
		if got := boundMemoryTotalTag(c.bytes); got != c.want {
			t.Errorf("%s: boundMemoryTotalTag(%d) = %q, want %q", c.name, c.bytes, got, c.want)
		}
	}
}

// TestBoundCoreCountTag proves core counts clamp to a plausible range, out-of-range
// values collapsing to metricTagOther.
func TestBoundCoreCountTag(t *testing.T) {
	cases := []struct {
		cores int64
		want  string
	}{
		{1, "1"}, {8, "8"}, {16, "16"}, {64, "64"},
		{0, metricTagOther}, {65, metricTagOther}, {-4, metricTagOther}, {1 << 40, metricTagOther},
	}
	for _, c := range cases {
		if got := boundCoreCountTag(c.cores); got != c.want {
			t.Errorf("boundCoreCountTag(%d) = %q, want %q", c.cores, got, c.want)
		}
	}
}

// TestSystemInfoIntTagCardinality is the SEC-4 int-field property: N randomized /
// adversarial memory & core payloads must NOT produce N distinct tag values. Each
// int field is bounded by its bucket set (+1 for the "other" sentinel).
func TestSystemInfoIntTagCardinality(t *testing.T) {
	const n = 2000
	rng := rand.New(rand.NewSource(7))
	intFields := []string{"total_memory", "num_logical_cores", "num_physical_cores"}

	distinct := make(map[string]map[string]struct{}, len(intFields))
	for _, f := range intFields {
		distinct[f] = make(map[string]struct{})
	}

	for i := 0; i < n; i++ {
		si := evr.SystemInfo{
			MemoryTotal:      rng.Int63(),
			NumLogicalCores:  rng.Int63(),
			NumPhysicalCores: rng.Int63(),
		}
		if i%3 == 0 { // also exercise small / in-band adversarial values
			si.MemoryTotal = int64(rng.Intn(1 << 20))
			si.NumLogicalCores = int64(rng.Intn(1000))
			si.NumPhysicalCores = int64(rng.Intn(1000))
		}
		tags := systemInfoMetricTags(si)
		for _, f := range intFields {
			distinct[f][tags[f]] = struct{}{}
		}
	}

	for _, f := range intFields {
		bound := systemInfoTagCardinalityBound(f)
		if bound == 0 {
			t.Errorf("field %q: no cardinality bound registered", f)
		}
		if got := len(distinct[f]); got > bound {
			t.Errorf("field %q: %d distinct values from %d random inputs (bound %d) — unbounded cardinality (SEC-4 int)", f, got, n, bound)
		}
	}
}

// TestSystemInfoFingerprintDeterministicAndDistinct proves the per-player
// fingerprint is stable for identical bounded tuples, changes when ANY
// attacker-controlled login tag changes (loginMetricFingerprintFields — including
// the LoginProfile-derived build_number/app_id/publisher_lock), and ignores
// non-attacker keys (e.g. websocket_auth) so churn on them does not manufacture new
// "systems".
func TestSystemInfoFingerprintDeterministicAndDistinct(t *testing.T) {
	mk := func(headset string) map[string]string {
		return map[string]string{
			"network_type": "WiFi", "headset_type": headset,
			"total_memory": "16", "num_logical_cores": "8", "num_physical_cores": "8",
			"build_number": "630783", "app_id": "0", "publisher_lock": "rad15_live",
			"device_type": headset, "build_version": "630783",
		}
	}
	a, b := mk("Meta Quest 3"), mk("Meta Quest 3")
	if systemInfoFingerprint(a) != systemInfoFingerprint(b) {
		t.Errorf("identical bounded tuples must fingerprint equal")
	}
	if systemInfoFingerprint(a) == systemInfoFingerprint(mk("Meta Quest 2")) {
		t.Errorf("different bounded tuples must fingerprint differently")
	}
	// A SEC-4 int field IS part of the fingerprint.
	mem := mk("Meta Quest 3")
	mem["total_memory"] = "32"
	if systemInfoFingerprint(a) == systemInfoFingerprint(mem) {
		t.Errorf("total_memory is a fingerprint field and must affect the fingerprint")
	}
	// LoginProfile-derived fields ARE part of the fingerprint (folded into the cap).
	for _, f := range []string{"build_number", "app_id", "publisher_lock"} {
		v := mk("Meta Quest 3")
		v[f] = "different-bounded-value"
		if systemInfoFingerprint(a) == systemInfoFingerprint(v) {
			t.Errorf("field %q must affect the fingerprint (folded into the per-player cap)", f)
		}
	}
	// A non-attacker key must NOT affect the fingerprint.
	a["websocket_auth"] = "true"
	if systemInfoFingerprint(a) != systemInfoFingerprint(b) {
		t.Errorf("non-attacker keys must not affect the fingerprint")
	}
}

// TestSystemFingerprintLimiterCapsDistinctPerPlayer proves a single player is
// capped at maxPerPlayer distinct systems: the first cap are admitted, further
// NEW fingerprints are denied (collapse to sentinel), and already-seen ones are
// always admitted.
func TestSystemFingerprintLimiterCapsDistinctPerPlayer(t *testing.T) {
	const capN = 4
	l := newSystemFingerprintLimiter(capN, 100)
	const player = "p1"

	for i := 0; i < capN; i++ {
		if !l.allow(player, uint64(i)) {
			t.Fatalf("fingerprint %d: want allowed (under cap), got denied", i)
		}
	}
	if !l.allow(player, 0) {
		t.Errorf("already-seen fingerprint 0: want allowed, got denied")
	}
	for i := capN; i < capN+5; i++ {
		if l.allow(player, uint64(i)) {
			t.Errorf("fingerprint %d: want denied (over cap), got allowed", i)
		}
	}
	if !l.allow(player, uint64(capN-1)) {
		t.Errorf("already-seen fingerprint %d after cap: want allowed, got denied", capN-1)
	}
}

// TestSystemFingerprintLimiterIsPerPlayer proves one player saturating their cap
// does not affect another player's independent budget.
func TestSystemFingerprintLimiterIsPerPlayer(t *testing.T) {
	const capN = 2
	l := newSystemFingerprintLimiter(capN, 100)

	l.allow("A", 1)
	l.allow("A", 2)
	if l.allow("A", 3) {
		t.Fatalf("player A over cap: want denied")
	}
	for i := uint64(0); i < uint64(capN); i++ {
		if !l.allow("B", 100+i) {
			t.Errorf("player B fingerprint %d: want allowed, got denied", 100+i)
		}
	}
}

// TestSystemFingerprintLimiterEvictsLRUPlayers proves the tracker is
// memory-bounded: it never holds more than maxPlayers, evicting the
// least-recently-used player, whose state resets on re-appearance.
func TestSystemFingerprintLimiterEvictsLRUPlayers(t *testing.T) {
	const maxPlayers = 3
	l := newSystemFingerprintLimiter(4, maxPlayers)

	for p := 0; p < maxPlayers; p++ {
		l.allow(fmt.Sprintf("p%d", p), 1)
	}
	if got := l.trackedPlayers(); got != maxPlayers {
		t.Fatalf("tracked players: got %d, want %d", got, maxPlayers)
	}
	// New player overflows the tracker -> LRU (p0) evicted, stays bounded.
	l.allow("p3", 1)
	if got := l.trackedPlayers(); got != maxPlayers {
		t.Fatalf("tracked players after overflow: got %d, want %d (LRU not bounded)", got, maxPlayers)
	}
	// p0 was evicted: fresh state, admits up to cap distinct systems again.
	for f := 0; f < 4; f++ {
		if !l.allow("p0", uint64(1000+f)) {
			t.Errorf("evicted p0 fresh fingerprint %d: want allowed, got denied", 1000+f)
		}
	}
}

// TestSystemFingerprintLimiterConcurrent exercises allow() under the race
// detector and asserts the player LRU stays bounded under contention.
func TestSystemFingerprintLimiterConcurrent(t *testing.T) {
	l := newSystemFingerprintLimiter(8, 64)
	var wg sync.WaitGroup
	for g := 0; g < 32; g++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(seed)))
			for i := 0; i < 500; i++ {
				l.allow(fmt.Sprintf("p%d", rng.Intn(128)), rng.Uint64())
			}
		}(g)
	}
	wg.Wait()
	if got := l.trackedPlayers(); got > 64 {
		t.Errorf("tracked players %d exceeds max 64 under concurrency", got)
	}
}

// TestBoundSystemsPerPlayerCollapsesBeyondCap proves the integration helper: the
// first cap distinct systems keep their bounded tags, systems beyond the cap
// collapse every SEC-4 field to metricTagOther, and a previously-admitted system
// is still emitted with its real tags.
func TestBoundSystemsPerPlayerCollapsesBeyondCap(t *testing.T) {
	const capN = 2
	l := newSystemFingerprintLimiter(capN, 100)
	const player = "abuser"

	mk := func(headset string) map[string]string {
		return map[string]string{
			"cpu_model":      metricTagOther,
			"gpu_model":      metricTagOther,
			"network_type":   "WiFi",
			"driver_version": metricTagOther,
			"headset_type":   headset,
		}
	}

	systems := []string{"sys-a", "sys-b", "sys-c", "sys-d"}
	for i, s := range systems {
		tags := mk(s)
		boundSystemsPerPlayer(l, tags, player)
		if i < capN {
			if tags["headset_type"] != s {
				t.Errorf("system %d under cap: headset_type collapsed to %q, want %q", i, tags["headset_type"], s)
			}
			continue
		}
		for _, f := range systemInfoMetricTagFields {
			if tags[f] != metricTagOther {
				t.Errorf("system %d over cap: field %q = %q, want %q", i, f, tags[f], metricTagOther)
			}
		}
	}

	// A previously-admitted system is still emitted with its real tags.
	tags := mk("sys-a")
	boundSystemsPerPlayer(l, tags, player)
	if tags["headset_type"] != "sys-a" {
		t.Errorf("re-seen system: headset_type = %q, want %q", tags["headset_type"], "sys-a")
	}
}

// TestBoundSystemsPerPlayerEmptyPlayerIDSkips proves an unavailable player ID is
// NOT tracked (which would pool unrelated players under one key) and never
// collapses tags.
func TestBoundSystemsPerPlayerEmptyPlayerIDSkips(t *testing.T) {
	l := newSystemFingerprintLimiter(1, 100)
	for i := 0; i < 10; i++ {
		want := fmt.Sprintf("sys-%d", i)
		tags := map[string]string{
			"cpu_model":      metricTagOther,
			"gpu_model":      metricTagOther,
			"network_type":   "WiFi",
			"driver_version": metricTagOther,
			"headset_type":   want,
		}
		boundSystemsPerPlayer(l, tags, "")
		if tags["headset_type"] != want {
			t.Errorf("empty playerID system %d: collapsed to %q, want passthrough %q", i, tags["headset_type"], want)
		}
	}
	if got := l.trackedPlayers(); got != 0 {
		t.Errorf("empty playerID must not be tracked: tracked=%d, want 0", got)
	}
}

// TestSystemInfoMetricTagKnownGoodPassthrough proves the fix does not lose
// legitimate cardinality: values on the allow-list survive unchanged, while
// unknown values bucket to the sentinel rather than passing raw through.
func TestSystemInfoMetricTagKnownGoodPassthrough(t *testing.T) {
	tags := systemInfoMetricTags(evr.SystemInfo{
		NetworkType: "WiFi",    // seeded allow-list value
		HeadsetType: "Quest 3", // maps to canonical "Meta Quest 3"
	})

	if got := tags["network_type"]; got != "WiFi" {
		t.Errorf("network_type: known-good value not preserved: got %q, want %q", got, "WiFi")
	}
	if got := tags["headset_type"]; got != "Meta Quest 3" {
		t.Errorf("headset_type: known headset not normalized: got %q, want %q", got, "Meta Quest 3")
	}
	// An unlisted headset must bucket to the sentinel, not pass raw.
	if got := boundHeadsetMetricTag("totally-made-up-headset"); got != metricTagOther {
		t.Errorf("unlisted headset: not bucketed: got %q, want %q", got, metricTagOther)
	}
}
