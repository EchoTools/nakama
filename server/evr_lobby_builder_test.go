package server

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
)

func TestSortGameServerIPs(t *testing.T) {
	tests := []struct {
		name     string
		entrants []*MatchmakerEntry
		expected []string
	}{
		{
			name: "Single entrant with one latency",
			entrants: []*MatchmakerEntry{
				{
					NumericProperties: map[string]float64{
						"rtt1": 100,
					},
				},
			},
			expected: []string{"rtt1"},
		},
		{
			name: "Multiple entrants with different latencies",
			entrants: []*MatchmakerEntry{
				{
					NumericProperties: map[string]float64{
						"rtt1": 100,
						"rtt2": 200,
					},
				},
				{
					NumericProperties: map[string]float64{
						"rtt1": 150,
						"rtt2": 250,
					},
				},
			},
			expected: []string{"rtt1", "rtt2"},
		},
		{
			name: "Multiple entrants with same latencies",
			entrants: []*MatchmakerEntry{
				{
					NumericProperties: map[string]float64{
						"rtt1": 100,
					},
				},
				{
					NumericProperties: map[string]float64{
						"rtt1": 100,
					},
				},
			},
			expected: []string{"rtt1"},
		},
		{
			name: "No latencies",
			entrants: []*MatchmakerEntry{
				{
					NumericProperties: map[string]float64{},
				},
			},
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lb := &LobbyBuilder{}
			result := lb.rankEndpointsByServerScore(tt.entrants)
			assert.Equal(t, tt.expected, result)
		})
	}
}
func TestGroupByTicket(t *testing.T) {
	tests := []struct {
		name     string
		entrants []*MatchmakerEntry
		expected [][]*MatchmakerEntry
	}{
		{
			name: "Single entrant with one ticket",
			entrants: []*MatchmakerEntry{
				{
					Ticket: "ticket1",
				},
			},
			expected: [][]*MatchmakerEntry{
				{
					{
						Ticket: "ticket1",
					},
				},
			},
		},
		{
			name: "Multiple entrants with different tickets",
			entrants: []*MatchmakerEntry{
				{
					Ticket: "ticket1",
				},
				{
					Ticket: "ticket2",
				},
			},
			expected: [][]*MatchmakerEntry{
				{
					{
						Ticket: "ticket1",
					},
				},
				{
					{
						Ticket: "ticket2",
					},
				},
			},
		},
		{
			name: "Multiple entrants with same ticket",
			entrants: []*MatchmakerEntry{
				{
					Ticket: "ticket1",
				},
				{
					Ticket: "ticket1",
				},
			},
			expected: [][]*MatchmakerEntry{
				{
					{
						Ticket: "ticket1",
					},
					{
						Ticket: "ticket1",
					},
				},
			},
		},
		{
			name: "No tickets",
			entrants: []*MatchmakerEntry{
				{
					Ticket: "",
				},
			},
			expected: [][]*MatchmakerEntry{
				{
					{
						Ticket: "",
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lb := &LobbyBuilder{}
			result := lb.groupByTicket(tt.entrants)
			assert.Equal(t, tt.expected, result)
		})
	}
}
func TestSortLabelIndexes(t *testing.T) {
	tests := []struct {
		name     string
		labels   []labelIndex
		expected []labelIndex
	}{
		{
			name: "Sort by regionMatches",
			labels: []labelIndex{
				{IsRegionMatch: false},
				{IsRegionMatch: true},
			},
			expected: []labelIndex{
				{IsRegionMatch: true},
				{IsRegionMatch: false},
			},
		},
		{
			name: "Sort by rtt",
			labels: []labelIndex{
				{RTT: 100},
				{RTT: 30},
			},
			expected: []labelIndex{
				{RTT: 30},
				{RTT: 100},
			},
		},
		{
			name: "Sort by rating",
			labels: []labelIndex{
				{Rating: 50},
				{Rating: 100},
				{Rating: 25},
				{Rating: 75},
			},
			expected: []labelIndex{
				{Rating: 100},
				{Rating: 75},
				{Rating: 50},
				{Rating: 25},
			},
		},
		{
			name: "Sort by isPriorityForMode",
			labels: []labelIndex{
				{IsPriorityForMode: false},
				{IsPriorityForMode: true},
			},
			expected: []labelIndex{
				{IsPriorityForMode: true},
				{IsPriorityForMode: false},
			},
		},
		{
			name: "Sort by isHighLatency",
			labels: []labelIndex{
				{IsHighLatency: false},
				{IsHighLatency: true},
				{IsHighLatency: false},
				{IsHighLatency: true},
				{IsHighLatency: false},
			},
			expected: []labelIndex{
				{IsHighLatency: false},
				{IsHighLatency: false},
				{IsHighLatency: false},
				{IsHighLatency: true},
				{IsHighLatency: true},
			},
		},
		{
			name: "Sort by isReachable",
			labels: []labelIndex{
				{IsReachable: false},
				{IsReachable: true},
			},
			expected: []labelIndex{
				{IsReachable: true},
				{IsReachable: false},
			},
		},
		{
			name: "Sort by activeCount",
			labels: []labelIndex{
				{ActiveCount: 2},
				{ActiveCount: 1},
			},
			expected: []labelIndex{
				{ActiveCount: 1},
				{ActiveCount: 2},
			},
		},
		{
			name: "Sort by activeCount and rtt",
			labels: []labelIndex{
				{IsRegionMatch: true, RTT: 50, IsReachable: true, ActiveCount: 2},
				{IsRegionMatch: true, RTT: 40, IsReachable: true, ActiveCount: 3},
				{IsRegionMatch: true, RTT: 55, IsReachable: true, ActiveCount: 2},
				{IsRegionMatch: true, RTT: 70, IsReachable: true, ActiveCount: 1},
			},
			expected: []labelIndex{
				{IsRegionMatch: true, RTT: 70, IsReachable: true, ActiveCount: 1},
				{IsRegionMatch: true, RTT: 50, IsReachable: true, ActiveCount: 2},
				{IsRegionMatch: true, RTT: 55, IsReachable: true, ActiveCount: 2},
				{IsRegionMatch: true, RTT: 40, IsReachable: true, ActiveCount: 3},
			},
		},
		{
			name: "Complex sorting",
			labels: []labelIndex{
				{IsRegionMatch: true, RTT: 100, IsPriorityForMode: false, IsReachable: false, ActiveCount: 2},
				{IsRegionMatch: true, RTT: 50, IsPriorityForMode: true, IsReachable: true, ActiveCount: 1},
				{IsRegionMatch: true, RTT: 50, IsPriorityForMode: true, IsReachable: true, ActiveCount: 2},
				{IsRegionMatch: true, RTT: 100, IsPriorityForMode: false, IsReachable: false, ActiveCount: 1},
			},
			expected: []labelIndex{
				{IsRegionMatch: true, RTT: 50, IsPriorityForMode: true, IsReachable: true, ActiveCount: 1},
				{IsRegionMatch: true, RTT: 50, IsPriorityForMode: true, IsReachable: true, ActiveCount: 2},
				{IsRegionMatch: true, RTT: 100, IsPriorityForMode: false, IsReachable: false, ActiveCount: 1},
				{IsRegionMatch: true, RTT: 100, IsPriorityForMode: false, IsReachable: false, ActiveCount: 2},
			},
		},
		{
			name: "RTT difference less than 30",
			labels: []labelIndex{
				{RTT: 60},
				{RTT: 50},
			},
			expected: []labelIndex{
				{RTT: 60},
				{RTT: 50},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			labelsCopy := make([]labelIndex, len(tt.labels))
			copy(labelsCopy, tt.labels)

			sortLabelIndexes(labelsCopy)
			if diff := cmp.Diff(labelsCopy, tt.expected); diff != "" {
				t.Errorf("sortLabelIndexes() mismatch (-got +want):\n%s", diff)
			}
		})
	}
}

// TestSelectNextMapCombatRandomizes verifies that selectNextMap returns a valid
// combat level on the first call (i.e. the queue is populated rather than
// short-circuited to LevelUnspecified).
//
// Bug: selectNextMap returns evr.LevelUnspecified on the first call for any mode
// because the early-exit at line 648 triggers when mapQueue[mode] has not yet
// been written (key absent → ok=false), before the queue-refill block at line 654
// can run. The queue is never seeded, so every call returns LevelUnspecified.
func TestSelectNextMapCombatRandomizes(t *testing.T) {
	lb := &LobbyBuilder{
		mapQueue: make(map[evr.Symbol][]evr.Symbol),
	}

	validLevels := evr.LevelsByMode[evr.ModeCombatPublic]
	if len(validLevels) == 0 {
		t.Fatal("LevelsByMode[ModeCombatPublic] is empty — test precondition violated")
	}

	// First call: queue is empty (key not present). Should still return a valid level.
	level := lb.selectNextMap(evr.ModeCombatPublic)
	assert.NotEqual(t, evr.LevelUnspecified, level,
		"selectNextMap returned LevelUnspecified on first call; queue was never seeded")
	assert.Contains(t, validLevels, level,
		"selectNextMap returned a level not in LevelsByMode[ModeCombatPublic]")

	// Call enough times to exercise the full queue rotation. With 4 combat maps
	// the queue refills every 4 calls; run 20 iterations to cover multiple cycles.
	seen := map[evr.Symbol]struct{}{level: {}}
	for i := 1; i < 20; i++ {
		got := lb.selectNextMap(evr.ModeCombatPublic)
		assert.NotEqual(t, evr.LevelUnspecified, got,
			"selectNextMap returned LevelUnspecified on call %d", i+1)
		assert.Contains(t, validLevels, got,
			"selectNextMap returned an invalid level on call %d: %v", i+1, got)
		seen[got] = struct{}{}
	}

	// Regression: verify the function actually rotates through distinct levels.
	// With 4 combat maps and 20 calls, at least 2 distinct levels must appear.
	assert.Greater(t, len(seen), 1,
		"selectNextMap returned the same level on all 20 calls; queue rotation is broken")
}
