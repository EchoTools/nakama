package service

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/heroiclabs/nakama/v3/server"
	"github.com/stretchr/testify/assert"
)

func TestSortGameServerIPs(t *testing.T) {
	tests := []struct {
		name     string
		entrants []*server.MatchmakerEntry
		expected []string
	}{
		{
			name: "Single entrant with one latency",
			entrants: []*server.MatchmakerEntry{
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
			entrants: []*server.MatchmakerEntry{
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
			entrants: []*server.MatchmakerEntry{
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
			entrants: []*server.MatchmakerEntry{
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
		entrants []*server.MatchmakerEntry
		expected [][]*server.MatchmakerEntry
	}{
		{
			name: "Single entrant with one ticket",
			entrants: []*server.MatchmakerEntry{
				{
					Ticket: "ticket1",
				},
			},
			expected: [][]*server.MatchmakerEntry{
				{
					{
						Ticket: "ticket1",
					},
				},
			},
		},
		{
			name: "Multiple entrants with different tickets",
			entrants: []*server.MatchmakerEntry{
				{
					Ticket: "ticket1",
				},
				{
					Ticket: "ticket2",
				},
			},
			expected: [][]*server.MatchmakerEntry{
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
			entrants: []*server.MatchmakerEntry{
				{
					Ticket: "ticket1",
				},
				{
					Ticket: "ticket1",
				},
			},
			expected: [][]*server.MatchmakerEntry{
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
			entrants: []*server.MatchmakerEntry{
				{
					Ticket: "",
				},
			},
			expected: [][]*server.MatchmakerEntry{
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
