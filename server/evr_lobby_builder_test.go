package server

import (
	"encoding/json"
	"os"
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
func TestRankEndpointsByAverageLatency(t *testing.T) {

	// open /tmp/candidates.json

	file, err := os.Open("/tmp/candidates.json")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	fmap := map[string][][]*MatchmakerEntry{}

	if err := json.NewDecoder(file).Decode(&fmap); err != nil {
		t.Fatal(err)
	}

	candidates := fmap["candidates"]

	entrants := candidates[0]

	lb := &LobbyBuilder{}
	result := lb.rankEndpointsByAverageLatency(entrants)

	t.Errorf("latency result: %v", result)

	result = lb.rankEndpointsByServerScore(entrants)

	t.Errorf("server score result: %v", result)
}
