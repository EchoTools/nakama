package server

import (
	"net"
	"testing"
	"time"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

// TestPingEndpoint_IncludesBothWhenInternalPrivate covers ADR 0002 req 3: the
// pre-connect ping carries BOTH the external and (private) internal address in a
// single endpoint so at least one returns a valid RTT.
func TestPingEndpoint_IncludesBothWhenInternalPrivate(t *testing.T) {
	got := pingEndpoint(evr.Endpoint{
		InternalIP: net.ParseIP("192.168.1.5"),
		ExternalIP: net.ParseIP("1.2.3.4"),
		Port:       6792,
	})
	assert.Equal(t, "192.168.1.5", got.InternalIP.String())
	assert.Equal(t, "1.2.3.4", got.GetExternalIP())
	assert.Equal(t, uint16(6792), got.Port)
}

// TestPingEndpoint_ExternalOnlyWhenInternalPublic covers ADR 0002 req 2: a
// publicly-routable address must never occupy the internal slot; it is zeroed,
// leaving external-only.
func TestPingEndpoint_ExternalOnlyWhenInternalPublic(t *testing.T) {
	got := pingEndpoint(evr.Endpoint{
		InternalIP: net.ParseIP("8.8.8.8"),
		ExternalIP: net.ParseIP("1.2.3.4"),
		Port:       6792,
	})
	assert.Nil(t, got.InternalIP)
	assert.Equal(t, "1.2.3.4", got.GetExternalIP())
}

func TestPingEndpoint_ExternalOnlyWhenNoInternal(t *testing.T) {
	got := pingEndpoint(evr.Endpoint{
		ExternalIP: net.ParseIP("1.2.3.4"),
		Port:       6792,
	})
	assert.Nil(t, got.InternalIP)
	assert.Equal(t, "1.2.3.4", got.GetExternalIP())
}

func TestChunkEndpoints(t *testing.T) {
	eps := make([]evr.Endpoint, 35)
	for i := range eps {
		eps[i] = evr.Endpoint{ExternalIP: net.ParseIP("1.2.3.4"), Port: uint16(6792 + i)}
	}

	batches := chunkEndpoints(eps, 16)
	assert.Len(t, batches, 3)
	assert.Len(t, batches[0], 16)
	assert.Len(t, batches[1], 16)
	assert.Len(t, batches[2], 3)
}

func TestChunkEndpoints_ExactMultiple(t *testing.T) {
	eps := make([]evr.Endpoint, 32)
	for i := range eps {
		eps[i] = evr.Endpoint{ExternalIP: net.ParseIP("1.2.3.4"), Port: uint16(i)}
	}

	batches := chunkEndpoints(eps, 16)
	assert.Len(t, batches, 2)
	assert.Len(t, batches[0], 16)
	assert.Len(t, batches[1], 16)
}

func TestChunkEndpoints_Empty(t *testing.T) {
	assert.Nil(t, chunkEndpoints(nil, 16))
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

// TestBuildJoinEndpoint_PrivateInternal_IncludesBoth covers ADR 0002 req 3: the
// join endpoint hands the client BOTH addresses (external + private internal) so
// the client validates and picks whichever it can reach — at least one is valid.
func TestBuildJoinEndpoint_PrivateInternal_IncludesBoth(t *testing.T) {
	server := evr.Endpoint{
		InternalIP: net.ParseIP("192.168.1.5"),
		ExternalIP: net.ParseIP("1.2.3.4"),
		Port:       6792,
	}
	got := buildJoinEndpoint(server)
	assert.Equal(t, "192.168.1.5", got.InternalIP.String(), "private internal IP is handed to the client")
	assert.Equal(t, "1.2.3.4", got.GetExternalIP())
	assert.Equal(t, uint16(6792), got.Port)
}

// TestBuildJoinEndpoint_PublicInternal_ExternalOnly is defense-in-depth for ADR
// 0002 req 2: even if a publicly-routable internal slot slipped past
// registration, it is never handed to the client.
func TestBuildJoinEndpoint_PublicInternal_ExternalOnly(t *testing.T) {
	server := evr.Endpoint{
		InternalIP: net.ParseIP("8.8.8.8"),
		ExternalIP: net.ParseIP("1.2.3.4"),
		Port:       6792,
	}
	got := buildJoinEndpoint(server)
	assert.Nil(t, got.InternalIP, "public internal IP must be stripped")
	assert.Equal(t, "1.2.3.4", got.GetExternalIP())
}

func TestBuildJoinEndpoint_NoInternal_ExternalOnly(t *testing.T) {
	server := evr.Endpoint{
		ExternalIP: net.ParseIP("1.2.3.4"),
		Port:       6792,
	}
	got := buildJoinEndpoint(server)
	assert.Nil(t, got.InternalIP)
	assert.Equal(t, "1.2.3.4", got.GetExternalIP())
}

func TestBuildJoinEndpoint_UnspecifiedInternal_ExternalOnly(t *testing.T) {
	server := evr.Endpoint{
		InternalIP: net.IPv4zero,
		ExternalIP: net.ParseIP("1.2.3.4"),
		Port:       6792,
	}
	got := buildJoinEndpoint(server)
	assert.Nil(t, got.InternalIP)
	assert.Equal(t, "1.2.3.4", got.GetExternalIP())
}
