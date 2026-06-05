package server

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
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
	FixDefaultServiceSettings(nil, globalSettings)
	flatEntries := make([]runtime.MatchmakerEntry, 0)
	for _, candidate := range runtimeCombinations {
		flatEntries = append(flatEntries, candidate...)
	}
	_, returnedEntries, _, _ := sbmm.processPotentialMatches(NewRuntimeGoLogger(zap.NewNop()), flatEntries)
	t.Logf("Matched %d candidate matches in %s", len(returnedEntries), time.Since(startTime))

	t.Errorf("autofail")

}

// TestGroupEntriesSequentially_NoStraddle verifies that a candidate with a
// party that straddles the team boundary is rejected by popping from the tail
// (arena only). Combat mode skips the straddle check.
func TestGroupEntriesSequentially_NoStraddle(t *testing.T) {
	tests := []struct {
		name       string
		mode       string
		// entries grouped by ticket: each entry in the outer slice = 1 ticket,
		// value = number of players for that ticket. []int{4,1,1,1,1} means
		// one party of 4 and 4 solos = 8 total.
		ticketSizes []int
		// optional override for max_team_size on the first entry (affects maxCount)
		maxTeamSize int
		// expected candidate sizes after grouping, nil = flushed as empty
		wantSizes []int
	}{
		{
			name:        "arena party of 4 first with 4 solos — no straddle",
			mode:        "echo_arena",
			ticketSizes: []int{4, 1, 1, 1, 1},
			wantSizes:   []int{8},
		},
		{
			name:         "arena party of 4 with max_team_size=3 (maxCount=6) — straddles, popped to empty",
			mode:         "echo_arena",
			ticketSizes:  []int{4, 2}, // candidate=6, boundary=3, party at 0-4 straddles
			maxTeamSize:  3,
			wantSizes:    nil, // pop 2 → [4]=4, check boundary=2, still straddles → pop 4 → empty
		},
		{
			name:        "arena party of 4 and party of 2 — 6 total, straddles (boundary=3), popped",
			mode:        "echo_arena",
			ticketSizes: []int{4, 2},
			wantSizes:   nil, // party 0-4 straddles boundary 3 → pop 2 → [4]=4, boundary 2 → still straddles → pop → empty
		},
		{
			name:        "arena only solos — no parties, always valid",
			mode:        "echo_arena",
			ticketSizes: []int{1, 1, 1, 1, 1, 1, 1, 1},
			wantSizes:   []int{8},
		},
		{
			name:        "arena party of 3 and 5 solos — no straddle",
			mode:        "echo_arena",
			ticketSizes: []int{3, 1, 1, 1, 1, 1},
			wantSizes:   []int{8},
		},
		{
			name:        "combat same config as arena — straddle check skipped",
			mode:        "echo_combat",
			ticketSizes: []int{4, 1, 1, 1, 1},
			wantSizes:   []int{8},
		},
		{
			name:        "combat party of 4 — combat splits by user, no straddle check needed",
			mode:        "echo_combat",
			ticketSizes: []int{4, 2},
			wantSizes:   []int{6},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build flat entry list
			var entries []runtime.MatchmakerEntry
			ticketID := 0
			firstEntry := true
			for _, sz := range tt.ticketSizes {
				ticketID++
				ticket := fmt.Sprintf("ticket-%d", ticketID)
				for j := range sz {
					props := map[string]any{
						"game_mode": tt.mode,
					}
					numProps := map[string]float64{
						"count_multiple": 2,
						"max_count":      8,
					}
					// Apply max_team_size override only to the first entry
					if firstEntry && tt.maxTeamSize > 0 {
						props["max_team_size"] = float64(tt.maxTeamSize)
					}
					_ = j
					entries = append(entries, &MatchmakerEntry{
						Ticket: ticket,
						Presence: &MatchmakerPresence{
							SessionId: fmt.Sprintf("sess-%d-%d", ticketID, j),
							UserId:    fmt.Sprintf("user-%d-%d", ticketID, j),
							Node:      "test",
						},
						Properties:        props,
						StringProperties:  map[string]string{"game_mode": tt.mode},
						NumericProperties: numProps,
					})
				}
				firstEntry = false
			}

			candidates := groupEntriesSequentially(entries)

			if tt.wantSizes == nil {
				if len(candidates) != 0 {
					t.Errorf("expected no candidates, got %d candidates with sizes %v", len(candidates), candidateSizes(candidates))
				}
				return
			}

			if len(candidates) != len(tt.wantSizes) {
				t.Errorf("expected %d candidates, got %d: sizes %v", len(tt.wantSizes), len(candidates), candidateSizes(candidates))
				return
			}
			for i, want := range tt.wantSizes {
				if len(candidates[i]) != want {
					t.Errorf("candidate[%d]: got size %d, want %d (all sizes: %v)", i, len(candidates[i]), want, candidateSizes(candidates))
				}
			}
		})
	}
}

func candidateSizes(candidates [][]runtime.MatchmakerEntry) []int {
	sizes := make([]int, len(candidates))
	for i, c := range candidates {
		sizes[i] = len(c)
	}
	return sizes
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
