package server

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/heroiclabs/nakama-common/runtime"
)

// Test filtering candidates based on max RTT to common servers
func TestHasEligibleServers(t *testing.T) {

	tests := []struct {
		name       string
		candidates [][]runtime.MatchmakerEntry
		want       [][]runtime.MatchmakerEntry
	}{
		{
			name: "All servers within maxRTT",
			candidates: [][]runtime.MatchmakerEntry{
				{
					&MatchmakerEntry{Properties: map[string]interface{}{"max_rtt": 110.0, "rtt_server1": 50.0, "rtt_server2": 60.0}},
					&MatchmakerEntry{Properties: map[string]interface{}{"rtt_server1": 40.0, "rtt_server2": 55.0}},
				},
			},

			want: [][]runtime.MatchmakerEntry{
				{
					&MatchmakerEntry{Properties: map[string]interface{}{"max_rtt": 110.0, "rtt_server1": 50.0, "rtt_server2": 60.0}},
					&MatchmakerEntry{Properties: map[string]interface{}{"rtt_server1": 40.0, "rtt_server2": 55.0}},
				},
			},
		},
		{
			name: "One server exceeds maxRTT",
			candidates: [][]runtime.MatchmakerEntry{{
				&MatchmakerEntry{Properties: map[string]interface{}{"max_rtt": 110.0, "rtt_server1": 150.0, "rtt_server2": 60.0}},
				&MatchmakerEntry{Properties: map[string]interface{}{"max_rtt": 110.0, "rtt_server1": 40.0, "rtt_server2": 55.0}},
			}},
			want: [][]runtime.MatchmakerEntry{{
				&MatchmakerEntry{Properties: map[string]interface{}{"max_rtt": 110.0, "rtt_server1": 150.0, "rtt_server2": 60.0}},
				&MatchmakerEntry{Properties: map[string]interface{}{"max_rtt": 110.0, "rtt_server1": 40.0, "rtt_server2": 55.0}},
			}},
		},
		{
			name: "Server unreachable for one player",
			candidates: [][]runtime.MatchmakerEntry{{
				&MatchmakerEntry{Properties: map[string]interface{}{"rtt_server1": 50.0}},
				&MatchmakerEntry{Properties: map[string]interface{}{"rtt_server1": 20.0, "rtt_server2": 55.0}},
			}},
			want: [][]runtime.MatchmakerEntry{{
				&MatchmakerEntry{Properties: map[string]interface{}{"rtt_server1": 50.0}},
				&MatchmakerEntry{Properties: map[string]interface{}{"rtt_server1": 20.0, "rtt_server2": 55.0}},
			}},
		},
		{
			name: "No common servers for players",
			candidates: [][]runtime.MatchmakerEntry{{
				&MatchmakerEntry{Properties: map[string]interface{}{"rtt_server1": 50.0}},
				&MatchmakerEntry{Properties: map[string]interface{}{"rtt_server2": 55.0}},
			}},
			want: [][]runtime.MatchmakerEntry{
				nil,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewSkillBasedMatchmaker()

			if count := m.filterWithinMaxRTT(tt.candidates); cmp.Diff(tt.want, tt.candidates) != "" {
				t.Errorf("hasEligibleServers() = %d: (want/got) %s", count, cmp.Diff(tt.want, tt.candidates))
			}
		})
	}
}

// Test processing potential matches from candidates file
func TestOverrideFn(t *testing.T) {
	var data CandidateData
	candidatesFilenames := []string{
		"/tmp/candidates.json",
	}

	for _, candidatesFilename := range candidatesFilenames {

		// Load the candidate data from the json file
		reader, err := os.Open(candidatesFilename)
		if err != nil {
			t.Error("Error opening file")
		}

		// read in the file
		decoder := json.NewDecoder(reader)
		err = decoder.Decode(&data)
		if err != nil {
			t.Errorf("Error decoding file: %v", err)
		}

		// Dedupe the candidates
		candidateByTicket := make(map[string]map[string]*MatchmakerEntry)
		for i, match := range data.Candidates {
			for j, entry := range match {
				if _, ok := candidateByTicket[entry.Ticket]; !ok {
					candidateByTicket[entry.Ticket] = make(map[string]*MatchmakerEntry)
				}
				if _, ok := candidateByTicket[entry.Ticket][entry.Presence.SessionId]; !ok {
					candidateByTicket[entry.Ticket][entry.Presence.SessionId] = entry
				} else {
					data.Candidates[i][j] = candidateByTicket[entry.Ticket][entry.Presence.SessionId]
				}
			}
		}
	}

	// Garbage collect the candidates

	t.Logf("candidates: %d", len(data.Candidates))

	sbmm := NewSkillBasedMatchmaker()

	runtimeCombinations := make([][]runtime.MatchmakerEntry, len(data.Candidates))
	for i, combination := range data.Candidates {
		runtimeEntry := make([]runtime.MatchmakerEntry, len(combination))
		for j, entry := range combination {
			runtimeEntry[j] = entry
		}
		runtimeCombinations[i] = runtimeEntry
	}

	t.Logf("Processing %d candidate matches", len(runtimeCombinations))
	startTime := time.Now()
	globalSettings := &ServiceSettingsData{}
	FixDefaultServiceSettings(globalSettings)
	_, returnedEntries, _ := sbmm.processPotentialMatches(runtimeCombinations)
	t.Logf("Matched %d candidate matches in %s", len(returnedEntries), time.Since(startTime))

	t.Errorf("autofail")

}

// Test filtering candidates based on max RTT to common servers
func TestFilterWithinMaxRTT(t *testing.T) {
	tests := []struct {
		name       string
		candidates [][]runtime.MatchmakerEntry
		want       [][]runtime.MatchmakerEntry
		wantCount  int
	}{
		{
			name: "All servers within maxRTT",
			candidates: [][]runtime.MatchmakerEntry{
				{
					&MatchmakerEntry{Properties: map[string]interface{}{"max_rtt": 110.0, "rtt_server1": 50.0, "rtt_server2": 60.0}},
					&MatchmakerEntry{Properties: map[string]interface{}{"rtt_server1": 40.0, "rtt_server2": 55.0}},
				},
			},
			want: [][]runtime.MatchmakerEntry{
				{
					&MatchmakerEntry{Properties: map[string]interface{}{"max_rtt": 110.0, "rtt_server1": 50.0, "rtt_server2": 60.0}},
					&MatchmakerEntry{Properties: map[string]interface{}{"rtt_server1": 40.0, "rtt_server2": 55.0}},
				},
			},
			wantCount: 0,
		},
		{
			name: "One server exceeds maxRTT",
			candidates: [][]runtime.MatchmakerEntry{{
				&MatchmakerEntry{Properties: map[string]interface{}{"max_rtt": 110.0, "rtt_server1": 150.0, "rtt_server2": 60.0}},
				&MatchmakerEntry{Properties: map[string]interface{}{"max_rtt": 110.0, "rtt_server1": 40.0, "rtt_server2": 55.0}},
			}},
			want: [][]runtime.MatchmakerEntry{{
				&MatchmakerEntry{Properties: map[string]interface{}{"max_rtt": 110.0, "rtt_server1": 150.0, "rtt_server2": 60.0}},
				&MatchmakerEntry{Properties: map[string]interface{}{"max_rtt": 110.0, "rtt_server1": 40.0, "rtt_server2": 55.0}},
			}},
			wantCount: 0,
		},
		{
			name: "Server unreachable for one player",
			candidates: [][]runtime.MatchmakerEntry{{
				&MatchmakerEntry{Properties: map[string]interface{}{"rtt_server1": 50.0}},
				&MatchmakerEntry{Properties: map[string]interface{}{"rtt_server1": 20.0, "rtt_server2": 55.0}},
			}},
			want: [][]runtime.MatchmakerEntry{{
				&MatchmakerEntry{Properties: map[string]interface{}{"rtt_server1": 50.0}},
				&MatchmakerEntry{Properties: map[string]interface{}{"rtt_server1": 20.0, "rtt_server2": 55.0}},
			}},
			wantCount: 0,
		},
		{
			name: "No common servers for players",
			candidates: [][]runtime.MatchmakerEntry{{
				&MatchmakerEntry{Properties: map[string]interface{}{"rtt_server1": 50.0}},
				&MatchmakerEntry{Properties: map[string]interface{}{"rtt_server2": 55.0}},
			}},
			want: [][]runtime.MatchmakerEntry{
				nil,
			},
			wantCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewSkillBasedMatchmaker()
			gotCount := m.filterWithinMaxRTT(tt.candidates)
			if gotCount != tt.wantCount {
				t.Errorf("filterWithinMaxRTT() gotCount = %v, want %v", gotCount, tt.wantCount)
			}
			if !reflect.DeepEqual(tt.candidates, tt.want) {
				t.Errorf("filterWithinMaxRTT() candidates = %v, want %v", tt.candidates, tt.want)
			}
		})
	}
}
