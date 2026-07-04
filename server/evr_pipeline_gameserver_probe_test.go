package server

import (
	"net"
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
)

// TestServerProbeTarget_ReturnsExternalNeverInternal covers ADR 0002 req 1: when
// a game server connects (registration / continuous health check), nakama probes
// ONLY the external IP. The internal IP may be unreachable from nakama and must
// never be a probe target.
func TestServerProbeTarget_ReturnsExternalNeverInternal(t *testing.T) {
	ep := evr.Endpoint{
		InternalIP: net.ParseIP("192.168.1.5"),
		ExternalIP: net.ParseIP("1.2.3.4"),
		Port:       6792,
	}
	got := serverProbeTarget(ep)
	assert.Equal(t, "1.2.3.4", got.String(), "nakama probes the external IP")
	assert.NotEqual(t, ep.InternalIP.String(), got.String(), "nakama must never probe the internal IP")
}

func TestServerProbeTarget_NoInternal(t *testing.T) {
	ep := evr.Endpoint{
		ExternalIP: net.ParseIP("1.2.3.4"),
		Port:       6792,
	}
	got := serverProbeTarget(ep)
	assert.Equal(t, "1.2.3.4", got.String())
}
