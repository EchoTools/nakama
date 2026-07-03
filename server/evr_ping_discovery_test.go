package server

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
)

// newPingParamsCtx returns a context carrying SessionParameters whose latency
// history is lh, mirroring how a live session stores its params on the context.
func newPingParamsCtx(lh *LatencyHistory) context.Context {
	params := &SessionParameters{
		latencyHistory: atomic.NewPointer(lh),
	}
	ptr := atomic.NewPointer(params)
	return context.WithValue(context.Background(), ctxSessionParametersKey{}, ptr)
}

func TestBestAddress_BothReachable_PrefersInternal(t *testing.T) {
	h := NewLatencyHistory()
	h.Add(net.ParseIP("1.2.3.4"), 35, 25, time.Time{})    // external: 35ms
	h.Add(net.ParseIP("192.168.1.5"), 2, 25, time.Time{}) // internal: 2ms

	ip, rtt, ok := h.BestAddress("1.2.3.4", "192.168.1.5")
	require.True(t, ok)
	assert.Equal(t, "192.168.1.5", ip)
	assert.Equal(t, 2, rtt)
}

func TestBestAddress_BothReachable_PrefersLower(t *testing.T) {
	h := NewLatencyHistory()
	h.Add(net.ParseIP("1.2.3.4"), 10, 25, time.Time{})     // external: 10ms
	h.Add(net.ParseIP("192.168.1.5"), 50, 25, time.Time{}) // internal: 50ms (worse)

	ip, rtt, ok := h.BestAddress("1.2.3.4", "192.168.1.5")
	require.True(t, ok)
	assert.Equal(t, "1.2.3.4", ip)
	assert.Equal(t, 10, rtt)
}

func TestBestAddress_OnlyExternalReachable(t *testing.T) {
	h := NewLatencyHistory()
	h.Add(net.ParseIP("1.2.3.4"), 35, 25, time.Time{}) // external only

	ip, rtt, ok := h.BestAddress("1.2.3.4", "192.168.1.5")
	require.True(t, ok)
	assert.Equal(t, "1.2.3.4", ip)
	assert.Equal(t, 35, rtt)
}

func TestBestAddress_NeitherReachable(t *testing.T) {
	h := NewLatencyHistory()

	_, _, ok := h.BestAddress("1.2.3.4", "192.168.1.5")
	assert.False(t, ok)
}

func TestBestAddress_NoInternalIP(t *testing.T) {
	h := NewLatencyHistory()
	h.Add(net.ParseIP("1.2.3.4"), 35, 25, time.Time{})

	ip, rtt, ok := h.BestAddress("1.2.3.4", "")
	require.True(t, ok)
	assert.Equal(t, "1.2.3.4", ip)
	assert.Equal(t, 35, rtt)
}

func TestBestAddress_EqualRTT_PrefersInternal(t *testing.T) {
	h := NewLatencyHistory()
	h.Add(net.ParseIP("1.2.3.4"), 20, 25, time.Time{})
	h.Add(net.ParseIP("192.168.1.5"), 20, 25, time.Time{})

	ip, _, ok := h.BestAddress("1.2.3.4", "192.168.1.5")
	require.True(t, ok)
	// When equal, internal is preferred (intRTT <= extRTT)
	assert.Equal(t, "192.168.1.5", ip)
}

func TestHasRecentEntry(t *testing.T) {
	h := NewLatencyHistory()
	h.Add(net.ParseIP("1.2.3.4"), 35, 25, time.Time{})

	assert.True(t, h.HasRecentEntry("1.2.3.4", time.Now().Add(-1*time.Minute)))
	assert.False(t, h.HasRecentEntry("1.2.3.4", time.Now().Add(1*time.Minute)))
	assert.False(t, h.HasRecentEntry("5.6.7.8", time.Now().Add(-1*time.Minute)))
}

func TestChunkPingTargets(t *testing.T) {
	targets := make([]PingTarget, 35)
	for i := range targets {
		targets[i] = PingTarget{
			Address:   net.ParseIP("1.2.3.4"),
			Port:      uint16(6792 + i),
			ServerKey: "1.2.3.4",
		}
	}

	batches := chunkPingTargets(targets, 16)
	assert.Len(t, batches, 3)
	assert.Len(t, batches[0], 16)
	assert.Len(t, batches[1], 16)
	assert.Len(t, batches[2], 3)
}

func TestChunkPingTargets_ExactMultiple(t *testing.T) {
	targets := make([]PingTarget, 32)
	for i := range targets {
		targets[i] = PingTarget{Address: net.ParseIP("1.2.3.4"), Port: uint16(i)}
	}

	batches := chunkPingTargets(targets, 16)
	assert.Len(t, batches, 2)
	assert.Len(t, batches[0], 16)
	assert.Len(t, batches[1], 16)
}

func TestChunkPingTargets_Empty(t *testing.T) {
	batches := chunkPingTargets(nil, 16)
	assert.Nil(t, batches)
}

func TestLoadPingDiscoveryConfig_Defaults(t *testing.T) {
	cfg := LoadPingDiscoveryConfig(map[string]string{})
	assert.Equal(t, 8, cfg.MaxMessages)
	assert.Equal(t, 60, cfg.SpreadSeconds)
}

func TestLoadPingDiscoveryConfig_Override(t *testing.T) {
	cfg := LoadPingDiscoveryConfig(map[string]string{
		"PING_DISCOVERY_MAX_MESSAGES":   "12",
		"PING_DISCOVERY_SPREAD_SECONDS": "30",
	})
	assert.Equal(t, 12, cfg.MaxMessages)
	assert.Equal(t, 30, cfg.SpreadSeconds)
}

func TestLoadPingDiscoveryConfig_InvalidFallsBackToDefault(t *testing.T) {
	cfg := LoadPingDiscoveryConfig(map[string]string{
		"PING_DISCOVERY_MAX_MESSAGES":   "not_a_number",
		"PING_DISCOVERY_SPREAD_SECONDS": "-5",
	})
	assert.Equal(t, 8, cfg.MaxMessages)
	assert.Equal(t, 60, cfg.SpreadSeconds)
}

func TestBuildJoinEndpoint_NoParams_ReturnsUnchanged(t *testing.T) {
	server := evr.Endpoint{
		InternalIP: net.ParseIP("192.168.1.5"),
		ExternalIP: net.ParseIP("1.2.3.4"),
		Port:       6792,
	}
	// Context with no session params — must return the endpoint as-is.
	got := buildJoinEndpoint(context.Background(), server)
	assert.Equal(t, server, got)
}

func TestBuildJoinEndpoint_NoInternalIP_ReturnsUnchanged(t *testing.T) {
	server := evr.Endpoint{
		InternalIP: nil,
		ExternalIP: net.ParseIP("1.2.3.4"),
		Port:       6792,
	}
	ctx := newPingParamsCtx(NewLatencyHistory())

	got := buildJoinEndpoint(ctx, server)
	assert.Equal(t, server, got)
}

func TestBuildJoinEndpoint_UnspecifiedInternalIP_ReturnsUnchanged(t *testing.T) {
	server := evr.Endpoint{
		InternalIP: net.IPv4zero,
		ExternalIP: net.ParseIP("1.2.3.4"),
		Port:       6792,
	}
	ctx := newPingParamsCtx(NewLatencyHistory())

	got := buildJoinEndpoint(ctx, server)
	assert.Equal(t, server, got)
}

func TestBuildJoinEndpoint_InternalReachable_IncludesInternal(t *testing.T) {
	intIP := net.ParseIP("192.168.1.5")
	extIP := net.ParseIP("1.2.3.4")
	server := evr.Endpoint{InternalIP: intIP, ExternalIP: extIP, Port: 6792}

	lh := NewLatencyHistory()
	lh.Add(extIP, 35, 25, time.Time{}) // external reachable
	lh.Add(intIP, 2, 25, time.Time{})  // internal reachable (client is on LAN)
	ctx := newPingParamsCtx(lh)

	got := buildJoinEndpoint(ctx, server)
	assert.Equal(t, intIP, got.InternalIP, "internal IP should be included when client reached it")
	assert.Equal(t, extIP, got.ExternalIP)
	assert.Equal(t, uint16(6792), got.Port)
}

func TestBuildJoinEndpoint_InternalUnreachable_ExternalOnly(t *testing.T) {
	intIP := net.ParseIP("192.168.1.5")
	extIP := net.ParseIP("1.2.3.4")
	server := evr.Endpoint{InternalIP: intIP, ExternalIP: extIP, Port: 6792}

	lh := NewLatencyHistory()
	lh.Add(extIP, 35, 25, time.Time{}) // only external reachable (remote client)
	ctx := newPingParamsCtx(lh)

	got := buildJoinEndpoint(ctx, server)
	assert.Nil(t, got.InternalIP, "internal IP must be omitted when client could not reach it")
	assert.Equal(t, extIP, got.ExternalIP)
	assert.Equal(t, uint16(6792), got.Port)
}

func TestBuildJoinEndpoint_InternalReachableButSlower_ExternalOnly(t *testing.T) {
	intIP := net.ParseIP("192.168.1.5")
	extIP := net.ParseIP("1.2.3.4")
	server := evr.Endpoint{InternalIP: intIP, ExternalIP: extIP, Port: 6792}

	lh := NewLatencyHistory()
	lh.Add(extIP, 20, 25, time.Time{}) // external is the faster path
	lh.Add(intIP, 40, 25, time.Time{}) // internal reachable but slower
	ctx := newPingParamsCtx(lh)

	// Optimal routing: the external path is best, so the internal IP is omitted.
	got := buildJoinEndpoint(ctx, server)
	assert.Nil(t, got.InternalIP, "internal must be omitted when the external path is faster")
	assert.Equal(t, extIP, got.ExternalIP)
}

func TestBuildJoinEndpoint_NeitherReachable_ExternalOnly(t *testing.T) {
	intIP := net.ParseIP("192.168.1.5")
	extIP := net.ParseIP("1.2.3.4")
	server := evr.Endpoint{InternalIP: intIP, ExternalIP: extIP, Port: 6792}

	// Empty history — player queued before discovery finished.
	ctx := newPingParamsCtx(NewLatencyHistory())

	got := buildJoinEndpoint(ctx, server)
	assert.Nil(t, got.InternalIP, "safe default is external-only when no latency data exists")
	assert.Equal(t, extIP, got.ExternalIP)
}
