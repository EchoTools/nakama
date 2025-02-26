package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/google/go-cmp/cmp"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/intinig/go-openskill/types"
	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protojson"
	"gopkg.in/yaml.v3"
)

func createTestMatchmakerWithOverride(t fatalable, logger *zap.Logger, tickerActive bool, messageCallback func(presences []*PresenceID, envelope *rtapi.Envelope), overrideFn RuntimeMatchmakerOverrideFunction) (*LocalMatchmaker, func() error, error) {
	cfg := NewConfig(logger)
	cfg.Database.Addresses = []string{"postgres:postgres@localhost:5432/nakama"}
	cfg.Matchmaker.IntervalSec = 60
	cfg.Matchmaker.MaxIntervals = 8
	cfg.Matchmaker.RevPrecision = false
	cfg.Matchmaker.MaxTickets = 5
	// configure a path runtime can use (it will mkdir this, so it must be writable)
	var err error
	cfg.Runtime.Path, err = os.MkdirTemp("", "nakama-matchmaker-test")
	if err != nil {
		t.Fatal(err)
	}

	messageRouter := &testMessageRouter{
		sendToPresence: messageCallback,
	}
	sessionRegistry := &testSessionRegistry{}
	tracker := &testTracker{}
	metrics := &testMetrics{}

	jsonpbMarshaler := &protojson.MarshalOptions{
		UseEnumNumbers:  true,
		EmitUnpopulated: false,
		Indent:          "",
		UseProtoNames:   true,
	}
	jsonpbUnmarshaler := &protojson.UnmarshalOptions{
		DiscardUnknown: false,
	}

	matchRegistry, runtimeMatchCreateFunc, err := createTestMatchRegistry(t, logger)
	if err != nil {
		t.Fatalf("error creating test match registry: %v", err)
	}

	rt, _, err := NewRuntime(context.Background(), logger, logger, nil, jsonpbMarshaler, jsonpbUnmarshaler, cfg, "", nil, nil, nil, nil, sessionRegistry, nil, nil, nil, tracker, metrics, nil, messageRouter, storageIdx, nil)
	if err != nil {
		t.Fatal(err)
	}

	rt.matchmakerOverrideFunction = overrideFn

	// simulate a matchmaker match function
	rt.matchmakerMatchedFunction = func(ctx context.Context, entries []*MatchmakerEntry) (string, bool, error) {
		if len(entries) != 2 {
			return "", false, nil
		}

		if !isModeAuthoritative(entries[0].Properties) {
			return "", false, nil
		}

		if !isModeAuthoritative(entries[1].Properties) {
			return "", false, nil
		}

		res, err := matchRegistry.CreateMatch(context.Background(),
			runtimeMatchCreateFunc, "match", map[string]interface{}{})
		if err != nil {
			t.Fatal(err)
		}
		return res, true, nil
	}

	matchMaker := NewLocalBenchMatchmaker(logger, logger, cfg, messageRouter, metrics, rt, tickerActive)

	return matchMaker.(*LocalMatchmaker), func() error {
		matchMaker.Stop()
		matchRegistry.Stop(0)
		return os.RemoveAll(cfg.Runtime.Path)
	}, nil
}

// Special function used for testing the matchmaker
func testEvrMatchmakerOverrideFn(ctx context.Context, candidateMatches [][]*MatchmakerEntry) (matches [][]*MatchmakerEntry) {

	log.Printf("Override function called with %d candidate matches", len(candidateMatches))
	runtimeCombinations := make([][]runtime.MatchmakerEntry, len(candidateMatches))
	for i, combination := range candidateMatches {
		runtimeEntry := make([]runtime.MatchmakerEntry, len(combination))
		for j, entry := range combination {
			runtimeEntry[j] = runtime.MatchmakerEntry(entry)
		}
		runtimeCombinations[i] = runtimeEntry
	}

	sbmm := NewSkillBasedMatchmaker()

	// Get all the tickets
	tickets := make(map[string]struct{}, 0)
	for _, combination := range runtimeCombinations {
		for _, entry := range combination {
			tickets[entry.GetTicket()] = struct{}{}
		}
	}
	log.Printf("Processing %d tickets", len(tickets))

	startTime := time.Now()
	filteredCandidates, returnedEntries, _ := sbmm.processPotentialMatches(runtimeCombinations)
	log.Printf("Processing %d candidate matches in %s", len(runtimeCombinations), time.Since(startTime))
	_ = filteredCandidates
	combinations := make([][]*MatchmakerEntry, len(returnedEntries))
	for i, combination := range returnedEntries {
		entries := make([]*MatchmakerEntry, len(combination))
		for j, entry := range combination {
			e, _ := entry.(*MatchmakerEntry)
			entries[j] = e
		}
		combinations[i] = entries
	}
	return combinations

}

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

			if count := m.filterWithinMaxRTT(tt.candidates); cmp.Diff(tt.want, tt.candidates) != "" {
				t.Errorf("hasEligibleServers() = %d: (want/got) %s", count, cmp.Diff(tt.want, tt.candidates))
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
			gotCount := m.filterOddSizedTeams(tt.candidates)
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

	_ = m.filterWithinMaxRTT(candidates)

	// Remove odd sized teams
	_ = m.filterOddSizedTeams(candidates)

	// Create a list of balanced matches with predictions
	predictions := m.predictOutcomes(candidates)

	m.sortByDraw(predictions)

	writeAsJSONFile(predictions, "/tmp/predictions.json")
	m.sortLimitRankSpread(predictions, 0.10, 0.5)
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
		&RatedEntry{Entry: &MatchmakerEntry{Properties: map[string]interface{}{"priority_threshold": time.Now().UTC().Add(-10 * time.Minute).Unix()}, Ticket: "ticket1"}},
	}
	team2 := RatedEntryTeam{
		&RatedEntry{Entry: &MatchmakerEntry{Properties: map[string]interface{}{}}},
	}
	team3 := RatedEntryTeam{
		&RatedEntry{Entry: &MatchmakerEntry{Properties: map[string]interface{}{"priority_threshold": time.Now().UTC().Add(-1 * time.Minute).Format(time.RFC3339)}, Ticket: "ticket3"}},
	}
	team4 := RatedEntryTeam{
		&RatedEntry{Entry: &MatchmakerEntry{Properties: map[string]interface{}{"priority_threshold": time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339)}, Ticket: "ticket4"}},
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
	_, returnedEntries, _ := sbmm.processPotentialMatches(runtimeCombinations)
	t.Logf("Matched %d candidate matches in %s", len(returnedEntries), time.Since(startTime))

	t.Errorf("autofail")

}

func TestCharacterizationMatchmaker1v1(t *testing.T) {

	EvrRuntimeModuleFns = nil

	// read the yml config file

	limitEntries := 32
	downloadLiveData := false
	saveCopy := true
	candidatesFilenames := []string{
		"../_matches/m5.json",
		"../_matches/m6.json",
		"../_matches/m7.json",
		"../_matches/m8.json",
		"../_matches/m9.json",
		"../_matches/live-2024-12-09-22-55-03.json",
		"../_matches/live-2024-12-09-21-54-20.json",
		"../_matches/live-2024-12-09-23-23-56.json",
		"../_matches/live-2024-12-09-23-16-44.json",
	}

	reconstruct := true
	useOverrideFn := true
	minCount := 2
	maxCount := 8
	countMultiple := 2

	var reader io.ReadCloser
	var err error
	var data CandidateData

	if downloadLiveData {

		cfg := make(map[string]interface{})
		file, err := os.ReadFile("../local.yml")
		if err != nil {
			t.Fatalf("Error reading yaml file: %v", err)
		}
		err = yaml.Unmarshal(file, &cfg)
		if err != nil {
			t.Fatalf("Error unmarshalling yaml file: %v", err)
		}
		httpKey := cfg["runtime"].(map[string]interface{})["http_key"].(string)
		// Download the match data from the server

		url := `https://g.echovrce.com/v2/rpc/matchmaker/candidates?unwrap&http_key=` + httpKey

		resp, err := http.Get(url)
		if err != nil {
			t.Fatalf("Error fetching match data: %v", err)
		}

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Unexpected status code: %d", resp.StatusCode)
		}

		jsonBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("Error reading file: %v", err)
		}
		resp.Body.Close()
		if saveCopy {
			// Create a timestamp for the file
			timestamp := time.Now().Format("2006-01-02-15-04-05")
			fn := fmt.Sprintf("../_matches/live-%s.json", timestamp)
			err = os.WriteFile(fn, jsonBytes, 0644)
			t.Logf("Saved %d bytes of live data to %s", len(jsonBytes), fn)
		}

		// read in the file
		err = json.Unmarshal(jsonBytes, &data)
		if err != nil {
			t.Errorf("Error decoding file: %v", err)
		}

	} else {

		for _, candidatesFilename := range candidatesFilenames {
			d := CandidateData{}
			// Load the candidate data from the json file
			reader, err = os.Open(candidatesFilename)
			if err != nil {
				t.Error("Error opening file")
			}

			// read in the file
			decoder := json.NewDecoder(reader)
			err = decoder.Decode(&d)
			if err != nil {
				t.Errorf("Error decoding file: %v", err)
			}

			data.Candidates = append(data.Candidates, d.Candidates...)
			data.Matches = append(data.Matches, d.Matches...)
		}

	}

	consoleLogger := loggerForTest(t)
	matchesSeen := make(map[string]*rtapi.MatchmakerMatched)
	mu := sync.Mutex{}
	var matchmaker *LocalMatchmaker
	var cleanup func() error

	if useOverrideFn {
		matchmaker, cleanup, err = createTestMatchmakerWithOverride(t, consoleLogger, true, func(presences []*PresenceID, envelope *rtapi.Envelope) {
			id := envelope.GetMatchmakerMatched().GetToken()

			mu.Lock()
			defer mu.Unlock()
			matchesSeen[id] = envelope.GetMatchmakerMatched()
		},
			testEvrMatchmakerOverrideFn,
		)
	} else {

		matchmaker, cleanup, err = createTestMatchmaker(t, consoleLogger, true, func(presences []*PresenceID, envelope *rtapi.Envelope) {

			mu.Lock()
			defer mu.Unlock()
			if len(presences) == 1 {
				matchesSeen[presences[0].SessionID.String()] = envelope.GetMatchmakerMatched()
			}
		})
	}

	defer cleanup()

	// Create a list of all players from all match candidates
	candidateSizes := make(map[int]int)

	entries := make([]*MatchmakerEntry, 0, len(data.Candidates))
	for _, c := range data.Candidates {
		candidateSizes[len(c)]++
		entries = append(entries, c...)
	}

	t.Logf("Candidate sizes: %v", candidateSizes)

	// ensure that all players are unique
	seen := make(map[string]struct{})
	for i := 0; i < len(entries); i++ {
		if _, ok := seen[entries[i].Presence.SessionId]; ok {
			entries = append(entries[:i], entries[i+1:]...)
			i--
		} else {
			seen[entries[i].Presence.SessionId] = struct{}{}
			entries[i].StringProperties, entries[i].NumericProperties = splitProperties(entries[i].Properties)
			entries[i].Presence.SessionID = uuid.FromStringOrNil(entries[i].Presence.SessionId)
		}
	}

	if reconstruct {
		// Reconstruct the entry (to get the latest matchmaking query and props)
		for i, entry := range entries {

			entries[i] = newMatchmakingEntryFromExisting(entry, minCount, maxCount, countMultiple)
			if err != nil {
				t.Fatalf("Error creating entry: %v", err)
			}

		}
	}

	if limitEntries > 0 && len(entries) > limitEntries {
		entries = entries[:limitEntries]
	}

	t.Logf("Entry count: %d", len(entries))

	playerNames := make([]string, 0, len(entries))
	for _, entry := range entries {
		playerNames = append(playerNames, entry.Presence.Username)
	}

	t.Logf("Players: %v", strings.Join(playerNames, ", "))

	// Find parties in the entries
	parties := make(map[string][]*MatchmakerEntry)
	for i := 0; i < len(entries); i++ {
		if entries[i].PartyId != "" {
			if _, ok := parties[entries[i].PartyId]; !ok {
				parties[entries[i].PartyId] = make([]*MatchmakerEntry, 0)
			}
			parties[entries[i].PartyId] = append(parties[entries[i].PartyId], entries[i])

			// Remove the party from the entries
			entries = append(entries[:i], entries[i+1:]...)
			i--
		}
	}
	t.Logf("Found %d parties and %d solo players", len(parties), len(entries))

	for _, entry := range entries {
		// Output the ticket query, and how long they have been waiting
		t.Logf("Waiting: %v, Ticket: %s, Query: %s, ", time.Now().UTC().Unix()-int64(entry.CreateTime), entry.Presence.SessionId, entry.StringProperties["query"])
	}

	ticketCount := 0

	newestSubmission := 0.0

	for _, entry := range entries {
		if t, ok := entry.NumericProperties["submission_time"]; ok {
			if t > newestSubmission {
				newestSubmission = t
			}
		}
	}

	ticketCounts := make(map[string]int)

	// If the /tmp/unmatched.json exists, load it as the entries

	for _, entry := range entries {

		_, _, err = matchmaker.Add(
			context.Background(),
			[]*MatchmakerPresence{entry.Presence},
			entry.GetPresence().GetSessionId(),
			entry.PartyId,
			entry.StringProperties["query"],
			minCount,
			maxCount,
			countMultiple,
			entry.StringProperties,
			entry.NumericProperties,
		)
		if err != nil {
			// print out ticket counts
			for k, v := range ticketCounts {
				t.Logf("Ticket count for %s: %d", k, v)
			}
			t.Fatalf("error matchmaker add (ticket #%d): %v", ticketCount+1, err)
		}
		ticketCounts[entry.Presence.SessionId]++
		ticketCount++
	}

	for IDStr, members := range parties {
		// add the parties using the other interface
		// Prepare the list of presences that will go into the matchmaker as part of the party.
		presences := make([]*MatchmakerPresence, 0, len(members))
		for _, member := range members {
			memberUserPresence := member.Presence
			presences = append(presences, &MatchmakerPresence{
				UserId:    memberUserPresence.UserId,
				SessionId: memberUserPresence.SessionId,
				Username:  memberUserPresence.Username,
				Node:      member.Presence.Node,
				SessionID: member.Presence.SessionID,
			})
			if member.Presence.SessionID == members[0].Presence.SessionID && members[0].Presence.Node == member.Presence.Node {
				continue
			}
		}
		query := members[0].StringProperties["query"]
		_, _, err := matchmaker.Add(context.Background(), presences, "", IDStr, query, minCount, maxCount, countMultiple, members[0].StringProperties, members[0].NumericProperties)
		if err != nil {
			t.Fatalf("error matchmaker add: %v", err)
		}
		ticketCount++

	}
	t.Logf("Entered %d tickets", ticketCount)

	startTime := time.Now()
	matchmaker.Process()
	t.Logf("Matchmaker process time: %v", time.Since(startTime))

	count := 0

	playerNamesByMatch := make([]string, 0, len(matchesSeen))

	matchedPlayersMap := make(map[string]struct{}, 0)
	matchedPlayers := make([]string, 0, len(matchedPlayersMap))
	// output the matches that were created
	for _, match := range matchesSeen {

		// Create a map of partyID's to alpha numeric characters
		partyMap := make(map[string]string)
		partyMap[""] = "-"
		for _, entry := range entries {
			if _, ok := partyMap[entry.PartyId]; ok {
				continue
			}
			partyMap[entry.PartyId] = string('A' + rune(len(partyMap)))
		}

		// get the players in the match
		playerIds := make([]string, 0, len(match.GetUsers()))
		playerUsernames := make([]string, 0, len(match.GetUsers()))
		for _, p := range match.GetUsers() {
			matchedPlayersMap[p.Presence.GetUsername()] = struct{}{}
			matchedPlayers = append(matchedPlayers, p.Presence.GetUsername())
			playerName := fmt.Sprintf("%s:%s", partyMap[match.GetUsers()[0].PartyId], p.Presence.GetUsername())
			playerIds = append(playerIds, playerName)

			playerUsernames = append(playerUsernames, p.Presence.GetUsername())
		}

		slices.Sort(playerUsernames)
		playerNamesByMatch = append(playerNamesByMatch, strings.Join(playerUsernames, ", "))

		t.Logf("  Match %d: %s", count, strings.Join(playerIds, ", "))
		count++
	}
	// count the total matches
	t.Logf("Total matches: %d", count)

	unmatched := make([]*MatchmakerEntry, 0, len(entries))

	for _, entry := range entries {
		if _, ok := matchedPlayersMap[entry.Presence.Username]; !ok {
			unmatched = append(unmatched, entry)

		}
	}

	t.Logf("Unmatched count: %d", len(unmatched))
	for _, e := range unmatched {
		entryJson, _ := json.MarshalIndent(e, "", "  ")
		t.Logf("Unmatched: %s", string(entryJson))
	}
	// Compare the player lists of the data.Matches with the matchesSeen

	livePlayerNamesByMatch := make([]string, 0, len(data.Matches))
	for _, match := range data.Matches {

		// Create a map of ticketID's to alpha numeric characters

		playerUsernames := make([]string, 0, len(match))
		for _, p := range match {
			playerUsernames = append(playerUsernames, p.Presence.Username)
		}

		slices.Sort(playerUsernames)

		livePlayerNamesByMatch = append(livePlayerNamesByMatch, strings.Join(playerUsernames, ", "))
	}

	slices.Sort(playerNamesByMatch)
	slices.Sort(livePlayerNamesByMatch)

	// compare the player names
	if cmp.Diff(playerNamesByMatch, livePlayerNamesByMatch) != "" {
		t.Errorf("Player names mismatch")
	}
	t.Errorf("autofail")
}

func TestAssembleUniqueMatches(t *testing.T) {
	tests := []struct {
		name         string
		ratedMatches []PredictedMatch
		want         [][]runtime.MatchmakerEntry
	}{
		{
			name: "No duplicates",
			ratedMatches: []PredictedMatch{
				{
					Team1: RatedEntryTeam{
						&RatedEntry{Entry: &MatchmakerEntry{Presence: &MatchmakerPresence{SessionId: "1"}}},
					},
					Team2: RatedEntryTeam{
						&RatedEntry{Entry: &MatchmakerEntry{Presence: &MatchmakerPresence{SessionId: "2"}}},
					},
				},
				{
					Team1: RatedEntryTeam{
						&RatedEntry{Entry: &MatchmakerEntry{Presence: &MatchmakerPresence{SessionId: "3"}}},
					},
					Team2: RatedEntryTeam{
						&RatedEntry{Entry: &MatchmakerEntry{Presence: &MatchmakerPresence{SessionId: "4"}}},
					},
				},
			},
			want: [][]runtime.MatchmakerEntry{
				{
					&MatchmakerEntry{Presence: &MatchmakerPresence{SessionId: "1"}},
					&MatchmakerEntry{Presence: &MatchmakerPresence{SessionId: "2"}},
				},
				{
					&MatchmakerEntry{Presence: &MatchmakerPresence{SessionId: "3"}},
					&MatchmakerEntry{Presence: &MatchmakerPresence{SessionId: "4"}},
				},
			},
		},
		{
			name: "With duplicates",
			ratedMatches: []PredictedMatch{
				{
					Team1: RatedEntryTeam{
						&RatedEntry{Entry: &MatchmakerEntry{Presence: &MatchmakerPresence{SessionId: "1"}}},
					},
					Team2: RatedEntryTeam{
						&RatedEntry{Entry: &MatchmakerEntry{Presence: &MatchmakerPresence{SessionId: "2"}}},
					},
				},
				{
					Team1: RatedEntryTeam{
						&RatedEntry{Entry: &MatchmakerEntry{Presence: &MatchmakerPresence{SessionId: "1"}}},
					},
					Team2: RatedEntryTeam{
						&RatedEntry{Entry: &MatchmakerEntry{Presence: &MatchmakerPresence{SessionId: "3"}}},
					},
				},
			},
			want: [][]runtime.MatchmakerEntry{
				{
					&MatchmakerEntry{Presence: &MatchmakerPresence{SessionId: "1"}},
					&MatchmakerEntry{Presence: &MatchmakerPresence{SessionId: "2"}},
				},
			},
		},
		{
			name: "All duplicates",
			ratedMatches: []PredictedMatch{
				{
					Team1: RatedEntryTeam{
						&RatedEntry{Entry: &MatchmakerEntry{Presence: &MatchmakerPresence{SessionId: "1"}}},
					},
					Team2: RatedEntryTeam{
						&RatedEntry{Entry: &MatchmakerEntry{Presence: &MatchmakerPresence{SessionId: "2"}}},
					},
				},
				{
					Team1: RatedEntryTeam{
						&RatedEntry{Entry: &MatchmakerEntry{Presence: &MatchmakerPresence{SessionId: "1"}}},
					},
					Team2: RatedEntryTeam{
						&RatedEntry{Entry: &MatchmakerEntry{Presence: &MatchmakerPresence{SessionId: "2"}}},
					},
				},
			},
			want: [][]runtime.MatchmakerEntry{
				{
					&MatchmakerEntry{Presence: &MatchmakerPresence{SessionId: "1"}},
					&MatchmakerEntry{Presence: &MatchmakerPresence{SessionId: "2"}},
				},
			},
		},
		{
			name:         "Empty rated matches",
			ratedMatches: []PredictedMatch{},
			want:         [][]runtime.MatchmakerEntry{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewSkillBasedMatchmaker()
			got := m.assembleUniqueMatches(tt.ratedMatches)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("assembleUniqueMatches() = %v, want %v", got, tt.want)
			}
		})
	}
}

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

func splitProperties(props map[string]interface{}) (map[string]string, map[string]float64) {
	// Split the properties into string and numeric
	stringProperties := make(map[string]string)
	numericProperties := make(map[string]float64)
	for k, v := range props {
		switch v := v.(type) {
		case string:
			stringProperties[k] = v
		case float64:
			numericProperties[k] = v
		}
	}
	return stringProperties, numericProperties
}

func newMatchmakingEntryFromExisting(entry *MatchmakerEntry, minCount, maxCount, countMultiple int) *MatchmakerEntry {

	params := LobbySessionParameters{}

	entry.StringProperties = make(map[string]string)
	entry.NumericProperties = make(map[string]float64)

	for k, v := range entry.Properties {
		switch v := v.(type) {
		case string:
			entry.StringProperties[k] = v
		case float64:
			entry.NumericProperties[k] = v
		}
	}
	params.FromMatchmakerEntry(entry)

	if params.RankPercentileMaxDelta == 0 {
		params.RankPercentileMaxDelta = 0.3
	}

	ticketParams := MatchmakingTicketParameters{
		MinCount:                   minCount,
		MaxCount:                   maxCount,
		CountMultiple:              countMultiple,
		IncludeRankRange:           true,
		IncludeEarlyQuitPenalty:    true,
		IncludeRequireCommonServer: true,
	}

	_, stringProps, numericProps := params.MatchmakingParameters(&ticketParams)

	newEntry := &MatchmakerEntry{
		Ticket:     entry.Ticket,
		Presence:   entry.Presence,
		PartyId:    entry.PartyId,
		Properties: make(map[string]interface{}),
		CreateTime: entry.CreateTime,

		StringProperties:  stringProps,
		NumericProperties: numericProps,
	}

	entry.Properties = make(map[string]interface{})
	for k, v := range stringProps {
		newEntry.Properties[k] = v
	}

	for k, v := range numericProps {
		newEntry.Properties[k] = v
	}

	return newEntry
}
