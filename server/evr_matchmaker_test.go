package server

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/google/go-cmp/cmp"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/intinig/go-openskill/types"
	"go.uber.org/zap"
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
			name:     "equal to modulus returns rtt",
			rtt:      30 * time.Millisecond,
			modulus:  15 * time.Millisecond,
			expected: 30 * time.Millisecond,
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
			want: [][]runtime.MatchmakerEntry{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewSkillBasedMatchmaker()

			if got, count := m.filterWithinMaxRTT(tt.candidates); cmp.Diff(tt.want, got) != "" {
				t.Errorf("hasEligibleServers() = %d: (want/got) %s", count, cmp.Diff(tt.want, got))
			}
		})
	}
}

func TestCreateBalancedMatch(t *testing.T) {
	tests := []struct {
		name      string
		groups    [][]*RatedEntry
		teamSize  int
		wantTeam1 RatedEntryTeam
		wantTeam2 RatedEntryTeam
	}{
		{
			name: "Balanced teams with solo players",
			groups: [][]*RatedEntry{
				{&RatedEntry{Rating: types.Rating{Mu: 25, Sigma: 8.111}}},
				{&RatedEntry{Rating: types.Rating{Mu: 30, Sigma: 8.222}}},
				{&RatedEntry{Rating: types.Rating{Mu: 35, Sigma: 8.333}}},
				{&RatedEntry{Rating: types.Rating{Mu: 40, Sigma: 8.444}}},
			},
			teamSize: 2,
			wantTeam1: RatedEntryTeam{
				&RatedEntry{Rating: types.Rating{Mu: 40, Sigma: 8.444}},
				&RatedEntry{Rating: types.Rating{Mu: 25, Sigma: 8.111}},
			},
			wantTeam2: RatedEntryTeam{
				&RatedEntry{Rating: types.Rating{Mu: 35, Sigma: 8.333}},
				&RatedEntry{Rating: types.Rating{Mu: 30, Sigma: 8.222}},
			},
		},
		{
			name: "Balanced teams with parties",
			groups: [][]*RatedEntry{
				{
					&RatedEntry{Rating: types.Rating{Mu: 25, Sigma: 8.3331}},
					&RatedEntry{Rating: types.Rating{Mu: 30, Sigma: 8.3332}},
				},
				{
					&RatedEntry{Rating: types.Rating{Mu: 40, Sigma: 8.3334}},
					&RatedEntry{Rating: types.Rating{Mu: 35, Sigma: 8.3333}},
				},
			},
			teamSize: 2,
			wantTeam1: RatedEntryTeam{
				&RatedEntry{Rating: types.Rating{Mu: 40, Sigma: 8.3334}},
				&RatedEntry{Rating: types.Rating{Mu: 35, Sigma: 8.3333}},
			},
			wantTeam2: RatedEntryTeam{
				&RatedEntry{Rating: types.Rating{Mu: 30, Sigma: 8.3332}},
				&RatedEntry{Rating: types.Rating{Mu: 25, Sigma: 8.3331}},
			},
		},
		{
			name: "Mixed solo players and parties",
			groups: [][]*RatedEntry{
				{&RatedEntry{Rating: types.Rating{Mu: 25, Sigma: 8.333}}},
				{&RatedEntry{Rating: types.Rating{Mu: 40, Sigma: 8.333}}, &RatedEntry{Rating: types.Rating{Mu: 35, Sigma: 8.333}}},
				{&RatedEntry{Rating: types.Rating{Mu: 30, Sigma: 8.333}}},
			},
			teamSize: 2,
			wantTeam1: RatedEntryTeam{
				&RatedEntry{Rating: types.Rating{Mu: 40, Sigma: 8.333}},
				&RatedEntry{Rating: types.Rating{Mu: 35, Sigma: 8.333}},
			},
			wantTeam2: RatedEntryTeam{
				&RatedEntry{Rating: types.Rating{Mu: 30, Sigma: 8.333}},
				&RatedEntry{Rating: types.Rating{Mu: 25, Sigma: 8.333}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewSkillBasedMatchmaker()
			gotTeam1, gotTeam2 := m.createBalancedMatch(tt.groups, tt.teamSize)

			t.Logf("Team 1 Strength: %f", gotTeam1.Strength())
			t.Logf("Team 2 Strength: %f", gotTeam2.Strength())

			if cmp.Diff(gotTeam1, tt.wantTeam1) != "" {
				t.Errorf("CreateBalancedMatch() team1 (- want / + got) = %s", cmp.Diff(gotTeam1, tt.wantTeam1))
			}
			if !reflect.DeepEqual(gotTeam2, tt.wantTeam2) {
				t.Errorf("CreateBalancedMatch() team2 (- want / + got) = %s", cmp.Diff(gotTeam2, tt.wantTeam2))
			}

		})
	}
}

func TestRemoveOddSizedTeams(t *testing.T) {
	m := NewSkillBasedMatchmaker()

	entries := make([]runtime.MatchmakerEntry, 0)
	for i := 0; i < 5; i++ {
		entries = append(entries, &MatchmakerEntry{Presence: &MatchmakerPresence{SessionId: uuid.NewV5(uuid.Nil, fmt.Sprintf("%d", i)).String()}})
	}

	tests := []struct {
		name       string
		candidates [][]runtime.MatchmakerEntry
		want       [][]runtime.MatchmakerEntry
		wantCount  int
	}{
		{
			name: "No odd-sized teams",
			candidates: [][]runtime.MatchmakerEntry{
				{
					entries[1],
					entries[2],
				},
				{
					entries[3],
					entries[4],
				},
			},
			want: [][]runtime.MatchmakerEntry{
				{
					entries[1],
					entries[2],
				},
				{
					entries[3],
					entries[4],
				},
			},
			wantCount: 0,
		},
		{
			name: "One odd-sized team",
			candidates: [][]runtime.MatchmakerEntry{
				{
					entries[1],
				},
				{
					entries[2],
					entries[3],
				},
			},
			want: [][]runtime.MatchmakerEntry{
				{
					entries[2],
					entries[3],
				},
			},
			wantCount: 1,
		},
		{
			name: "Multiple odd-sized teams",
			candidates: [][]runtime.MatchmakerEntry{
				{
					entries[1],
				},
				{
					entries[2],
					entries[3],
				},
				{
					entries[4],
				},
			},
			want: [][]runtime.MatchmakerEntry{
				{
					entries[2],
					entries[3],
				},
			},
			wantCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotCount := m.filterOddSizedTeams(tt.candidates)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("removeOddSizedTeams() got = %v, want %v", got, tt.want)
			}
			if gotCount != tt.wantCount {
				t.Errorf("removeOddSizedTeams() gotCount = %v, want %v", gotCount, tt.wantCount)
			}
		})
	}
}

type CandidateData struct {
	Candidates [][]*MatchmakerEntry `json:"candidates"`
	Matches    [][]*MatchmakerEntry `json:"matches"`
}

func (c CandidateData) mm() [][]runtime.MatchmakerEntry {
	var candidates [][]runtime.MatchmakerEntry
	for _, entry := range c.Candidates {
		var matchmakerEntries []runtime.MatchmakerEntry
		for _, e := range entry {
			matchmakerEntries = append(matchmakerEntries, e)
		}
		candidates = append(candidates, matchmakerEntries)
	}
	return candidates
}

func TestMatchmaker(t *testing.T) {
	// disable for now
	//t.SkipNow()
	// open /tmp/possible-matches.json
	file, err := os.Open("/tmp/candidates.json")
	if err != nil {
		t.Error("Error opening file")
	}
	defer file.Close()

	var data CandidateData
	// read in the file
	decoder := json.NewDecoder(file)
	err = decoder.Decode(&data)
	if err != nil {
		t.Errorf("Error decoding file: %v", err)
	}
	m := NewSkillBasedMatchmaker()

	candidates := data.mm()

	// Get teh sizes of the candidate matches
	sizes := make(map[int]int, 0)
	for _, c := range candidates {
		sizes[len(c)]++
	}

	t.Errorf("Sizes: %v", sizes)

	candidates, _ = m.filterWithinMaxRTT(candidates)

	// Remove odd sized teams
	candidates, _ = m.filterOddSizedTeams(candidates)

	// Create a list of balanced matches with predictions
	predictions := m.predictOutcomes(candidates)

	m.sortByDraw(predictions)

	writeAsJSONFile(predictions, "/tmp/predictions.json")
	m.sortLimitRankSpread(predictions, 0.10)
	m.sortPriority(predictions)
	rostersByPrediction := make([][]string, 0)
	for _, c := range predictions {
		rosters := make([]string, 0)
		for _, team := range []RatedEntryTeam{c.Team1, c.Team2} {
			roster := make([]string, 0)
			for _, player := range team {
				roster = append(roster, player.Entry.GetPresence().GetSessionId())
			}
			slices.Sort(roster)
			rosters = append(rosters, strings.Join(roster, ","))
		}
		slices.Sort(rosters)
		rostersByPrediction = append(rostersByPrediction, rosters)
	}

	madeMatches := m.assembleUniqueMatches(predictions)

	// Sort by matches that have players who have been waiting more than half the Matchmaking timeout
	// This is to prevent players from waiting too long

	//t.Logf("Predictions: %v", predictions)

	t.Errorf("length: %v", len(madeMatches))

	for _, match := range madeMatches {
		teams := make([]string, 0)

		teamSize := len(match) / 2
		team1 := make([]string, 0)
		for _, player := range match[0:teamSize] {
			team1 = append(team1, player.GetPresence().GetUsername())
		}
		team2 := make([]string, 0)
		for _, player := range match[teamSize:] {
			team2 = append(team2, player.GetPresence().GetUsername())
		}
		teams = append(teams, strings.Join(team1, ","))
		teams = append(teams, strings.Join(team2, ","))

		t.Errorf("Match: %v", strings.Join(teams, " vs "))
	}

	//t.Errorf("Candidates: %v", candidates)
}
func TestSortPriority(t *testing.T) {
	team1 := RatedEntryTeam{
		&RatedEntry{Entry: &MatchmakerEntry{Properties: map[string]interface{}{"priority_threshold": float64(time.Now().Add(-10 * time.Minute).Unix())}, Ticket: "ticket1"}},
	}
	team2 := RatedEntryTeam{
		&RatedEntry{Entry: &MatchmakerEntry{Properties: map[string]interface{}{}}},
	}
	team3 := RatedEntryTeam{
		&RatedEntry{Entry: &MatchmakerEntry{Properties: map[string]interface{}{"priority_threshold": float64(time.Now().Add(-1 * time.Minute).Unix())}, Ticket: "ticket3"}},
	}
	team4 := RatedEntryTeam{
		&RatedEntry{Entry: &MatchmakerEntry{Properties: map[string]interface{}{"priority_threshold": float64(time.Now().Add(-2 * time.Minute).Unix())}, Ticket: "ticket4"}},
	}
	team5 := RatedEntryTeam{
		&RatedEntry{Entry: &MatchmakerEntry{Properties: map[string]interface{}{}, Ticket: "ticket5"}},
	}

	tests := []struct {
		name        string
		predictions []PredictedMatch
		want        []PredictedMatch
	}{
		{
			name: "Mixed priority thresholds",
			predictions: []PredictedMatch{
				{Team1: team1, Team2: team5},
				{Team1: team3, Team2: team4},
				{Team1: team3, Team2: team2},
			},
			want: []PredictedMatch{
				{Team1: team3, Team2: team2},
				{Team1: team3, Team2: team4},
				{Team1: team1, Team2: team5},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewSkillBasedMatchmaker()
			m.sortPriority(tt.predictions)

			for i, got := range tt.predictions {
				want := tt.want[i]
				if !reflect.DeepEqual(got, want) {
					t.Errorf("sortPriority() =\nTeam1: %v\nTeam2: %v\nwant\nTeam1: %v\nTeam2: %v", getTeamTickets(got.Team1), getTeamTickets(got.Team2), getTeamTickets(want.Team1), getTeamTickets(want.Team2))
				}
			}
		})
	}
}

func getTeamTickets(team RatedEntryTeam) []string {
	tickets := make([]string, len(team))
	for i, entry := range team {
		tickets[i] = entry.Entry.Ticket
	}
	return tickets
}

func writeAsJSONFile(data interface{}, filename string) {
	file, err := os.Create(filename)
	if err != nil {
		logger.Error("Error opening file", zap.Error(err), zap.String("filename", filename))
		return
	}
	defer file.Close()

	// Write the data
	output, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		logger.Error("Error marshalling data", zap.Error(err))
		return
	}

	file.Write(output)
}
