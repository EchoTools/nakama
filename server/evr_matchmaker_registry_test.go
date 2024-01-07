package server

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestMatchmakingRegistry_GetPingCandidates(t *testing.T) {
	latencyCacheExpiry := 10 * time.Minute

	logger, _ := zap.NewProduction()
	defer logger.Sync() // flushes buffer, if any

	matchRegistry, _, err := createTestMatchRegistry(t, logger)
	if err != nil {
		t.Fatalf("error creating test match registry: %v", err)
	}

	// Create a MatchmakingRegistry instance
	config := &MatchmakingRegistryConfig{
		Logger:        logger,
		MatchRegistry: matchRegistry,
		Metrics:       &testMetrics{},
	}
	r := NewMatchmakingRegistry(config)

	// Create a user ID and endpoints for testing
	userId := uuid.Must(uuid.NewV4())
	endpoints := []*evr.Endpoint{
		{
			InternalIP: net.ParseIP("192.168.1.1"),
			Port:       1234,
			ExternalIP: net.ParseIP("1.1.1.1"),
		},
		{
			InternalIP: net.ParseIP("192.168.1.2"),
			Port:       1235,
			ExternalIP: net.ParseIP("1.1.1.2"),
		},
		{
			InternalIP: net.ParseIP("192.168.1.3"),
			Port:       1236,
			ExternalIP: net.ParseIP("1.1.1.3"),
		},
		{
			InternalIP: net.ParseIP("192.168.1.4"),
			Port:       1237,
			ExternalIP: net.ParseIP("1.1.1.4"),
		},
	}
	rttUnit := 30 * time.Second
	cache := r.GetCache(userId)
	cache.Lock()
	for i, endpoint := range endpoints {
		cache.Store[endpoint.ID()] = &EndpointWithLatency{
			Endpoint:  endpoint,
			RTT:       rttUnit * time.Duration(i+1),
			Timestamp: time.Now(),
		}
	}
	cache.Unlock()
	// Test case 1: No existing broadcasters
	t.Run("NoExistingBroadcasters", func(t *testing.T) {
		pingCandidates := r.GetPingCandidates(userId, endpoints)
		assert.Equal(t, 4, len(pingCandidates), "Expected no ping candidates")
	})

	// Test case 2: Existing broadcasters with no expired entries
	t.Run("ExistingBroadcastersNoExpiredEntries", func(t *testing.T) {
		pingCandidates := r.GetPingCandidates(userId, endpoints)
		assert.Equal(t, 4, len(pingCandidates), "Expected no ping candidates")
	})

	// Test case 3: Existing broadcasters with expired entries
	t.Run("ExistingBroadcastersWithExpiredEntries", func(t *testing.T) {
		// Add some existing broadcasters with expired entries
		r.UpdateBroadcasters(endpoints[:2])
		cache := r.GetCache(userId)
		cache.Lock()
		cache.Store[endpoints[0].ID()].Timestamp = time.Now().Add(-2 * latencyCacheExpiry)
		cache.Unlock()

		pingCandidates := r.GetPingCandidates(userId, endpoints)
		assert.Equal(t, 4, len(pingCandidates), "Expected 1 ping candidate")
		assert.Equal(t, "192.168.1.1:1.1.1.1", pingCandidates[0].ID(), "Expected ping candidate to be 'endpoint1'")
	})

	// Test case 4: Existing broadcasters with both expired and non-expired entries
	t.Run("ExistingBroadcastersWithMixedEntries", func(t *testing.T) {
		// Add some existing broadcasters with mixed entries
		r.UpdateBroadcasters(endpoints[:2])
		cache := r.GetCache(userId)
		cache.Lock()
		cache.Store[endpoints[0].ID()].Timestamp = time.Now().Add(-2 * latencyCacheExpiry)
		cache.Store[endpoints[2].ID()].Timestamp = time.Now().Add(-3 * latencyCacheExpiry)
		cache.Unlock()

		pingCandidates := r.GetPingCandidates(userId, endpoints)
		assert.Equal(t, 4, len(pingCandidates), "Expected 2 ping candidates")
		assert.Equal(t, "192.168.1.3:1.1.1.3", pingCandidates[0].ID(), "Expected first ping candidate to be '192.168.1.3:1.1.1.3'")
		assert.Equal(t, "192.168.1.1:1.1.1.1", pingCandidates[1].ID(), "Expected second ping candidate to be '192.168.1.1:1.1.1.1'")
	})

	// Test case 5: More than 16 ping candidates
	t.Run("MoreThan16PingCandidates", func(t *testing.T) {
		// Add existing broadcasters with more than 16 entries
		r.UpdateBroadcasters(endpoints[:2])
		cache := r.GetCache(userId)
		cache.Lock()

		endpoints := make([]*evr.Endpoint, 20)
		for i := 1; i <= 20; i++ {
			endpoints[i-1] = &evr.Endpoint{
				InternalIP: net.ParseIP(fmt.Sprintf("192.168.1.%d", i)),
				Port:       1234,
				ExternalIP: net.ParseIP(fmt.Sprintf("1.1.1.%d", i)),
			}
		}
		cache.Unlock()

		pingCandidates := r.GetPingCandidates(userId, endpoints)
		assert.Equal(t, 16, len(pingCandidates), "Expected 16 ping candidates")
	})
}
