package server

import (
	"fmt"
	"reflect"
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
func TestNevrRuntimeSessionTrackerCapsDistinctVersions(t *testing.T) {
	tr := newNevrRuntimeSessionTracker()
	for i := 0; i < maxNevrVersionLabels; i++ {
		v := fmt.Sprintf("1.%d.0", i)
		if got := tr.keyFor(&evr.NevrIdentity{Version: v}, "d").version; got != v {
			t.Fatalf("version %q under the cap = %q, want its own label", v, got)
		}
	}

	if got := tr.keyFor(&evr.NevrIdentity{Version: "9.9.9"}, "d").version; got != metricTagOther {
		t.Fatalf("version past the cap = %q, want %q", got, metricTagOther)
	}
	if got := tr.keyFor(&evr.NevrIdentity{Version: "1.0.0"}, "d").version; got != "1.0.0" {
		t.Fatalf("version already labelled = %q, want 1.0.0", got)
	}
	if got := tr.keyFor(nil, "d").version; got != nevrVersionStock {
		t.Fatalf("stock = %q, want %q", got, nevrVersionStock)
	}
	if got := tr.keyFor(&evr.NevrIdentity{Version: "junk"}, "d").version; got != nevrVersionUnknown {
		t.Fatalf("unknown = %q, want %q", got, nevrVersionUnknown)
	}
	if n := len(tr.versions); n != maxNevrVersionLabels {
		t.Fatalf("labelled versions = %d, want %d (sentinels must not take a slot)", n, maxNevrVersionLabels)
	}
}

// A client that varies its version on every login gets at most maxNevrVersionLabels+1 distinct labels.
func TestNevrRuntimeSessionTrackerFloodStaysBounded(t *testing.T) {
	tr := newNevrRuntimeSessionTracker()
	labels := map[string]struct{}{}
	for i := 0; i < 1000; i++ {
		labels[tr.keyFor(&evr.NevrIdentity{Version: fmt.Sprintf("2.%d.0", i)}, "d").version] = struct{}{}
	}
	if len(labels) != maxNevrVersionLabels+1 {
		t.Fatalf("distinct version labels after 1000 versions = %d, want %d", len(labels), maxNevrVersionLabels+1)
	}
}

type gaugeWrite struct {
	name  string
	tags  map[string]string
	value float64
}

type gaugeRecorder struct {
	writes []gaugeWrite
}

func (r *gaugeRecorder) set(name string, tags map[string]string, value float64) {
	r.writes = append(r.writes, gaugeWrite{name: name, tags: tags, value: value})
}

func loginParamsFor(identity *evr.NevrIdentity, headset string) *SessionParameters {
	return &SessionParameters{loginPayload: &evr.LoginProfile{
		NevrIdentity: identity,
		SystemInfo:   evr.SystemInfo{HeadsetType: headset},
	}}
}

func sessionTags(version, device string) map[string]string {
	return map[string]string{"nevr_version": version, "device_type": device}
}

// The gauge rises when a session logs in and falls when it ends, per series, and a series with no session
// open is written at 0 and dropped from the map.
func TestNevrRuntimeSessionsGaugeIncDecOnStartAndEnd(t *testing.T) {
	tr := newNevrRuntimeSessionTracker()
	rec := &gaugeRecorder{}

	endA := trackNevrRuntimeSession(tr, loginParamsFor(&evr.NevrIdentity{Version: "4.2.0"}, "Meta Quest 3"), rec.set)
	endB := trackNevrRuntimeSession(tr, loginParamsFor(&evr.NevrIdentity{Version: "4.2.0"}, "Meta Quest 3"), rec.set)
	endC := trackNevrRuntimeSession(tr, loginParamsFor(nil, "Meta Quest 3"), rec.set)
	endB()
	endA()
	endC()

	want := []gaugeWrite{
		{nevrRuntimeSessionsGauge, sessionTags("4.2.0", "Meta Quest 3"), 1},
		{nevrRuntimeSessionsGauge, sessionTags("4.2.0", "Meta Quest 3"), 2},
		{nevrRuntimeSessionsGauge, sessionTags("stock", "Meta Quest 3"), 1},
		{nevrRuntimeSessionsGauge, sessionTags("4.2.0", "Meta Quest 3"), 1},
		{nevrRuntimeSessionsGauge, sessionTags("4.2.0", "Meta Quest 3"), 0},
		{nevrRuntimeSessionsGauge, sessionTags("stock", "Meta Quest 3"), 0},
	}
	if !reflect.DeepEqual(rec.writes, want) {
		t.Fatalf("gauge writes\n got: %+v\nwant: %+v", rec.writes, want)
	}
	if len(tr.open) != 0 {
		t.Fatalf("open series after every session ended = %d, want 0", len(tr.open))
	}
}
