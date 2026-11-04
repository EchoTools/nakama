package server

import (
	"regexp"
	"sync"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

// nevrVersionTag is the login_success tag that carries the nevr-runtime version the client declared at
// login, so a login can be correlated with the other dimensions on the same series (device_type,
// headset_type, build_number, ...).
const nevrVersionTag = "nevr_version"

const (
	// nevrVersionStock labels a login whose client declared no nevr_identity: the stock game.
	nevrVersionStock = "stock"
	// nevrVersionUnknown labels a login whose client declared an identity with no usable version.
	nevrVersionUnknown = "unknown"
	// maxNevrVersionLabels bounds the distinct versions that get their own series. The version is client
	// input, so a client that varies it per login must not mint a series per value. Past the cap a version
	// is bucketed to metricTagOther, the same sentinel the SEC-4 allow-lists use.
	maxNevrVersionLabels = 32
	// maxNevrVersionLabelLen caps the length of a version label; a longer one is unknown.
	maxNevrVersionLabelLen = 64
)

// nevrVersionTagCardinalityBound is the most distinct values nevr_version can take: the labelled
// versions plus the stock, unknown and other sentinels.
const nevrVersionTagCardinalityBound = maxNevrVersionLabels + 3

// nevrVersionPattern accepts MAJOR.MINOR.PATCH with an optional leading "v" and an optional prerelease,
// the semver shapes nevr-runtime declares. Build metadata ("+...") is accepted and dropped from the label,
// because it does not identify a build line. Group 1 is the label.
var nevrVersionPattern = regexp.MustCompile(`^v?((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?)(?:\+[0-9A-Za-z.-]+)?$`)

// normalizeNevrVersion returns the version label for the identity a client declared at login. A nil
// identity is the stock game. An identity whose version does not parse as semver is unknown. The result
// is still unbounded in principle (any well-formed version); the cap in nevrVersionLabeler bounds it.
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

// nevrVersionLabeler decides the nevr_version tag value for a login and remembers which versions have
// their own series, so the number of series the tag can mint is bounded across the whole process.
type nevrVersionLabeler struct {
	mu       sync.Mutex
	versions map[string]struct{}
}

func newNevrVersionLabeler() *nevrVersionLabeler {
	return &nevrVersionLabeler{versions: make(map[string]struct{})}
}

// nevrVersionLabels is the process-wide labeler for the login path.
var nevrVersionLabels = newNevrVersionLabeler()

// label returns the tag value for this identity. The version is bounded here: a version seen before, or
// one that fits under the cap, keeps its own label. Anything past the cap is metricTagOther. The
// sentinels stock and unknown are fixed strings and take no slot.
func (l *nevrVersionLabeler) label(identity *evr.NevrIdentity) string {
	version := normalizeNevrVersion(identity)
	if version == nevrVersionStock || version == nevrVersionUnknown {
		return version
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.versions[version]; ok {
		return version
	}
	if len(l.versions) >= maxNevrVersionLabels {
		return metricTagOther
	}
	l.versions[version] = struct{}{}
	return version
}

// nevrIdentity is the nevr_identity the session's client declared at login, nil for a stock client.
func (s *SessionParameters) nevrIdentity() *evr.NevrIdentity {
	if s == nil || s.loginPayload == nil {
		return nil
	}
	return s.loginPayload.NevrIdentity
}
