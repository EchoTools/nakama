package server

import (
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSortPingCandidatesByLatencyHistory(t *testing.T) {
	rand.Seed(time.Now().UnixNano())

	tests := []struct {
		name           string
		hostIPs        []string
		latencyHistory map[string]map[int64]int
		expectedOrder  []string
	}{
		{
			name:           "No latency history",
			hostIPs:        []string{"192.168.1.1", "192.168.1.2", "192.168.1.3"},
			latencyHistory: map[string]map[int64]int{},
			expectedOrder:  []string{"192.168.1.1", "192.168.1.2", "192.168.1.3"},
		},
		{
			name:    "Some latency history",
			hostIPs: []string{"192.168.1.1", "192.168.1.2", "192.168.1.3"},
			latencyHistory: map[string]map[int64]int{
				"192.168.1.1": {1: 100},
				"192.168.1.2": {2: 200},
			},
			expectedOrder: []string{"192.168.1.1", "192.168.1.2", "192.168.1.3"},
		},
		{
			name:    "Full latency history",
			hostIPs: []string{"192.168.1.1", "192.168.1.2", "192.168.1.3"},
			latencyHistory: map[string]map[int64]int{
				"192.168.1.1": {3: 300},
				"192.168.1.2": {1: 100},
				"192.168.1.3": {2: 200},
			},
			expectedOrder: []string{"192.168.1.2", "192.168.1.3", "192.168.1.1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sortPingCandidatesByLatencyHistory(tt.hostIPs, tt.latencyHistory)
			assert.Equal(t, tt.expectedOrder, tt.hostIPs)
		})
	}
}
