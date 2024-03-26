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
	config := NewConfig(logger)

	matchmaker, _, _ := createTestMatchmaker(t, logger, false, nil)
	r := NewMatchmakingRegistry(logger, matchRegistry, matchmaker, &testMetrics{}, config)

	// Create a user ID and endpoints for testing
	userId := uuid.Must(uuid.NewV4())
	endpoints := []evr.Endpoint{
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
		cache.Store[endpoint.ID()] = EndpointWithRTT{
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
		cache.Store[endpoints[0].ID()] = EndpointWithRTT{
			Endpoint:  endpoints[0],
			RTT:       rttUnit,
			Timestamp: time.Now().Add(-2 * latencyCacheExpiry),
		}

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
		cache.Store[endpoints[0].ID()] = EndpointWithRTT{
			Endpoint:  endpoints[0],
			RTT:       rttUnit,
			Timestamp: time.Now().Add(-2 * latencyCacheExpiry),
		}
		cache.Store[endpoints[2].ID()] = EndpointWithRTT{
			Endpoint:  endpoints[2],
			RTT:       rttUnit,
			Timestamp: time.Now(),
		}

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

		endpoints := make([]evr.Endpoint, 20)
		for i := 1; i <= 20; i++ {
			endpoints[i-1] = evr.Endpoint{
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
func TestMatchmakingRegistry_UpdateBroadcasters(t *testing.T) {

	logger, _ := zap.NewProduction()
	defer logger.Sync() // flushes buffer, if any

	matchRegistry, _, err := createTestMatchRegistry(t, logger)
	if err != nil {
		t.Fatalf("error creating test match registry: %v", err)
	}

	matchmaker, _, _ := createTestMatchmaker(t, logger, false, nil)
	r := NewMatchmakingRegistry(logger, matchRegistry, matchmaker, &testMetrics{}, NewConfig(logger))

	// Create test endpoints
	endpoints := []evr.Endpoint{
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
	}

	// Test case 1: Update broadcasters with new endpoints
	t.Run("UpdateBroadcastersWithNewEndpoints", func(t *testing.T) {
		r.UpdateBroadcasters(endpoints)

		// Verify that the broadcasters have been updated correctly
		r.Lock()
		defer r.Unlock()
		assert.Equal(t, 2, len(r.broadcasters), "Expected 2 broadcasters")
		assert.Equal(t, endpoints[0], r.broadcasters[endpoints[0].ID()], "Expected first broadcaster to be updated")
		assert.Equal(t, endpoints[1], r.broadcasters[endpoints[1].ID()], "Expected second broadcaster to be updated")
	})

	// Test case 2: Update broadcasters with existing endpoints
	t.Run("UpdateBroadcastersWithExistingEndpoints", func(t *testing.T) {
		// Add additional endpoints
		existingEndpoints := []evr.Endpoint{
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
		allEndpoints := append(endpoints, existingEndpoints...)
		r.UpdateBroadcasters(allEndpoints)

		// Verify that the broadcasters have been updated correctly
		r.Lock()
		defer r.Unlock()
		assert.Equal(t, 4, len(r.broadcasters), "Expected 4 broadcasters")
		assert.Equal(t, allEndpoints[0], r.broadcasters[allEndpoints[0].ID()], "Expected first broadcaster to be updated")
		assert.Equal(t, allEndpoints[1], r.broadcasters[allEndpoints[1].ID()], "Expected second broadcaster to be updated")
		assert.Equal(t, existingEndpoints[0], r.broadcasters[existingEndpoints[0].ID()], "Expected third broadcaster to be updated")
		assert.Equal(t, existingEndpoints[1], r.broadcasters[existingEndpoints[1].ID()], "Expected fourth broadcaster to be updated")
	})
}

func Test_ipToKey(t *testing.T) {
	type args struct {
		ip net.IP
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "IPv4",
			args: args{
				ip: net.ParseIP("192.168.1.1"),
			},
			want: "rttc0a80101",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ipToKey(tt.args.ip); got != tt.want {
				t.Errorf("ipToKey() = %v, want %v", got, tt.want)
			}
		})
	}
}
