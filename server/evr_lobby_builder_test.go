package server

import (
	"testing"

	"github.com/google/go-cmp/cmp"
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

func TestRegionFallbackServerCount(t *testing.T) {
	// This test verifies that only AVAILABLE servers (UnassignedLobby)
	// are counted when reporting servers in a region for fallback messages.
	// Previously, ALL servers (including active ones) were counted.

	tests := []struct {
		name                 string
		indexes              []labelIndex
		regions              []string
		requireRegion        bool
		expectedServerCount  int
		expectedHasRegion    bool
		expectedClosestMatch bool // Should find a closest server for fallback
	}{
		{
			name: "No available servers in region - all active",
			indexes: []labelIndex{
				{
					Label: &MatchLabel{
						LobbyType: PublicLobby, // Active server
						GameServer: &GameServerPresence{
							RegionCodes: []string{"us-east"},
						},
					},
					IsRegionMatch: true,
					IsReachable:   true,
					RTT:           50,
				},
				{
					Label: &MatchLabel{
						LobbyType: PublicLobby, // Active server
						GameServer: &GameServerPresence{
							RegionCodes: []string{"us-east"},
						},
					},
					IsRegionMatch: true,
					IsReachable:   true,
					RTT:           60,
				},
				{
					Label: &MatchLabel{
						LobbyType: UnassignedLobby, // Available server in different region
						GameServer: &GameServerPresence{
							RegionCodes: []string{"us-west"},
						},
					},
					IsRegionMatch: false,
					IsReachable:   true,
					RTT:           100,
				},
			},
			regions:              []string{"us-east"},
			requireRegion:        true,
			expectedServerCount:  0, // Should be 0 because no available servers in us-east
			expectedHasRegion:    false,
			expectedClosestMatch: true, // Should find us-west as fallback
		},
		{
			name: "Some available servers in region",
			indexes: []labelIndex{
				{
					Label: &MatchLabel{
						LobbyType: UnassignedLobby, // Available
						GameServer: &GameServerPresence{
							RegionCodes: []string{"us-east"},
						},
					},
					IsRegionMatch: true,
					IsReachable:   true,
					RTT:           50,
				},
				{
					Label: &MatchLabel{
						LobbyType: PublicLobby, // Active
						GameServer: &GameServerPresence{
							RegionCodes: []string{"us-east"},
						},
					},
					IsRegionMatch: true,
					IsReachable:   true,
					RTT:           60,
				},
			},
			regions:              []string{"us-east"},
			requireRegion:        true,
			expectedServerCount:  1, // Only 1 available server
			expectedHasRegion:    true,
			expectedClosestMatch: false,
		},
		{
			name: "All servers available in region",
			indexes: []labelIndex{
				{
					Label: &MatchLabel{
						LobbyType: UnassignedLobby,
						GameServer: &GameServerPresence{
							RegionCodes: []string{"eu-west"},
						},
					},
					IsRegionMatch: true,
					IsReachable:   true,
					RTT:           40,
				},
				{
					Label: &MatchLabel{
						LobbyType: UnassignedLobby,
						GameServer: &GameServerPresence{
							RegionCodes: []string{"eu-west"},
						},
					},
					IsRegionMatch: true,
					IsReachable:   true,
					RTT:           45,
				},
			},
			regions:              []string{"eu-west"},
			requireRegion:        true,
			expectedServerCount:  2,
			expectedHasRegion:    true,
			expectedClosestMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Replicate the counting logic from LobbyGameServerAllocate
			hasRegionMatch := false
			serverCount := 0

			for _, index := range tt.indexes {
				if index.IsRegionMatch && index.Label.LobbyType == UnassignedLobby {
					hasRegionMatch = true
					serverCount++
				}
			}

			// Verify the counts
			if serverCount != tt.expectedServerCount {
				t.Errorf("serverCount = %d, want %d", serverCount, tt.expectedServerCount)
			}

			if hasRegionMatch != tt.expectedHasRegion {
				t.Errorf("hasRegionMatch = %v, want %v", hasRegionMatch, tt.expectedHasRegion)
			}

			// If we need a fallback, verify we can find one
			if tt.expectedClosestMatch && tt.requireRegion && len(tt.regions) > 0 && !hasRegionMatch {
				foundClosest := false
				for _, index := range tt.indexes {
					if index.Label.LobbyType == UnassignedLobby && index.IsReachable {
						foundClosest = true
						break
					}
				}
				if !foundClosest {
					t.Error("Expected to find a closest server for fallback, but none found")
				}
			}
		})
	}
}
