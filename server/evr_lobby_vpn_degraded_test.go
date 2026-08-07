package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// SEC-6 — the VPN gate fails open globally and its degradation is unalertable.
//
// The IPQS circuit breaker is a single process-global flag opened by any single
// failure (1s HTTP timeout, any non-200, or a Success:false body — which is what
// quota exhaustion returns) with backoff 5s→5min. While it is open,
// IPQSClient.Get returns (nil, nil), IPInfoCache.Get falls through to (nil, nil),
// and params.ipInfo is nil for every player. The lobby gate requires
// params.isVPN && params.ipInfo != nil, so VPN blocking is off for every guild
// for up to five minutes — and each uncached login burns two IPQS calls, so an
// attacker cycling fresh VPN IPs can induce the outage themselves.
//
// This PR does not change the fail-open policy. It makes the degraded state
// alertable and stops the warning from burying itself.

// recordingMetrics records CustomCounter calls; everything else is a no-op.
type recordingMetrics struct {
	testMetrics
	mu       sync.Mutex
	counters map[string]int64
	tags     map[string]map[string]string
}

func newRecordingMetrics() *recordingMetrics {
	return &recordingMetrics{counters: make(map[string]int64), tags: make(map[string]map[string]string)}
}

func (m *recordingMetrics) CustomCounter(name string, tags map[string]string, delta int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counters[name] += delta
	m.tags[name] = tags
}

func (m *recordingMetrics) count(name string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.counters[name]
}

func (m *recordingMetrics) tagsFor(name string) map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tags[name]
}

// resetVPNDegradedThrottle clears the process-wide degraded-VPN log throttle
// before the test and again after it.
//
// It deliberately mutates the existing throttle rather than replacing the
// package var: a reassignment is an unsynchronized write to state production
// code reads, and leftover throttle state from a previous test would suppress
// the very log line these tests count — making an assertion of "0 lines" pass
// for entirely the wrong reason.
func resetVPNDegradedThrottle(t *testing.T) {
	t.Helper()
	reset := func() {
		vpnDegradedLogThrottle.reset()
		vpnUnconfiguredLogThrottle.reset()
	}
	reset()
	t.Cleanup(reset)
}

// (a) The degraded state must be alertable. Every sibling rejection path in
// lobbyAuthorize increments a counter; the degradation path is the one place
// where VPN blocking silently stops working, and it had only a bare zap.Warn.
func TestWarnVPNDegraded_IncrementsAlertableCounter(t *testing.T) {
	resetVPNDegradedThrottle(t)

	core, _ := observer.New(zapcore.DebugLevel)
	metrics := newRecordingMetrics()

	warnVPNDegraded(zap.New(core), metrics.CustomCounter, "203.0.113.7", "123456789012345678", "987654321098765432", false, vpnDegradedLookupFailed)

	require.Equal(t, int64(1), metrics.count("lobby_vpn_check_degraded"),
		"SEC-6(a): the degraded VPN gate must emit a counter so it can be alerted on")

	tags := metrics.tagsFor("lobby_vpn_check_degraded")
	require.NotNil(t, tags)
	assert.Equal(t, "987654321098765432", tags["group_id"],
		"the counter must be attributable to the guild whose gate stopped working")
}

// (b) During an outage the warning fired once per lobby authorize per player in
// every VPN-blocking guild, burying its own signal. The counter must still
// count every occurrence (that is the true volume); the log line must not.
func TestWarnVPNDegraded_LogIsThrottledPerGuildButCounterIsNot(t *testing.T) {
	resetVPNDegradedThrottle(t)

	core, logs := observer.New(zapcore.DebugLevel)
	logger := zap.New(core)
	metrics := newRecordingMetrics()

	const guildA = "111111111111111111"
	const guildB = "222222222222222222"

	for i := 0; i < 25; i++ {
		warnVPNDegraded(logger, metrics.CustomCounter, "203.0.113.7", "123456789012345678", guildA, false, vpnDegradedLookupFailed)
	}
	warnVPNDegraded(logger, metrics.CustomCounter, "203.0.113.8", "123456789012345679", guildB, true, vpnDegradedLookupFailed)

	require.Equal(t, int64(26), metrics.count("lobby_vpn_check_degraded"),
		"the counter carries the real per-player volume and must not be throttled")

	entries := logs.FilterMessage("VPN blocking degraded: IPQS lookup unavailable for VPN check").All()
	require.Len(t, entries, 2,
		"SEC-6(b): the warning must be throttled to one line per guild per window — "+
			"26 unthrottled lines per guild per outage bury the signal they exist to raise")

	guilds := []string{entries[0].ContextMap()["guild_id"].(string), entries[1].ContextMap()["guild_id"].(string)}
	assert.ElementsMatch(t, []string{guildA, guildB}, guilds,
		"throttling is per guild: each affected guild must get its own line")
}

func TestWarnVPNDegraded_LogResumesAfterThrottleWindow(t *testing.T) {
	resetVPNDegradedThrottle(t)

	now := time.Now()
	vpnDegradedLogThrottle.setClock(func() time.Time { return now })

	core, logs := observer.New(zapcore.DebugLevel)
	logger := zap.New(core)
	metrics := newRecordingMetrics()

	const guild = "111111111111111111"

	warnVPNDegraded(logger, metrics.CustomCounter, "203.0.113.7", "1", guild, false, vpnDegradedLookupFailed)
	now = now.Add(59 * time.Second)
	warnVPNDegraded(logger, metrics.CustomCounter, "203.0.113.7", "1", guild, false, vpnDegradedLookupFailed)
	now = now.Add(2 * time.Second)
	warnVPNDegraded(logger, metrics.CustomCounter, "203.0.113.7", "1", guild, false, vpnDegradedLookupFailed)

	require.Len(t, logs.FilterMessage("VPN blocking degraded: IPQS lookup unavailable for VPN check").All(), 2,
		"a persistent outage must keep re-announcing itself once the window elapses")
}

// The warning must still carry the triage fields it always carried, plus the
// session's VPN flag — the condition the guard dropped.
func TestWarnVPNDegraded_CarriesTriageFields(t *testing.T) {
	resetVPNDegradedThrottle(t)

	core, logs := observer.New(zapcore.DebugLevel)
	metrics := newRecordingMetrics()

	warnVPNDegraded(zap.New(core), metrics.CustomCounter, "203.0.113.7", "123456789012345678", "987654321098765432", true, vpnDegradedLookupFailed)

	entries := logs.FilterMessage("VPN blocking degraded: IPQS lookup unavailable for VPN check").All()
	require.Len(t, entries, 1)
	require.Equal(t, zapcore.WarnLevel, entries[0].Level)

	ctx := entries[0].ContextMap()
	assert.Equal(t, "203.0.113.7", ctx["client_ip"])
	assert.Equal(t, "123456789012345678", ctx["discord_id"])
	assert.Equal(t, "987654321098765432", ctx["guild_id"])
	assert.Equal(t, true, ctx["session_flagged_vpn"],
		"params.isVPN is the signal the guard dropped; keep it as triage context")
}

// (b) The degradation warning must be scoped the same way the gate it stands in
// for is scoped: a player the guild has explicitly exempted from VPN blocking
// was never going to be rejected, so their lookup failing is not a gap.
func TestShouldWarnVPNDegraded(t *testing.T) {
	for _, tc := range []struct {
		name            string
		blockVPNUsers   bool
		isVPNBypass     bool
		ipInfo          IPInfo
		want            bool
		wantExplainedBy string
	}{
		{name: "degraded in a blocking guild", blockVPNUsers: true, isVPNBypass: false, ipInfo: nil, want: true},
		{name: "guild does not block VPNs", blockVPNUsers: false, isVPNBypass: false, ipInfo: nil, want: false},
		{name: "player is VPN-bypassed", blockVPNUsers: true, isVPNBypass: true, ipInfo: nil, want: false,
			wantExplainedBy: "an exempt player would have passed the gate anyway — not a gap"},
		{name: "lookup succeeded", blockVPNUsers: true, isVPNBypass: false, ipInfo: &StubIPInfo{}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldWarnVPNDegraded(tc.blockVPNUsers, tc.ipInfo, func() bool { return tc.isVPNBypass })
			require.Equal(t, tc.want, got, tc.wantExplainedBy)
		})
	}
}

// Review follow-up. The bypass check reaches through GuildGroup.HasRole into a
// per-guild RLock (server/evr_guild_group.go), so it must stay behind the two
// cheap conditions the way the original `&&` chain had it. Passing a plain bool
// made Go evaluate it on every lobby authorize, including the overwhelmingly
// common case where the guild does not block VPNs at all.
func TestShouldWarnVPNDegraded_DoesNotEvaluateBypassUnlessItMatters(t *testing.T) {
	for _, tc := range []struct {
		name          string
		blockVPNUsers bool
		ipInfo        IPInfo
	}{
		{"guild does not block VPNs", false, nil},
		{"lookup succeeded, so the gate ran normally", true, &StubIPInfo{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			require.False(t, shouldWarnVPNDegraded(tc.blockVPNUsers, tc.ipInfo, func() bool { called = true; return false }))
			require.False(t, called,
				"the bypass lookup takes a per-guild read lock; it must stay short-circuited behind the cheap checks")
		})
	}
}

// Review follow-up (blocking). SEC-6(a) made the degraded gate alertable, but on
// an untagged counter the alert is useless on a large class of deployments.
//
// The IP intelligence providers are only constructed inside `if redisClient !=
// nil` (server/evr_pipeline.go), and redisClient is nil unless REDIS_URI is set.
// A deployment without Redis therefore has *no* providers: IPInfoCache.Get
// returns (nil, nil) for every public IP, permanently — not transiently.
// shouldWarnVPNDegraded is then true for every player in every BlockVPNUsers
// guild on every lobby authorize, forever, so an alert wired to
// lobby_vpn_check_degraded fires forever and carries zero information. That is
// the same "buries its own signal" failure SEC-6(b) exists to fix, relocated
// from the log into the metric.
//
// The fix is to tell the two apart. The standing configuration gap is still
// counted and still logged — it is a real security gap — but on its own reason
// tag and its own much longer log window, so it can neither be mistaken for an
// outage nor drown one out.
func TestVPNDegradedReasonFor_DistinguishesAStandingGapFromAnOutage(t *testing.T) {
	configured, err := NewIPInfoCache(nil, nil, &erroringIPInfoProvider{name: "IPQS", info: &StubIPInfo{}})
	require.NoError(t, err)
	require.Equal(t, vpnDegradedLookupFailed, vpnDegradedReasonFor(configured),
		"with a provider wired up, an empty result is a transient lookup failure — the alertable case")

	unconfigured, err := NewIPInfoCache(nil, nil)
	require.NoError(t, err)
	require.Equal(t, vpnDegradedNotConfigured, vpnDegradedReasonFor(unconfigured),
		"with no provider at all the gate can never evaluate; this is a standing config gap, not an outage")

	require.Equal(t, vpnDegradedNotConfigured, vpnDegradedReasonFor(nil),
		"a nil cache is the same standing gap, and classifying it must not panic")
}

func TestIPInfoCache_IsConfigured(t *testing.T) {
	none, err := NewIPInfoCache(nil, nil)
	require.NoError(t, err)
	require.False(t, none.IsConfigured(),
		"no REDIS_URI means no providers are ever appended (server/evr_pipeline.go)")

	some, err := NewIPInfoCache(nil, nil, &erroringIPInfoProvider{name: "IPQS", info: &StubIPInfo{}})
	require.NoError(t, err)
	require.True(t, some.IsConfigured())

	var nilCache *IPInfoCache
	require.False(t, nilCache.IsConfigured(), "must be nil-safe: callers classify before dereferencing")
}

func TestWarnVPNDegraded_CounterCarriesReasonSoAlertsCanSelectTheTransientCase(t *testing.T) {
	resetVPNDegradedThrottle(t)

	core, _ := observer.New(zapcore.DebugLevel)
	metrics := newRecordingMetrics()

	warnVPNDegraded(zap.New(core), metrics.CustomCounter, "203.0.113.7", "1", "987654321098765432", false, vpnDegradedNotConfigured)

	tags := metrics.tagsFor("lobby_vpn_check_degraded")
	require.NotNil(t, tags)
	require.Equal(t, "not_configured", tags["reason"],
		"a permanently-firing counter must be distinguishable from a real outage, or the alert is noise")

	warnVPNDegraded(zap.New(core), metrics.CustomCounter, "203.0.113.7", "1", "987654321098765432", false, vpnDegradedLookupFailed)
	require.Equal(t, "lookup_failed", metrics.tagsFor("lobby_vpn_check_degraded")["reason"],
		"alert on reason=lookup_failed to page only on the transient case")
}

// The standing gap must not consume the outage warning's throttle budget, and
// must not be reported in the outage's words: an operator grepping for the IPQS
// outage message must not find a deployment that simply has no provider.
func TestWarnVPNDegraded_UnconfiguredGapDoesNotImpersonateOrCrowdOutAnOutage(t *testing.T) {
	resetVPNDegradedThrottle(t)

	now := time.Now()
	vpnDegradedLogThrottle.setClock(func() time.Time { return now })
	vpnUnconfiguredLogThrottle.setClock(func() time.Time { return now })

	core, logs := observer.New(zapcore.DebugLevel)
	logger := zap.New(core)
	metrics := newRecordingMetrics()

	const guild = "111111111111111111"

	// A whole hour of authorizes on a deployment with no provider configured.
	for i := 0; i < 60; i++ {
		warnVPNDegraded(logger, metrics.CustomCounter, "203.0.113.7", "1", guild, false, vpnDegradedNotConfigured)
		now = now.Add(time.Minute)
	}

	outage := logs.FilterMessage("VPN blocking degraded: IPQS lookup unavailable for VPN check").All()
	require.Empty(t, outage,
		"a deployment with no provider is not an IPQS outage and must not be logged as one")

	gap := logs.FilterMessage("VPN blocking inert: no IP intelligence provider is configured").All()
	require.Len(t, gap, 1,
		"the standing gap holds until the process is restarted with a provider configured, so it gets a "+
			"long window — 60 lines per guild per hour, forever, is the log spam SEC-6(b) exists to prevent")
	require.Equal(t, zapcore.WarnLevel, gap[0].Level)
	require.Equal(t, "not_configured", gap[0].ContextMap()["reason"])

	require.Equal(t, int64(60), metrics.count("lobby_vpn_check_degraded"),
		"the counter still carries the true volume; only the prose is throttled")

	// The outage warning's budget is untouched: a real IPQS failure in the same
	// guild still gets its line immediately.
	warnVPNDegraded(logger, metrics.CustomCounter, "203.0.113.7", "1", guild, false, vpnDegradedLookupFailed)
	require.Len(t, logs.FilterMessage("VPN blocking degraded: IPQS lookup unavailable for VPN check").All(), 1,
		"the two conditions must throttle independently, or a standing gap silences a real outage")
}

// (c) The `errored` map in IPInfoCache.Get was populated and never read, so Get
// could never return a non-nil error. Provider failures must be observable.
type erroringIPInfoProvider struct {
	name string
	err  error
	info IPInfo
}

func (p *erroringIPInfoProvider) Name() string { return p.name }
func (p *erroringIPInfoProvider) Get(_ context.Context, _ string) (IPInfo, error) {
	return p.info, p.err
}

func TestIPInfoCache_ProviderErrorsAreCounted(t *testing.T) {
	metrics := newRecordingMetrics()
	core, logs := observer.New(zapcore.DebugLevel)

	cache, err := NewIPInfoCache(zap.New(core), metrics,
		&erroringIPInfoProvider{name: "IPQS", err: errors.New("quota exhausted")},
	)
	require.NoError(t, err)

	info, err := cache.Get(context.Background(), "203.0.113.7")

	require.NoError(t, err, "the cache deliberately fails open — the error channel stays nil")
	require.Nil(t, info)
	require.Equal(t, int64(1), metrics.count("ip_info_provider_error"),
		"SEC-6(c): a provider error must be counted, not swallowed into a map nobody reads")
	assert.Equal(t, "IPQS", metrics.tagsFor("ip_info_provider_error")["provider"])
	assert.Positive(t, logs.Len(), "a provider error should leave a trace in the log too")
}

func TestIPInfoCache_SuccessfulProviderIsNotCountedAsAnError(t *testing.T) {
	metrics := newRecordingMetrics()
	core, _ := observer.New(zapcore.DebugLevel)

	cache, err := NewIPInfoCache(zap.New(core), metrics,
		&erroringIPInfoProvider{name: "broken", err: errors.New("boom")},
		&erroringIPInfoProvider{name: "working", info: &StubIPInfo{}},
	)
	require.NoError(t, err)

	info, err := cache.Get(context.Background(), "203.0.113.7")
	require.NoError(t, err)
	require.NotNil(t, info, "a later provider that succeeds must still be used")
	require.Equal(t, int64(1), metrics.count("ip_info_provider_error"),
		"only the failing provider is counted")
}

// Review follow-up. Counting the provider error introduced the first
// dereference of s.metrics and s.logger on this path; NewIPInfoCache validates
// neither and existing callers pass nil for both (server/evr_bugfix_test.go:62,
// :88). Observability must never be the thing that panics a login path, so the
// error branch has to survive a cache built with no logger and no metrics.
func TestIPInfoCache_ProviderErrorWithNilLoggerAndMetricsDoesNotPanic(t *testing.T) {
	cache, err := NewIPInfoCache(nil, nil,
		&erroringIPInfoProvider{name: "IPQS", err: errors.New("quota exhausted")},
	)
	require.NoError(t, err)

	require.NotPanics(t, func() {
		info, getErr := cache.Get(context.Background(), "203.0.113.7")
		require.NoError(t, getErr)
		require.Nil(t, info, "the fail-open result is unchanged when there is nowhere to report to")
	})

	require.NotPanics(t, func() {
		require.False(t, cache.IsVPN("203.0.113.7"),
			"IsVPN still fails open when the provider errors and there is no logger")
	})
}
