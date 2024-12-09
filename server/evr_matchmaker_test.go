package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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
)

func createTestMatchmakerWithOverride(t fatalable, logger *zap.Logger, tickerActive bool, messageCallback func(presences []*PresenceID, envelope *rtapi.Envelope), overrideFn RuntimeMatchmakerOverrideFunction) (*LocalMatchmaker, func() error, error) {
	cfg := NewConfig(logger)
	cfg.Database.Addresses = []string{"postgres:postgres@localhost:5432/nakama"}
	cfg.Matchmaker.IntervalSec = 1
	cfg.Matchmaker.MaxIntervals = 2
	cfg.Matchmaker.RevPrecision = true
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
		&RatedEntry{Entry: &MatchmakerEntry{Properties: map[string]interface{}{"priority_threshold": float64(time.Now().UTC().Add(-10 * time.Minute).Unix())}, Ticket: "ticket1"}},
	}
	team2 := RatedEntryTeam{
		&RatedEntry{Entry: &MatchmakerEntry{Properties: map[string]interface{}{}}},
	}
	team3 := RatedEntryTeam{
		&RatedEntry{Entry: &MatchmakerEntry{Properties: map[string]interface{}{"priority_threshold": float64(time.Now().UTC().Add(-1 * time.Minute).Unix())}, Ticket: "ticket3"}},
	}
	team4 := RatedEntryTeam{
		&RatedEntry{Entry: &MatchmakerEntry{Properties: map[string]interface{}{"priority_threshold": float64(time.Now().UTC().Add(-2 * time.Minute).Unix())}, Ticket: "ticket4"}},
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
	candidatesFilename := "../_matches/m4.json"
	// Load the candidate data from the json file
	file, err := os.Open(candidatesFilename)
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

	t.Logf("candidates: %d", len(data.Candidates))

	sbmm := NewSkillBasedMatchmaker()

	runtimeCombinations := make([][]runtime.MatchmakerEntry, len(data.Candidates))
	for i, combination := range data.Candidates {
		runtimeEntry := make([]runtime.MatchmakerEntry, len(combination))
		for j, entry := range combination {
			runtimeEntry[j] = runtime.MatchmakerEntry(entry)
		}
		runtimeCombinations[i] = runtimeEntry
	}

	deduped := sbmm.filterDuplicates(runtimeCombinations)

	players := make(map[string]struct{}, 0)
	for _, combination := range runtimeCombinations {
		for _, entry := range combination {
			players[entry.GetPresence().GetUsername()] = struct{}{}
		}
	}

	t.Logf("players: %d, original: %d, filtered: %d", len(players), len(data.Candidates), deduped)
	t.Errorf("autofail")

}

func TestCharacterizationMatchmaker1v1(t *testing.T) {

	candidatesFilename := "../_matches/m5.json"
	// Load the candidate data from the json file
	file, err := os.Open(candidatesFilename)
	if err != nil {
		t.Error("Error opening file")
	}
	defer file.Close()
	consoleLogger := loggerForTest(t)

	useOverride := true

	matchesSeen := make(map[string]*rtapi.MatchmakerMatched)
	mu := sync.Mutex{}
	var matchmaker *LocalMatchmaker
	var cleanup func() error

	if useOverride {
		matchmaker, cleanup, err = createTestMatchmakerWithOverride(t, consoleLogger, true, func(presences []*PresenceID, envelope *rtapi.Envelope) {
			id := envelope.GetMatchmakerMatched().GetToken()
			t.Logf("Matched: %s", id)
			mu.Lock()
			defer mu.Unlock()
			matchesSeen[id] = envelope.GetMatchmakerMatched()
		},
			evrMatchmakerOverrideFn,
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
	if err != nil {
		t.Fatalf("error creating test matchmaker: %v", err)
	}

	defer cleanup()

	var data CandidateData

	// read in the file
	decoder := json.NewDecoder(file)
	err = decoder.Decode(&data)
	if err != nil {
		t.Errorf("Error decoding file: %v", err)
	}

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

	t.Logf("Entry count: %d", len(entries))

	minCount := 1
	maxCount := 8
	countMultiple := 2
	_ = maxCount
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

	ticketCount := 0

	newestSubmission := 0.0

	for _, entry := range entries {
		if t, ok := entry.NumericProperties["submission_time"]; ok {
			if t > newestSubmission {
				newestSubmission = t
			}
		}
	}
	offset := float64(time.Now().UTC().Unix()) - newestSubmission

	// Offset all the submission times and thresholds so that the newest is the current itme
	for _, entry := range entries {
		if t, ok := entry.NumericProperties["submission_time"]; ok {
			entry.NumericProperties["submission_time"] = t + offset
		}
		if t, ok := entry.NumericProperties["priority_threshold"]; ok {
			entry.NumericProperties["priority_threshold"] = t + offset
		}
	}

	ticketCounts := make(map[string]int)

	for _, entry := range entries {
		query := entry.StringProperties["query"]
		_, _, err := matchmaker.Add(
			context.Background(),
			[]*MatchmakerPresence{entry.Presence},
			entry.GetPresence().GetSessionId(),
			entry.PartyId,
			query,
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
		// add the parties using the other itnerface
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
		for _, p := range match.GetUsers() {
			playerName := fmt.Sprintf("%s:%s", partyMap[match.GetUsers()[0].PartyId], p.Presence.GetUsername())
			playerIds = append(playerIds, playerName)
		}
		t.Logf("  Match %d: %s", count, strings.Join(playerIds, ", "))
		count++
	}
	// count the total matches

	// assert that 2 are notified of a match
	if len(matchesSeen) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(matchesSeen))
	}

	// assert that session1 is one of the ones notified
	if _, ok := matchesSeen[entries[0].Presence.SessionId]; !ok {
		t.Errorf("expected session1 to match, it didn't %#v", matchesSeen)
	}
	// cannot assert session2 or session3, one of them will match, but it
	// cannot be assured which one
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

// Special function used for testing the matchmaker
func evrMatchmakerOverrideFn(ctx context.Context, candidateMatches [][]*MatchmakerEntry) (matches [][]*MatchmakerEntry) {

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
	filteredCandidates, returnedEntries, _ := sbmm.processPotentialMatches(runtimeCombinations)
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
