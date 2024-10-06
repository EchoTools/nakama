package server

import (
	"reflect"
	"testing"
)

func TestSortByGreatestPlayerAvailability(t *testing.T) {
	tests := []struct {
		name                 string
		rttByPlayerByExtIP   map[string]map[string]int
		expectedSortedExtIPs []string
	}{
		{
			name: "Single player single IP",
			rttByPlayerByExtIP: map[string]map[string]int{
				"192.168.1.1": {"player1": 50},
			},
			expectedSortedExtIPs: []string{"192.168.1.1"},
		},
		{
			name: "Multiple players single IP",
			rttByPlayerByExtIP: map[string]map[string]int{
				"192.168.1.1": {"player1": 50, "player2": 60, "player3": 70},
			},
			expectedSortedExtIPs: []string{"192.168.1.1"},
		},
		{
			name: "Multiple players multiple IPs",
			rttByPlayerByExtIP: map[string]map[string]int{
				"192.168.1.1": {"player1": 50, "player2": 60, "player3": 70},
				"192.168.1.2": {"player4": 40},
			},
			expectedSortedExtIPs: []string{"192.168.1.1", "192.168.1.2"},
		},
		{
			name: "Same player count different RTTs",
			rttByPlayerByExtIP: map[string]map[string]int{
				"192.168.1.1": {"player1": 50},
				"192.168.1.2": {"player2": 40},
			},
			expectedSortedExtIPs: []string{"192.168.1.2", "192.168.1.1"},
		},
		{
			name: "Same player count, same RTTs",
			rttByPlayerByExtIP: map[string]map[string]int{
				"192.168.1.1": {"player1": 50},
				"192.168.1.2": {"player2": 50},
			},
			expectedSortedExtIPs: []string{"192.168.1.1", "192.168.1.2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sortedExtIPs := sortByGreatestPlayerAvailability(tt.rttByPlayerByExtIP)
			if !reflect.DeepEqual(sortedExtIPs, tt.expectedSortedExtIPs) {
				t.Errorf("expected %v, got %v", tt.expectedSortedExtIPs, sortedExtIPs)
			}
		})
	}
}
