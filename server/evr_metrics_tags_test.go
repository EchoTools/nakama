package server

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

// sec4Fields are the attacker-controlled SystemInfo strings that flow into
// login metric tags (BUGS.md SEC-4).
var sec4Fields = []string{"cpu_model", "gpu_model", "network_type", "driver_version", "headset_type"}

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

// TestSystemInfoMetricTagKnownGoodPassthrough proves the fix does not lose
// legitimate cardinality: values on the allow-list survive unchanged, while
// unknown values bucket to the sentinel rather than passing raw through.
func TestSystemInfoMetricTagKnownGoodPassthrough(t *testing.T) {
	tags := systemInfoMetricTags(evr.SystemInfo{
		NetworkType: "WiFi",    // seeded allow-list value
		HeadsetType: "Quest 3", // maps to canonical "Meta Quest 3"
		CPUModel:    "totally-made-up-cpu-string",
	})

	if got := tags["network_type"]; got != "WiFi" {
		t.Errorf("network_type: known-good value not preserved: got %q, want %q", got, "WiFi")
	}
	if got := tags["headset_type"]; got != "Meta Quest 3" {
		t.Errorf("headset_type: known headset not normalized: got %q, want %q", got, "Meta Quest 3")
	}
	if got := tags["cpu_model"]; got != metricTagOther {
		t.Errorf("cpu_model: unknown value not bucketed: got %q, want %q", got, metricTagOther)
	}
}
