package server

import (
	"regexp"
	"sync"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

// nevrRuntimeSessionsGauge is the gauge of logged-in EVR sessions, by the nevr-runtime version and the
// device type the client declared at login. CustomGauge sets an absolute value, so the count is kept by
// nevrRuntimeSessionTracker and the gauge is set from it on every session start and end.
const nevrRuntimeSessionsGauge = "nevr_runtime_sessions_gauge"

const (
	// nevrVersionStock labels a session whose client declared no nevr_identity: the stock game.
	nevrVersionStock = "stock"
	// nevrVersionUnknown labels a session whose client declared an identity with no usable version.
	nevrVersionUnknown = "unknown"
	// maxNevrVersionLabels bounds the distinct versions that get their own series. The version is client
	// input, so a client that varies it per login must not mint a series per value. Past the cap a version
	// is bucketed to metricTagOther, the same sentinel the SEC-4 allow-lists use.
	maxNevrVersionLabels = 32
	// maxNevrVersionLabelLen caps the length of a version label; a longer one is unknown.
	maxNevrVersionLabelLen = 64
)

// nevrVersionPattern accepts MAJOR.MINOR.PATCH with an optional leading "v" and an optional prerelease,
// the semver shapes nevr-runtime declares. Build metadata ("+...") is accepted and dropped from the label,
// because it does not identify a build line. Group 1 is the label.
var nevrVersionPattern = regexp.MustCompile(`^v?((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?)(?:\+[0-9A-Za-z.-]+)?$`)

// normalizeNevrVersion returns the version label for the identity a client declared at login. A nil
// identity is the stock game. An identity whose version does not parse as semver is unknown. The result
// is still unbounded in principle (any well-formed version); the cap in nevrRuntimeSessionTracker bounds it.
func normalizeNevrVersion(identity *evr.NevrIdentity) string {
	if identity == nil {
		return nevrVersionStock
	}
	match := nevrVersionPattern.FindStringSubmatch(identity.Version)
	if match == nil || len(match[1]) > maxNevrVersionLabelLen {
		return nevrVersionUnknown
	}
	return match[1]
}

// nevrSessionKey is the label set of one series of nevrRuntimeSessionsGauge.
type nevrSessionKey struct {
	version string
	device  string
}

func (k nevrSessionKey) tags() map[string]string {
	return map[string]string{"nevr_version": k.version, "device_type": k.device}
}

// gaugeSetter is the signature of Metrics.CustomGauge.
type gaugeSetter func(name string, tags map[string]string, value float64)

// nevrRuntimeSessionTracker counts open sessions per nevrSessionKey. The mutex covers the counts, the set
// of versions that have their own series, and the gauge writes, so the gauge can't be left behind the
// counts by two sessions starting or ending at once.
type nevrRuntimeSessionTracker struct {
	mu       sync.Mutex
	open     map[nevrSessionKey]int
	versions map[string]struct{}
}

func newNevrRuntimeSessionTracker() *nevrRuntimeSessionTracker {
	return &nevrRuntimeSessionTracker{
		open:     make(map[nevrSessionKey]int),
		versions: make(map[string]struct{}),
	}
}

// nevrRuntimeSessions is the process-wide tracker. A session's labels are decided once, at login.
var nevrRuntimeSessions = newNevrRuntimeSessionTracker()

// keyFor returns the series a session with this identity and device belongs to. The version is bounded
// here: a version seen before, or one that fits under the cap, keeps its own label. Anything past the cap
// is metricTagOther. The sentinels stock and unknown are fixed strings and take no slot.
func (t *nevrRuntimeSessionTracker) keyFor(identity *evr.NevrIdentity, device string) nevrSessionKey {
	version := normalizeNevrVersion(identity)
	t.mu.Lock()
	defer t.mu.Unlock()
	if version != nevrVersionStock && version != nevrVersionUnknown {
		if _, ok := t.versions[version]; !ok {
			if len(t.versions) >= maxNevrVersionLabels {
				version = metricTagOther
			} else {
				t.versions[version] = struct{}{}
			}
		}
	}
	return nevrSessionKey{version: version, device: device}
}

// move adds delta to the open-session count of key and writes the gauge for that series. A count that
// reaches zero is dropped, so the map holds only series with a session open.
func (t *nevrRuntimeSessionTracker) move(key nevrSessionKey, delta int, set gaugeSetter) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := t.open[key] + delta
	if n <= 0 {
		delete(t.open, key)
		n = 0
	} else {
		t.open[key] = n
	}
	set(nevrRuntimeSessionsGauge, key.tags(), float64(n))
}

// nevrIdentity is the nevr_identity the session's client declared at login, nil for a stock client.
func (s *SessionParameters) nevrIdentity() *evr.NevrIdentity {
	if s == nil || s.loginPayload == nil {
		return nil
	}
	return s.loginPayload.NevrIdentity
}

// trackNevrRuntimeSession counts a session that has logged in and returns the function that uncounts it
// when the session ends. Labels are decided here, once, so the decrement names the same series as the
// increment even if the client's declared state changes later.
func trackNevrRuntimeSession(t *nevrRuntimeSessionTracker, params *SessionParameters, set gaugeSetter) (end func()) {
	key := t.keyFor(params.nevrIdentity(), params.boundDeviceType())
	t.move(key, 1, set)
	return func() { t.move(key, -1, set) }
}
