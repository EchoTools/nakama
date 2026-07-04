package server

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// This file drives the ACTUAL login metric-emission path (buildLoginSuccessMetricTags
// and SessionParameters.MetricsTags), not the SEC-4 helpers in isolation. The
// pre-existing evr_metrics_tags_test.go tests systemInfoMetricTags(si) — a map the
// login_success counter never emits — so it stayed green while login_success still
// carried raw device_type/build_version/build_number/app_id/publisher_lock. These
// tests assert boundedness on the real emitted tuple, across EVERY attacker-controlled
// field, and fail if any one flows raw.

// loginAttackerTagFields are every attacker-controlled tag key emitted on the
// login_success metric. A randomized login payload must not be able to mint an
// unbounded number of distinct values for ANY of them.
var loginAttackerTagFields = []string{
	"network_type", "headset_type", "total_memory",
	"num_logical_cores", "num_physical_cores",
	"build_number", "app_id", "publisher_lock",
	"device_type", "build_version",
}

// maxDistinctPerLoginField is a generous per-field ceiling: larger than any single
// bounded field's legitimate cardinality (headset ~= len(canonicalHeadsetTypes)+1,
// cores = 65, memory = 11, build = 3, app = 4, publisher = 3, network = 2) yet far
// below N, so a raw (unbounded) field trips it decisively.
const maxDistinctPerLoginField = 100

// adversarialLoginPayload returns a LoginProfile whose every attacker-controlled
// field is unique per call — the DoS model: one client randomizing the payload on
// each login to churn Prometheus series.
func adversarialLoginPayload(rng *rand.Rand, i int) *evr.LoginProfile {
	return &evr.LoginProfile{
		BuildNumber:   evr.BuildNumber(rng.Int63()),
		AppId:         rng.Uint64(),
		PublisherLock: fmt.Sprintf("pub-%d-%d", i, rng.Int63()),
		SystemInfo: evr.SystemInfo{
			HeadsetType:      fmt.Sprintf("hmd-%d-%d", i, rng.Int63()),
			DriverVersion:    fmt.Sprintf("drv-%d-%d", i, rng.Int63()),
			NetworkType:      fmt.Sprintf("net-%d-%d", i, rng.Int63()),
			VideoCard:        fmt.Sprintf("gpu-%d-%d", i, rng.Int63()),
			CPUModel:         fmt.Sprintf("cpu-%d-%d", i, rng.Int63()),
			NumPhysicalCores: rng.Int63(),
			NumLogicalCores:  rng.Int63(),
			MemoryTotal:      rng.Int63(),
		},
	}
}

func paramsWithPayload(payload *evr.LoginProfile, userID string) *SessionParameters {
	p := &SessionParameters{
		IsWebsocketAuthenticated: true,
		loginPayload:             payload,
	}
	if userID != "" {
		p.profile = &EVRProfile{account: &api.Account{User: &api.User{Id: userID}}}
	}
	return p
}

func tupleKey(tags map[string]string) string {
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(tags[k])
		b.WriteByte('\x00')
	}
	return b.String()
}

// TestLoginSuccessPerFieldCardinalityBounded drives buildLoginSuccessMetricTags —
// the real login_success emission — with N adversarial payloads and asserts NO
// attacker-controlled field yields more than maxDistinctPerLoginField distinct
// values. playerID is empty so the per-player cap is skipped: this isolates the
// per-field allow-list/bucket bounding. FAILS pre-fix on build_number, app_id,
// publisher_lock, device_type, build_version (all emitted raw).
func TestLoginSuccessPerFieldCardinalityBounded(t *testing.T) {
	const n = 2000
	rng := rand.New(rand.NewSource(1))
	limiter := newSystemFingerprintLimiter(maxDistinctSystemsPerPlayer, maxTrackedPlayersForSystemLimit)

	distinct := make(map[string]map[string]struct{}, len(loginAttackerTagFields))
	for _, f := range loginAttackerTagFields {
		distinct[f] = make(map[string]struct{})
	}

	for i := 0; i < n; i++ {
		params := paramsWithPayload(adversarialLoginPayload(rng, i), "") // empty ID => cap skipped
		tags := buildLoginSuccessMetricTags(params, limiter)
		for _, f := range loginAttackerTagFields {
			distinct[f][tags[f]] = struct{}{}
		}
	}

	for _, f := range loginAttackerTagFields {
		if got := len(distinct[f]); got > maxDistinctPerLoginField {
			t.Errorf("login_success field %q: %d distinct values from %d adversarial logins (ceiling %d) — flows raw, unbounded cardinality (SEC-4)", f, got, n, maxDistinctPerLoginField)
		}
	}
}

// TestLoginSuccessPerPlayerTupleBounded proves the per-player invariant: a single
// account, however it randomizes the payload, cannot mint an unbounded number of
// distinct login_success tag-tuples. FAILS pre-fix because build_number/app_id/
// publisher_lock/device_type/build_version are outside the fingerprint cap and are
// emitted raw, so each varying login is a fresh series.
func TestLoginSuccessPerPlayerTupleBounded(t *testing.T) {
	const n = 2000
	rng := rand.New(rand.NewSource(3))
	limiter := newSystemFingerprintLimiter(maxDistinctSystemsPerPlayer, maxTrackedPlayersForSystemLimit)
	const player = "single-abuser"

	tuples := make(map[string]struct{})
	for i := 0; i < n; i++ {
		params := paramsWithPayload(adversarialLoginPayload(rng, i), player)
		tuples[tupleKey(buildLoginSuccessMetricTags(params, limiter))] = struct{}{}
	}

	// Non-attacker tags are constant here, so the only variation is the bounded
	// attacker tuple, capped per player: at most maxDistinctSystemsPerPlayer admitted
	// systems plus the single all-"other" collapsed tuple.
	if got, want := len(tuples), maxDistinctSystemsPerPlayer+1; got > want {
		t.Errorf("single player emitted %d distinct login_success tuples from %d adversarial logins (bound %d) — a client can churn series (SEC-4)", got, n, want)
	}
}

// legalHeadsetInputs / legalAppIDs are legitimate bounded values used to walk the
// allowed tuple space and prove the per-player cap folds in the LoginProfile-derived
// fields (build_number/app_id/publisher_lock), not just SystemInfo.
func legalHeadsetInputs() []string {
	out := make([]string, 0, 4)
	for _, canon := range headsetMappings { // canonical names round-trip through boundHeadsetMetricTag
		out = append(out, canon)
		if len(out) == 4 {
			break
		}
	}
	sort.Strings(out) // determinism across map iteration order
	return out
}

// TestLoginSuccessCapFoldsProfileFields walks a single player across the LEGAL
// bounded value space (canonical headsets x memory tiers x known builds x known
// app IDs x known publisher locks). Every value is allow-listed, so per-field
// bounding alone does NOT collapse them — only folding build_number/app_id/
// publisher_lock into the per-player fingerprint cap keeps the distinct-tuple count
// at or below maxDistinctSystemsPerPlayer+1. FAILS pre-fix, where those fields sit
// outside the fingerprint and multiply past the cap.
func TestLoginSuccessCapFoldsProfileFields(t *testing.T) {
	limiter := newSystemFingerprintLimiter(maxDistinctSystemsPerPlayer, maxTrackedPlayersForSystemLimit)
	const player = "walker"

	headsets := legalHeadsetInputs()
	mems := []int64{8, 16, 32}
	builds := evr.KnownBuilds
	apps := []uint64{NoOvrAppId, QuestAppId, PcvrAppId}
	pubs := []string{"rad15_live", "echovrce"}

	tuples := make(map[string]struct{})
	for _, hs := range headsets {
		for _, mem := range mems {
			for _, b := range builds {
				for _, app := range apps {
					for _, pub := range pubs {
						payload := &evr.LoginProfile{
							BuildNumber:   b,
							AppId:         app,
							PublisherLock: pub,
							SystemInfo: evr.SystemInfo{
								HeadsetType:     hs,
								NetworkType:     "WiFi",
								MemoryTotal:     mem * bytesPerGiB,
								NumLogicalCores: 8,
							},
						}
						params := paramsWithPayload(payload, player)
						tuples[tupleKey(buildLoginSuccessMetricTags(params, limiter))] = struct{}{}
					}
				}
			}
		}
	}

	combos := len(headsets) * len(mems) * len(builds) * len(apps) * len(pubs)
	// The cap admits at most maxDistinctSystemsPerPlayer distinct systems; beyond it
	// every attacker-controlled tag collapses to metricTagOther. The collapsed bucket
	// can still splay by the two bounded derived booleans MetricsTags emits but does
	// NOT fold into the fingerprint — is_vr and is_pcvr (2x2 = 4 max, both legitimate
	// low-cardinality dimensions). So the per-player distinct-tuple bound is
	// maxDistinctSystemsPerPlayer + 4. Without folding the profile fields into the cap,
	// this same walk mints one series per legal combination (see the pre-fix RED run).
	const derivedBoolSplay = 2 * 2 // is_vr x is_pcvr
	if got, want := len(tuples), maxDistinctSystemsPerPlayer+derivedBoolSplay; got > want {
		t.Errorf("single player walking %d legal bounded combinations emitted %d distinct login_success tuples (bound %d) — profile fields not folded into the per-player fingerprint cap (SEC-4)", combos, got, want)
	}
}

// TestSiblingMetricTagsBounded drives SessionParameters.MetricsTags directly — the
// shared source for login_process_latency, session_duration_seconds,
// session_authenticate/authorize/initialize — and asserts its attacker-controlled
// tags (device_type, build_version) are bounded across adversarial payloads. FAILS
// pre-fix: MetricsTags emits normalizeHeadsetType(raw) and the raw build number.
func TestSiblingMetricTagsBounded(t *testing.T) {
	const n = 2000
	rng := rand.New(rand.NewSource(5))

	device := make(map[string]struct{})
	build := make(map[string]struct{})
	for i := 0; i < n; i++ {
		params := paramsWithPayload(adversarialLoginPayload(rng, i), "")
		tags := params.MetricsTags()
		device[tags["device_type"]] = struct{}{}
		build[tags["build_version"]] = struct{}{}
	}

	if got := len(device); got > maxDistinctPerLoginField {
		t.Errorf("sibling metric tag device_type: %d distinct values from %d adversarial logins (ceiling %d) — raw headset flows into every MetricsTags-based metric (SEC-4)", got, n, maxDistinctPerLoginField)
	}
	if got := len(build); got > maxDistinctPerLoginField {
		t.Errorf("sibling metric tag build_version: %d distinct values from %d adversarial logins (ceiling %d) — raw build number flows into every MetricsTags-based metric (SEC-4)", got, n, maxDistinctPerLoginField)
	}
}
