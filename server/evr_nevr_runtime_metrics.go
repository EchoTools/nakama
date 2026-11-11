package server

import (
	"regexp"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

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
	// nevrVersionDev labels a login from a development build of nevr-runtime.
	nevrVersionDev = "dev"
	// maxNevrVersionLabels bounds the distinct versions that get their own series. The version is client
	// input, so a client that varies it per login must not mint a series per value. Past the cap a version
	// is bucketed to metricTagOther, the same sentinel the SEC-4 allow-lists use.
	maxNevrVersionLabels = 32
	// maxNevrVersionLabelLen caps the length of a version label; a longer one is unknown.
	maxNevrVersionLabelLen = 64
)

// nevrVersionTagCardinalityBound is the most distinct values nevr_version can take: the labelled
// versions plus the stock, unknown, dev and other sentinels.
const nevrVersionTagCardinalityBound = maxNevrVersionLabels + 4

// nevrDevVersionMarker is the part of a nevr-runtime version that marks a development build. nevr-runtime
// stamps every build that is not a CI build on a release tag X.Y.(Z+1)-dev.<N>+<sha>
// (nevr-runtime cmake/set_project_version_from_git.cmake; .github/workflows/build.yml), and decides
// NEVR_BUILD_IS_DEV by searching the version for this same "-dev." (cmake/nevr_build_info.cmake). The
// version is all the login declares, so a CI build of an rc tag, which nevr-runtime itself counts as a
// release although its version carries "-dev.", is a dev build here.
const nevrDevVersionMarker = "-dev."

// nevrVersionPattern accepts MAJOR.MINOR.PATCH with an optional leading "v" and an optional prerelease,
// the semver shapes nevr-runtime declares. Build metadata ("+...") is accepted and dropped from the label,
// because it does not identify a build line. Group 1 is the label.
var nevrVersionPattern = regexp.MustCompile(`^v?((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?)(?:\+[0-9A-Za-z.-]+)?$`)

// normalizeNevrVersion returns the version label for the identity a client declared at login. A nil
// identity is the stock game. An identity whose version does not parse as semver is unknown. A development
// build, whatever its number and commit, is dev. The result is still unbounded in principle (any
// well-formed release version); the cap in nevrVersionLabeler bounds it.
func normalizeNevrVersion(identity *evr.NevrIdentity) string {
	if identity == nil {
		return nevrVersionStock
	}
	match := nevrVersionPattern.FindStringSubmatch(identity.Version)
	if match == nil || len(match[1]) > maxNevrVersionLabelLen {
		return nevrVersionUnknown
	}
	if strings.Contains(match[1], nevrDevVersionMarker) {
		return nevrVersionDev
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
// sentinels stock, unknown and dev are fixed strings and take no slot.
func (l *nevrVersionLabeler) label(identity *evr.NevrIdentity) string {
	version := normalizeNevrVersion(identity)
	if version == nevrVersionStock || version == nevrVersionUnknown || version == nevrVersionDev {
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

// nevrVersionFirstSeenCounter counts accounts, not logins: it is incremented the first time an account
// logs in with a given nevr-runtime version on a given UTC day. increase(...[1d]) by nevr_version is the
// number of distinct accounts that ran each version that day. The login declares no install id, so the
// account is the unit; an account on two machines counts once.
const nevrVersionFirstSeenCounter = "nevr_version_first_seen"

// nevrVersionFirstSeenToday reports whether history holds no login of this normalized version at or after
// the start of now's UTC day. The history is what the login path already loads and stores per account, so
// the answer survives a restart. Known overcount: Update overwrites an entry's LoginData, so an account
// that goes v1, v2, v1 on one IP in one day counts v1 twice.
func nevrVersionFirstSeenToday(h *LoginHistory, version string, now time.Time) bool {
	dayStart := now.UTC().Truncate(24 * time.Hour)
	for _, e := range h.History {
		if e == nil || e.LoginData == nil || e.UpdatedAt.Before(dayStart) {
			continue
		}
		if normalizeNevrVersion(e.LoginData.NevrIdentity) == version {
			return false
		}
	}
	return true
}

// emitNevrVersionFirstSeen increments nevrVersionFirstSeenCounter and logs it. The caller decides first-seen
// with nevrVersionFirstSeenToday before it writes the login into the history, and calls this only once the
// login can no longer be rejected: the history is saved after the last rejection, so a count made earlier
// is made again on every retry of a rejected login. The label is bounded by labels.
func emitNevrVersionFirstSeen(logger *zap.Logger, m Metrics, labels *nevrVersionLabeler, userID string, login *evr.LoginProfile, now time.Time) {
	label := labels.label(nevrLoginIdentity(login))
	m.CustomCounter(nevrVersionFirstSeenCounter, map[string]string{nevrVersionTag: label}, 1)
	logger.Info("nevr version first seen",
		zap.String("user_id", userID),
		zap.String(nevrVersionTag, label),
		zap.String("day", now.UTC().Format("2006-01-02")))
}

// recordNevrVersionFirstSeen decides and emits in one step, for callers with nothing between the two.
func recordNevrVersionFirstSeen(logger *zap.Logger, m Metrics, labels *nevrVersionLabeler, h *LoginHistory, userID string, login *evr.LoginProfile, now time.Time) {
	if nevrVersionFirstSeenToday(h, normalizeNevrVersion(nevrLoginIdentity(login)), now) {
		emitNevrVersionFirstSeen(logger, m, labels, userID, login, now)
	}
}

// nevrLoginIdentity is the declared nevr_identity of a login, nil for a nil login or a stock client.
func nevrLoginIdentity(login *evr.LoginProfile) *evr.NevrIdentity {
	if login == nil {
		return nil
	}
	return login.NevrIdentity
}
