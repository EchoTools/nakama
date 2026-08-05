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
	for _, f := range candidatesFilenames {
		requireCharacterizationFixture(t, f)
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
		name string
		mode string
		// entries grouped by ticket: each entry in the outer slice = 1 ticket,
		// value = number of players for that ticket. []int{4,1,1,1,1} means
		// one party of 4 and 4 solos = 8 total.
		ticketSizes []int
		// optional override for max_team_size (affects maxCount = *2)
		maxTeamSize int
		// count_multiple; 0 means the production default of 2
		countMultiple float64
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
			name:        "arena party of 4 with max_team_size=3 (maxCount=6) — straddles, popped to empty",
			mode:        "echo_arena",
			ticketSizes: []int{4, 2}, // candidate=6, boundary=3, party at 0-4 straddles
			maxTeamSize: 3,
			wantSizes:   nil, // pop 2 → [4]=4, check boundary=2, still straddles → pop 4 → empty
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
			// Regression: the offending group is at index 1, not the head.
			// 3+1 and 2+2 both make a team of 4, so this is a legal 4v4 and
			// must be emitted whole. The positional check saw 3+2>4 at pos=3
			// and drained the candidate to nothing, starving all 8 players.
			name:        "arena 3-2-2-1 — even split exists (3+1 vs 2+2)",
			mode:        "echo_arena",
			ticketSizes: []int{3, 2, 2, 1},
			wantSizes:   []int{8},
		},
		{
			// Regression: 3+1 vs 3+1 is a legal 4v4. The positional check
			// fired on the second 3-party and popped solos until only [3,3]
			// remained, silently dropping two unrelated solo players.
			name:        "arena 3-3-1-1 — even split exists (3+1 vs 3+1)",
			mode:        "echo_arena",
			ticketSizes: []int{3, 3, 1, 1},
			wantSizes:   []int{8},
		},
		{
			// Anchor: no even split of 3+3+2 into two teams of 4 exists, so
			// trimming the 2-party down to a 3v3 is the correct salvage.
			// This must keep working — it is the guard's whole justification.
			name:        "arena 3-3-2 — no 4v4 split, trims to a 3v3",
			mode:        "echo_arena",
			ticketSizes: []int{3, 3, 2},
			wantSizes:   []int{6},
		},
		{
			// Anchor: two 4-parties are exactly one team each.
			name:        "arena 4-4 — each party is exactly one team",
			mode:        "echo_arena",
			ticketSizes: []int{4, 4},
			wantSizes:   []int{8},
		},
		{
			// A 5-party cannot occupy a 4-seat team, and 5+3 has no even
			// split, so nothing here is formable.
			name:        "arena 5-3 — 5-party cannot fit a 4-seat team",
			mode:        "echo_arena",
			ticketSizes: []int{5, 3},
			wantSizes:   nil,
		},
		{
			// A party of 3 plus one solo cannot be split 2v2.
			name:        "arena 3-1 — no 2v2 split of a 3-party",
			mode:        "echo_arena",
			ticketSizes: []int{3, 1},
			wantSizes:   nil,
		},
		{
			// count_multiple is operator-configurable (MatchmakingTicketParameters).
			// At 1 the countMultiple trim never fires, so the feasibility guard
			// is the only thing standing between an odd pool and a candidate
			// that can never satisfy len(blue) == len(orange).
			name:          "arena count_multiple=1 — lone solo is not an even match",
			mode:          "echo_arena",
			ticketSizes:   []int{1},
			countMultiple: 1,
			wantSizes:     nil,
		},
		{
			// 5 players, odd: trim the tail solo and the remaining 2+2 is a
			// legal 2v2. Proves the odd-total pop converges on a real salvage
			// rather than draining.
			name:          "arena count_multiple=1 — odd pool trims to an even 2v2",
			mode:          "echo_arena",
			ticketSizes:   []int{2, 2, 1},
			countMultiple: 1,
			wantSizes:     []int{4},
		},
		{
			// 5 players with an atomic 3-party: no even split survives any
			// trim, so nothing is emitted.
			name:          "arena count_multiple=1 — odd pool with a 3-party drains",
			mode:          "echo_arena",
			ticketSizes:   []int{3, 1, 1},
			countMultiple: 1,
			wantSizes:     nil,
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
			ticketSizeOf := map[string]int{}
			for _, sz := range tt.ticketSizes {
				ticketID++
				ticket := fmt.Sprintf("ticket-%d", ticketID)
				ticketSizeOf[ticket] = sz
				for j := range sz {
					// Config lives in Properties, not NumericProperties:
					// MatchmakerEntry.GetProperties() returns only Properties
					// (server/matchmaker.go), so anything set in
					// NumericProperties is invisible to the code under test.
					// Every entry in a real pool carries the same config, so
					// set it on every entry rather than only the first.
					props := map[string]any{
						"game_mode":      tt.mode,
						"count_multiple": tt.countMultiple,
					}
					if tt.maxTeamSize > 0 {
						props["max_team_size"] = float64(tt.maxTeamSize)
					}
					entries = append(entries, &MatchmakerEntry{
						Ticket: ticket,
						Presence: &MatchmakerPresence{
							SessionId: fmt.Sprintf("sess-%d-%d", ticketID, j),
							UserId:    fmt.Sprintf("user-%d-%d", ticketID, j),
							Node:      "test",
						},
						Properties:       props,
						StringProperties: map[string]string{"game_mode": tt.mode},
					})
				}
			}

			candidates := groupEntriesSequentially(entries)

			// Sizes alone would pass for an implementation that emitted the
			// right count of entries built from a truncated ticket, so check
			// membership too: for arena every emitted ticket must appear whole
			// and in exactly one candidate.
			if tt.mode == "echo_arena" {
				appearances := map[string]int{}
				for i, c := range candidates {
					counts := map[string]int{}
					for _, e := range c {
						counts[e.GetTicket()]++
					}
					for ticket, n := range counts {
						appearances[ticket]++
						if n != ticketSizeOf[ticket] {
							t.Errorf("candidate[%d] holds %d of ticket %s's %d entries (split)", i, n, ticket, ticketSizeOf[ticket])
						}
					}
				}
				for ticket, n := range appearances {
					if n > 1 {
						t.Errorf("ticket %s appears in %d candidates", ticket, n)
					}
				}
			}

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

// TestGroupEntriesSequentially_Boundaries covers the edges of the packing
// contract: empty input, maxCount derived from max_team_size, a party that
// exactly equals maxCount, and a party too large to fit at all.
func TestGroupEntriesSequentially_Boundaries(t *testing.T) {
	// maxCount = max_team_size * 2, so max_team_size=2 gives 4 seats / 2-seat teams.
	small := map[string]any{"max_team_size": 2.0, "count_multiple": 2.0}

	t.Run("empty input returns no candidates", func(t *testing.T) {
		if got := groupEntriesSequentially(nil); got != nil {
			t.Errorf("expected nil for empty input, got %v", candidateSizes(got))
		}
		if got := groupEntriesSequentially([]runtime.MatchmakerEntry{}); got != nil {
			t.Errorf("expected nil for empty slice, got %v", candidateSizes(got))
		}
	})

	t.Run("party exactly equals maxCount but cannot split into two teams", func(t *testing.T) {
		// A 4-party fills all 4 seats, but both seats-per-team are 2 and the
		// party is atomic, so no legal 2v2 exists. Prediction would reject it
		// at len(blueTeam) != len(orangeTeam); emitting it is pure waste.
		entries := makePartyTicket("quad", 4, small)
		if got := groupEntriesSequentially(entries); len(got) != 0 {
			t.Errorf("expected no candidates for a lone 4-party at maxCount=4, got %v", candidateSizes(got))
		}
	})

	t.Run("two 2-parties exactly fill both teams", func(t *testing.T) {
		var entries []runtime.MatchmakerEntry
		entries = append(entries, makePartyTicket("duoA", 2, small)...)
		entries = append(entries, makePartyTicket("duoB", 2, small)...)

		candidates := groupEntriesSequentially(entries)
		if len(candidates) != 1 {
			t.Fatalf("expected 1 candidate, got %d: %v", len(candidates), candidateSizes(candidates))
		}
		if len(candidates[0]) != 4 {
			t.Fatalf("expected candidate size 4, got %d", len(candidates[0]))
		}
	})

	t.Run("party larger than maxCount is skipped entirely", func(t *testing.T) {
		entries := makePartyTicket("toobig", 5, small)
		if got := groupEntriesSequentially(entries); len(got) != 0 {
			t.Errorf("expected no candidates for a 5-party at maxCount=4, got %v", candidateSizes(got))
		}
	})

	t.Run("oversized party is skipped without starving the rest", func(t *testing.T) {
		// The 5-party can never fit maxCount=4; the two solos still form a 1v1.
		var entries []runtime.MatchmakerEntry
		entries = append(entries, makePartyTicket("toobig", 5, small)...)
		entries = append(entries, makePartyTicket("s1", 1, small)...)
		entries = append(entries, makePartyTicket("s2", 1, small)...)

		candidates := groupEntriesSequentially(entries)
		if len(candidates) != 1 {
			t.Fatalf("expected 1 candidate, got %d: %v", len(candidates), candidateSizes(candidates))
		}
		if len(candidates[0]) != 2 {
			t.Fatalf("expected candidate size 2, got %d", len(candidates[0]))
		}
		for _, e := range candidates[0] {
			if e.GetTicket() == "toobig" {
				t.Errorf("oversized party leaked into a candidate")
			}
		}
	})
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
