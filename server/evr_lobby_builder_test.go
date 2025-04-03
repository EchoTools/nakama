package server

import (
	"testing"

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
				{regionMatches: false},
				{regionMatches: true},
			},
			expected: []labelIndex{
				{regionMatches: true},
				{regionMatches: false},
			},
		},
		{
			name: "Sort by rtt",
			labels: []labelIndex{
				{rtt: 100},
				{rtt: 50},
			},
			expected: []labelIndex{
				{rtt: 50},
				{rtt: 100},
			},
		},
		{
			name: "Sort by rating",
			labels: []labelIndex{
				{rating: 100},
				{rating: 50},
			},
			expected: []labelIndex{
				{rating: 100},
				{rating: 50},
			},
		},
		{
			name: "Sort by isPriorityForMode",
			labels: []labelIndex{
				{isPriorityForMode: false},
				{isPriorityForMode: true},
			},
			expected: []labelIndex{
				{isPriorityForMode: true},
				{isPriorityForMode: false},
			},
		},
		{
			name: "Sort by isReachable",
			labels: []labelIndex{
				{isReachable: false},
				{isReachable: true},
			},
			expected: []labelIndex{
				{isReachable: true},
				{isReachable: false},
			},
		},
		{
			name: "Sort by activeCount",
			labels: []labelIndex{
				{activeCount: 2},
				{activeCount: 1},
			},
			expected: []labelIndex{
				{activeCount: 1},
				{activeCount: 2},
			},
		},
		{
			name: "Complex sorting",
			labels: []labelIndex{
				{regionMatches: false, rtt: 100, rating: 50, isPriorityForMode: false, isReachable: false, activeCount: 2},
				{regionMatches: true, rtt: 50, rating: 100, isPriorityForMode: true, isReachable: true, activeCount: 1},
				{regionMatches: false, rtt: 50, rating: 50, isPriorityForMode: true, isReachable: true, activeCount: 2},
				{regionMatches: true, rtt: 100, rating: 100, isPriorityForMode: false, isReachable: false, activeCount: 1},
			},
			expected: []labelIndex{
				{regionMatches: true, rtt: 50, rating: 100, isPriorityForMode: true, isReachable: true, activeCount: 1},
				{regionMatches: true, rtt: 100, rating: 100, isPriorityForMode: false, isReachable: false, activeCount: 1},
				{regionMatches: false, rtt: 50, rating: 50, isPriorityForMode: true, isReachable: true, activeCount: 2},
				{regionMatches: false, rtt: 100, rating: 50, isPriorityForMode: false, isReachable: false, activeCount: 2},
			},
		},
		{
			name: "RTT difference less than 30",
			labels: []labelIndex{
				{rtt: 60},
				{rtt: 50},
			},
			expected: []labelIndex{
				{rtt: 60},
				{rtt: 50},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			labelsCopy := make([]labelIndex, len(tt.labels))
			copy(labelsCopy, tt.labels)

			sortLabelIndexes(labelsCopy)
			assert.Equal(t, tt.expected, labelsCopy)
		})
	}
}
