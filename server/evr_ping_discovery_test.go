package server

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBestAddress_BothReachable_PrefersInternal(t *testing.T) {
	h := NewLatencyHistory()
	h.Add(net.ParseIP("1.2.3.4"), 35, 25, time.Time{})   // external: 35ms
	h.Add(net.ParseIP("192.168.1.5"), 2, 25, time.Time{}) // internal: 2ms

	ip, rtt, ok := h.BestAddress("1.2.3.4", "192.168.1.5")
	require.True(t, ok)
	assert.Equal(t, "192.168.1.5", ip)
	assert.Equal(t, 2, rtt)
}

func TestBestAddress_BothReachable_PrefersLower(t *testing.T) {
	h := NewLatencyHistory()
	h.Add(net.ParseIP("1.2.3.4"), 10, 25, time.Time{})    // external: 10ms
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
