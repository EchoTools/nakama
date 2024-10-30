package server

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestMroundRTT(t *testing.T) {
	tests := []struct {
		name     string
		rtt      time.Duration
		modulus  time.Duration
		expected time.Duration
	}{
		{
			name:     "Test Case 1",
			rtt:      12 * time.Millisecond,
			modulus:  5 * time.Millisecond,
			expected: 10 * time.Millisecond,
		},
		{
			name:     "Test Case 2",
			rtt:      27 * time.Millisecond,
			modulus:  15 * time.Millisecond,
			expected: 30 * time.Millisecond,
		},
		{
			name:     "Test Case 3",
			rtt:      25 * time.Millisecond,
			modulus:  15 * time.Millisecond,
			expected: 30 * time.Millisecond,
		},
		{
			name:     "zero returns zero",
			rtt:      0 * time.Millisecond,
			modulus:  15 * time.Millisecond,
			expected: 0 * time.Millisecond,
		},
		{
			name:     ">modulus returns modulus",
			rtt:      1 * time.Millisecond,
			modulus:  15 * time.Millisecond,
			expected: 15 * time.Millisecond,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mroundRTT(tt.rtt, tt.modulus)
			if result != tt.expected {
				t.Errorf("Expected %v, but got %v", tt.expected, result)
			}
		})
	}
}

func TestRTTweightedPopulationComparison(t *testing.T) {
	tests := []struct {
		name     string
		i        time.Duration
		j        time.Duration
		o        int
		p        int
		expected bool
	}{
		{
			name:     "Test Case 1",
			i:        100 * time.Millisecond,
			j:        80 * time.Millisecond,
			o:        10,
			p:        5,
			expected: true,
		},
		{
			name:     "Test Case 2",
			i:        80 * time.Millisecond,
			j:        100 * time.Millisecond,
			o:        5,
			p:        10,
			expected: false,
		},
		{
			name:     "Test Case 3",
			i:        90 * time.Millisecond,
			j:        90 * time.Millisecond,
			o:        5,
			p:        5,
			expected: false,
		},
		// Add more test cases as needed
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RTTweightedPopulationCmp(tt.i, tt.j, tt.o, tt.p)
			if result != tt.expected {
				t.Errorf("Expected %v, but got %v", tt.expected, result)
			}
		})
	}
}

func Test_balanceMatches(t *testing.T) {

	// read in the possible_matches.json file

	file, err := os.Open("/home/andrew/src/echovrce-mm/possible-matches.json")
	if err != nil {
		t.Error("Error opening file")
	}
	defer file.Close()

	// read in the file
	decoder := json.NewDecoder(file)
	var candidates []*PredictedMatch
	err = decoder.Decode(&candidates)
	if err != nil {
		t.Errorf("Error decoding file: %v", err)
	}

	seenRosters := make(map[string]struct{})
	for _, match := range candidates {
		roster := make([]string, 0, len(match.Entrants()))
		for _, e := range match.Entrants() {
			roster = append(roster, e.Entry.GetTicket())
		}
		slices.Sort(roster)
		rosterString := strings.Join(roster, ",")
		if _, ok := seenRosters[rosterString]; ok {
			continue
		}
		seenRosters[rosterString] = struct{}{}
	}

	t.Log("Possible Match count", len(candidates))
	t.Log("Seen Rosters count", len(seenRosters))
	t.Error(" ")
}
