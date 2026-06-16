package server

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLatencyHistory_Add_And_LatestRTT(t *testing.T) {
	h := NewLatencyHistory()
	ip := net.ParseIP("1.2.3.4")
	h.Add(ip, 35, 25, time.Time{})

	got := h.LatestRTT(ip)
	assert.Equal(t, 35, got)
}

func TestLatencyHistory_BestAddress(t *testing.T) {
	// Tests are in evr_ping_discovery_test.go — this file tests the base
	// LatencyHistory methods. BestAddress tests are grouped with discovery.
}

func TestLatencyHistory_HasRecentEntry_Boundary(t *testing.T) {
	h := NewLatencyHistory()
	h.Add(net.ParseIP("10.0.0.1"), 15, 25, time.Time{})

	// Entry was just added — it's after any past cutoff.
	require.True(t, h.HasRecentEntry("10.0.0.1", time.Now().Add(-1*time.Second)))

	// Future cutoff — entry is before it.
	require.False(t, h.HasRecentEntry("10.0.0.1", time.Now().Add(1*time.Hour)))
}
