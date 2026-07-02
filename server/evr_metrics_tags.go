package server

import (
	"strings"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

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

// systemInfoMetricTags builds the client-controlled portion of the login metric
// tags from a SystemInfo login payload, with every attacker-controlled string
// bounded to its allow-list (unknown values bucket to metricTagOther).
//
// SEC-4: these fields are attacker-controlled JSON from the login payload.
// Emitting the raw strings as metric tag values would let one authenticated
// account mint an unbounded number of distinct Prometheus series (one per
// unique tuple) by randomizing SystemInfo on repeated logins ->
// metrics-backend memory pressure. See BUGS.md SEC-4.
func systemInfoMetricTags(si evr.SystemInfo) map[string]string {
	return map[string]string{
		"cpu_model":      cpuModelAllowlist.normalize(si.CPUModel),
		"gpu_model":      gpuModelAllowlist.normalize(si.VideoCard),
		"network_type":   networkTypeAllowlist.normalize(si.NetworkType),
		"driver_version": driverVersionAllowlist.normalize(si.DriverVersion),
		"headset_type":   boundHeadsetMetricTag(si.HeadsetType),
	}
}
