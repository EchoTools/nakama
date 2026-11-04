package server

import (
	"fmt"
	"strings"
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

func TestNormalizeNevrVersion(t *testing.T) {
	overlong := "1.0.0-" + strings.Repeat("a", maxNevrVersionLabelLen)
	cases := []struct {
		name     string
		identity *evr.NevrIdentity
		want     string
	}{
		{"no identity is the stock game", nil, nevrVersionStock},
		{"semver", &evr.NevrIdentity{Version: "4.2.0"}, "4.2.0"},
		{"leading v is dropped", &evr.NevrIdentity{Version: "v4.2.0"}, "4.2.0"},
		{"prerelease is kept", &evr.NevrIdentity{Version: "4.2.0-rc.1"}, "4.2.0-rc.1"},
		{"release is its version", &evr.NevrIdentity{Version: "6.16.5"}, "6.16.5"},
		{"dev build is dev", &evr.NevrIdentity{Version: "6.16.5-dev.8"}, nevrVersionDev},
		{"dev build with commit metadata is dev", &evr.NevrIdentity{Version: "6.16.5-dev.8+b0521f78"}, nevrVersionDev},
		{"dev build with a leading v is dev", &evr.NevrIdentity{Version: "v6.16.5-dev.8+b0521f78"}, nevrVersionDev},
		{"only the -dev. part marks a dev build", &evr.NevrIdentity{Version: "4.2.0-developer.1"}, "4.2.0-developer.1"},
		{"build metadata is dropped", &evr.NevrIdentity{Version: "4.2.0+abc.5"}, "4.2.0"},
		{"empty version is unknown", &evr.NevrIdentity{Version: ""}, nevrVersionUnknown},
		{"not a version is unknown", &evr.NevrIdentity{Version: "latest"}, nevrVersionUnknown},
		{"two components is unknown", &evr.NevrIdentity{Version: "4.2"}, nevrVersionUnknown},
		{"leading zero is unknown", &evr.NevrIdentity{Version: "04.2.0"}, nevrVersionUnknown},
		{"markup is unknown", &evr.NevrIdentity{Version: "4.2.0\"}"}, nevrVersionUnknown},
		{"overlong label is unknown", &evr.NevrIdentity{Version: overlong}, nevrVersionUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeNevrVersion(tc.identity); got != tc.want {
				t.Fatalf("normalizeNevrVersion = %q, want %q", got, tc.want)
			}
		})
	}
}

// Past maxNevrVersionLabels distinct versions, a new version is bucketed to metricTagOther, a version
// already labelled keeps its label, and the stock and unknown sentinels take no slot.
func TestNevrVersionLabelerCapsDistinctVersions(t *testing.T) {
	l := newNevrVersionLabeler()
	for i := 0; i < maxNevrVersionLabels; i++ {
		v := fmt.Sprintf("1.%d.0", i)
		if got := l.label(&evr.NevrIdentity{Version: v}); got != v {
			t.Fatalf("version %q under the cap = %q, want its own label", v, got)
		}
	}

	if got := l.label(&evr.NevrIdentity{Version: "9.9.9"}); got != metricTagOther {
		t.Fatalf("version past the cap = %q, want %q", got, metricTagOther)
	}
	if got := l.label(&evr.NevrIdentity{Version: "1.0.0"}); got != "1.0.0" {
		t.Fatalf("version already labelled = %q, want 1.0.0", got)
	}
	if got := l.label(nil); got != nevrVersionStock {
		t.Fatalf("stock = %q, want %q", got, nevrVersionStock)
	}
	if got := l.label(&evr.NevrIdentity{Version: "junk"}); got != nevrVersionUnknown {
		t.Fatalf("unknown = %q, want %q", got, nevrVersionUnknown)
	}
	if n := len(l.versions); n != maxNevrVersionLabels {
		t.Fatalf("labelled versions = %d, want %d (sentinels must not take a slot)", n, maxNevrVersionLabels)
	}
}

// Dev builds are one value, "dev": they take no slot of the maxNevrVersionLabels, so a run of distinct dev
// builds can neither mint series nor push a release out of its own.
func TestNevrVersionLabelerDevBuildsAreOneValueAndTakeNoSlot(t *testing.T) {
	l := newNevrVersionLabeler()
	for i := 0; i < maxNevrVersionLabels; i++ {
		l.label(&evr.NevrIdentity{Version: fmt.Sprintf("1.%d.0", i)})
	}
	for i := 0; i < 100; i++ {
		v := fmt.Sprintf("2.0.1-dev.%d+%07x", i, i)
		if got := l.label(&evr.NevrIdentity{Version: v}); got != nevrVersionDev {
			t.Fatalf("dev build %q = %q, want %q even with every version slot taken", v, got, nevrVersionDev)
		}
	}
	if n := len(l.versions); n != maxNevrVersionLabels {
		t.Fatalf("labelled versions = %d, want %d (dev must not take a slot)", n, maxNevrVersionLabels)
	}
}

// A client that varies its version on every login gets at most maxNevrVersionLabels+1 distinct labels.
func TestNevrVersionLabelerFloodStaysBounded(t *testing.T) {
	l := newNevrVersionLabeler()
	labels := map[string]struct{}{}
	for i := 0; i < 1000; i++ {
		labels[l.label(&evr.NevrIdentity{Version: fmt.Sprintf("2.%d.0", i)})] = struct{}{}
	}
	if len(labels) != maxNevrVersionLabels+1 {
		t.Fatalf("distinct version labels after 1000 versions = %d, want %d", len(labels), maxNevrVersionLabels+1)
	}
}

// useFreshNevrVersionLabels swaps the process-wide labeler for an empty one for the length of a test, so
// the version set other tests have filled does not decide this test's labels.
func useFreshNevrVersionLabels(t *testing.T) {
	t.Helper()
	prev := nevrVersionLabels
	nevrVersionLabels = newNevrVersionLabeler()
	t.Cleanup(func() { nevrVersionLabels = prev })
}

// The version rides on the login_success tag set the login path emits, next to the dimensions it is meant
// to be correlated with, rather than on a metric of its own.
func TestLoginSuccessCarriesNevrVersionWithItsOtherDimensions(t *testing.T) {
	useFreshNevrVersionLabels(t)
	limiter := newSystemFingerprintLimiter(maxDistinctSystemsPerPlayer, maxTrackedPlayersForSystemLimit)

	for _, tc := range []struct {
		name     string
		identity *evr.NevrIdentity
		want     string
	}{
		{"nevr-runtime client", &evr.NevrIdentity{Version: "v4.2.0+abc"}, "4.2.0"},
		{"stock client", nil, nevrVersionStock},
		{"unusable version", &evr.NevrIdentity{Version: "junk"}, nevrVersionUnknown},
		{"empty version", &evr.NevrIdentity{Version: ""}, nevrVersionUnknown},
		{"release", &evr.NevrIdentity{Version: "6.16.5"}, "6.16.5"},
		{"dev build", &evr.NevrIdentity{Version: "6.16.5-dev.8+b0521f78"}, nevrVersionDev},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := &evr.LoginProfile{
				NevrIdentity: tc.identity,
				SystemInfo:   evr.SystemInfo{HeadsetType: "Quest 3", NetworkType: "WiFi"},
			}
			tags := buildLoginSuccessMetricTags(paramsWithPayload(payload, ""), limiter)
			if got := tags[nevrVersionTag]; got != tc.want {
				t.Fatalf("%s = %q, want %q", nevrVersionTag, got, tc.want)
			}
			if got := tags["headset_type"]; got != "Meta Quest 3" {
				t.Fatalf("headset_type = %q, want Meta Quest 3 on the same tag set", got)
			}
			if got := tags["device_type"]; got != "Meta Quest 3" {
				t.Fatalf("device_type = %q, want Meta Quest 3 on the same tag set", got)
			}
		})
	}
}

// One account varying its version cannot burn the process-wide version slots for everyone: the version is
// part of the per-player fingerprint, so past maxDistinctSystemsPerPlayer it collapses to metricTagOther
// and takes no new slot.
func TestLoginSuccessNevrVersionCountsAgainstPerPlayerCap(t *testing.T) {
	useFreshNevrVersionLabels(t)
	limiter := newSystemFingerprintLimiter(maxDistinctSystemsPerPlayer, maxTrackedPlayersForSystemLimit)

	for i := 0; i < 3*maxDistinctSystemsPerPlayer; i++ {
		payload := &evr.LoginProfile{
			NevrIdentity: &evr.NevrIdentity{Version: fmt.Sprintf("3.%d.0", i)},
			SystemInfo:   evr.SystemInfo{HeadsetType: "Quest 3", NetworkType: "WiFi"},
		}
		buildLoginSuccessMetricTags(paramsWithPayload(payload, "one-account"), limiter)
	}

	nevrVersionLabels.mu.Lock()
	n := len(nevrVersionLabels.versions)
	nevrVersionLabels.mu.Unlock()
	if n > maxDistinctSystemsPerPlayer {
		t.Fatalf("one account took %d process-wide version slots, want at most %d", n, maxDistinctSystemsPerPlayer)
	}
}
