package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/intinig/go-openskill/rating"
	"github.com/intinig/go-openskill/types"
	"gopkg.in/yaml.v3"
)

func retrieveDataFromRemoteNakamaRPC[T any](uri string, dst T) error {
	cfg := make(map[string]interface{})
	file, err := os.ReadFile("../local.yml")
	if err != nil {
		return err
	}
	err = yaml.Unmarshal(file, &cfg)
	if err != nil {
		return err
	}
	httpKey := cfg["runtime"].(map[string]interface{})["http_key"].(string)
	// Download the match data from the server

	resp, err := http.Get(uri + "?unwrap&http_key=" + httpKey)
	if err != nil {
		return fmt.Errorf("Error fetching match data: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Unexpected status code: %d", resp.StatusCode)
	}

	jsonBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("Error reading file: %v", err)
	}
	resp.Body.Close()

	// read in the file
	err = json.Unmarshal(jsonBytes, dst)
	if err != nil {
		return fmt.Errorf("Error decoding file: %v", err)
	}
	return nil
}

func generateMatchmakerEntries(count int) []*MatchmakerEntry {
	groupID := uuid.Must(uuid.NewV4()).String()
	entries := make([]*MatchmakerEntry, 0, count)
	for i := range count {
		var (
			dn        = RandomDisplayName()
			sessionID = uuid.NewV5(uuid.Nil, fmt.Sprintf("%d", i))
		)
		entries = append(entries, &MatchmakerEntry{
			Ticket: uuid.NewV5(uuid.Nil, fmt.Sprintf("%d", i)).String(),
			Presence: &MatchmakerPresence{
				UserId:    uuid.NewV5(uuid.Nil, fmt.Sprintf("%d", i)).String(),
				SessionId: sessionID.String(),
				Username:  dn + "username",
				SessionID: sessionID,
			},
			Properties: map[string]any{
				"rating_mu":                 float64(20 + rand.Intn(9)),              // random between 20 and 28
				"rating_sigma":              float64(7 + rand.Intn(3)),               // random between 7 and 10
				"rank_percentile":           float64(float64(rand.Intn(8))/10 + 0.1), // random between 0.1 and 0.9
				"rank_percentile_max":       0.6,
				"rank_percentile_max_delta": 0.3,
				"rank_percentile_min":       0,
				"timestamp":                 float64(time.Now().UTC().Add(-time.Duration(rand.Intn(10)) * time.Minute).Unix()),
				"blocked_ids":               "",
				"display_name":              dn,
				"division":                  "",
				"game_mode":                 "echo_arena",
				"group_id":                  groupID,
				"priority_threshold":        "2025-03-13T18:34:54Z",
				"query":                     "+properties.game_mode:echo_arena +properties.group_id:147afc9d\\-2819\\-4197\\-926d\\-5b3f92790edc -properties.blocked_ids:/.*28e070c2\\-acb5\\-4e96\\-b53b\\-92c955edeb31.*/ -properties.rank_percentile:<0.000000 -properties.rank_percentile:>0.600000 -properties.rank_percentile_min:>0.200000 -properties.rank_percentile_max:<0.200000",
				"submission_time":           "2025-03-13T18:18:54Z",
				"version_lock":              "0x134b1272e1c4c0b7",
				"max_rtt":                   300,
				"rtt_116.203.155.106":       float64(160),
				"rtt_166.0.130.5":           float64(30),
				"rtt_166.0.130.6":           float64(30),
				"rtt_216.137.230.85":        float64(120),
				"rtt_45.92.36.210":          float64(140),
				"rtt_49.191.142.22":         float64(250),
				"rtt_5.42.134.192":          float64(140),
				"rtt_50.24.115.211":         float64(50),
				"rtt_68.235.129.88":         float64(40),
				"rtt_68.72.133.63":          float64(60),
				"rtt_69.234.156.212":        float64(50),
				"rtt_71.15.39.215":          float64(60),
				"rtt_98.117.251.89":         float64(80),
				"rtt_99.251.100.24":         float64(90),
			},
			NumericProperties: map[string]float64{},
		})

	}
	return entries
}
func generateMatchmakerCandidates(count int) [][]runtime.MatchmakerEntry {
	entries := generateMatchmakerEntries(count)
	candidates := make([][]runtime.MatchmakerEntry, 0, count*8)
	// Create all combinations of 8 players from the entries
	for i := range entries {
		for j := i + 1; j < len(entries); j++ {
			for k := j + 1; k < len(entries); k++ {
				for l := k + 1; l < len(entries); l++ {
					for m := l + 1; m < len(entries); m++ {
						for n := m + 1; n < len(entries); n++ {
							for o := n + 1; o < len(entries); o++ {
								for p := o + 1; p < len(entries); p++ {
									candidates = append(candidates, []runtime.MatchmakerEntry{
										entries[i],
										entries[j],
										entries[k],
										entries[l],
										entries[m],
										entries[n],
										entries[o],
										entries[p],
									})

								}
							}
						}
					}
				}
			}
		}
	}
	return candidates
}

func BenchmarkPredictOutcomes(b *testing.B) {

	profile := true
	if profile {
		// Create CPU profile file
		cpuProfile, _ := os.Create("/tmp/cpu.prof")
		defer cpuProfile.Close()
		pprof.StartCPUProfile(cpuProfile)
		defer pprof.StopCPUProfile()

		// Create Memory profile file
		memProfile, _ := os.Create("/tmp/mem.prof")
		defer memProfile.Close()
		defer pprof.WriteHeapProfile(memProfile)
	}
	// Create all combinations of 8 players from the entries
	candidates := generateMatchmakerCandidates(32)
	b.Logf("candidate count: %d", len(candidates))

	for b.Loop() {
		b.ReportMetric(float64(len(candidates)), "candidates")
		predictions := predictCandidateOutcomes(candidates)
		b.ReportMetric(float64(len(predictions)), "predictions")
	}
}
func TestCharacterizationMatchmaker(t *testing.T) {

	var (
		downloadLiveData = true
		stateFilename    = "../_local/matchmaker-state.json"
	)
	state := MatchmakerStateResponse{}

	// Load the candidate data from the json file
	reader, err := os.Open(stateFilename)
	if err != nil {
		t.Error("Error opening file")
	}

	// read in the file
	decoder := json.NewDecoder(reader)
	err = decoder.Decode(&state)
	if err != nil {
		t.Errorf("Error decoding file: %v", err)
	}
	if downloadLiveData {
		retrieveDataFromRemoteNakamaRPC("https://g.echovrce.com/v2/rpc/matchmaker/state", &state)

		f, err := os.Create("/tmp/matchmaker-state.json")
		if err != nil {
			t.Fatalf("Error creating file: %v", err)
		}
		defer f.Close()

		encoder := json.NewEncoder(f)
		encoder.SetIndent("", "  ")
		err = encoder.Encode(state)
		if err != nil {
			t.Fatalf("Error encoding file: %v", err)
		}
	}

	// Create a list of all players from all match candidates
	entries := make([]runtime.MatchmakerEntry, 0)
	for _, e := range state.Index {
		for _, p := range e.Presences {
			properties := make(map[string]any)

			for k, v := range e.StringProperties {
				properties[k] = v
			}
			for k, v := range e.NumericProperties {
				properties[k] = v
			}

			entries = append(entries, &MatchmakerEntry{
				Ticket:            e.Ticket,
				Presence:          p,
				Properties:        properties,
				PartyId:           e.PartyId,
				CreateTime:        e.CreatedAt,
				StringProperties:  e.StringProperties,
				NumericProperties: e.NumericProperties,
			})
		}
	}

	t.Logf("Entry count: %d", len(entries))

	// Find parties in the entries
	parties := make(map[string][]runtime.MatchmakerEntry)
	for i := range entries {
		if entries[i].GetPartyId() != "" {
			if _, ok := parties[entries[i].GetPartyId()]; !ok {
				parties[entries[i].GetPartyId()] = make([]runtime.MatchmakerEntry, 0)
			}
			parties[entries[i].GetPartyId()] = append(parties[entries[i].GetPartyId()], entries[i])
		}
	}
	t.Logf("Found %d parties and %d solo players", len(parties), len(entries))

	var (
		consoleLogger     = loggerForTest(t)
		logger            = NewRuntimeGoLogger(consoleLogger)
		sbmm              = NewSkillBasedMatchmaker()
		candidates        = allCombinations(entries, 8)
		matchedPlayersSet = make(map[string]struct{}, 0)
		count             = 0
	)

	// Create a map of partyID's to alpha numeric characters
	partyMap := make(map[string]string)
	partyMap[""] = "-"
	for _, entry := range entries {
		if _, ok := partyMap[entry.GetPartyId()]; ok {
			continue
		}
		partyMap[entry.GetPartyId()] = string('A' + rune(len(partyMap)))
	}
	playerIDMap := make(map[string]string, len(entries))
	for _, e := range entries {
		playerIDMap[e.GetPresence().GetUsername()] = fmt.Sprintf("%s:%s", partyMap[e.GetPartyId()], e.GetPresence().GetUsername())
	}

	playerIDs := make([]string, 0, len(entries))
	for _, e := range entries {
		playerIDs = append(playerIDs, playerIDMap[e.GetPresence().GetUsername()])
	}

	t.Logf("Players: %v", strings.Join(playerIDs, ", "))
	t.Logf("Candidate count: %d", len(candidates))

	matches := sbmm.EvrMatchmakerFn(context.Background(), logger, nil, nil, candidates)

	for _, match := range matches {

		// get the players in the match
		playerIds := make([]string, 0, len(match))
		for _, e := range match {
			matchedPlayersSet[e.GetPresence().GetUsername()] = struct{}{}
			playerIds = append(playerIds, playerIDMap[e.GetPresence().GetUsername()])
		}

		t.Logf("  Match %d: %s", count, strings.Join(playerIds, ", "))
		count++

		c := CandidateList(match)
		teams := make([]types.Team, 0, 2)
		teams = append(teams, c[:4].Ratings())
		teams = append(teams, c[4:].Ratings())

		ordinals := make([]float64, 0, 8)
		for _, r := range c.Ratings() {
			ordinals = append(ordinals, rating.Ordinal(r))
		}

		groups := make(map[string]CandidateList, 2)
		for _, e := range c {
			groups[e.GetTicket()] = append(groups[e.GetTicket()], e)
		}
		groupRatings := make([]types.Team, 0, 2)
		groupOrdinals := make([]float64, 0, 8)
		for _, entries := range groups {
			groupRatings = append(groupRatings, entries.Ratings())
			groupOrdinals = append(groupOrdinals, entries.TeamOrdinal())
		}

		teamRatingA := c[:4].TeamRating()
		teamRatingB := c[4:].TeamRating()

		ranks, probabilities := rating.PredictRank(groupRatings, nil)
		teamOrdinalA := rating.TeamOrdinal(teamRatingA)
		teamOrdinalB := rating.TeamOrdinal(teamRatingB)

		playerMus := make([]int, 0, 8)
		playerSigmas := make([]int, 0, 8)
		for _, r := range c.Ratings() {
			playerMus = append(playerMus, int(r.Mu))
			playerSigmas = append(playerSigmas, int(r.Sigma*100))
		}

		//t.Logf("    Team Ratings: %v", []types.TeamRating{teamRatingA, teamRatingB})
		t.Logf("    Team Ordinals: %v", []float64{teamOrdinalA, teamOrdinalB})
		t.Logf("    Player Mus: %v", playerMus)
		t.Logf("    Player Sigmas: %v", playerSigmas)
		t.Logf("    Player Ordinals: %v", ordinals)
		t.Logf("    Player Ranks: %v", ranks)
		t.Logf("    Player Rank Probabilities: %v", probabilities)
		t.Logf("    Group Ordinals: %v", groupOrdinals)
		t.Logf("    Group Ratings: %v", groupRatings)

		draw := rating.PredictDraw(teams, nil)
		t.Logf("    Draw: %v", draw)
	}
	// count the total matches
	t.Logf("Total matches: %d", count)

	unmatched := make([]runtime.MatchmakerEntry, 0, len(entries))

	for _, entry := range entries {
		if _, ok := matchedPlayersSet[entry.GetPresence().GetUsername()]; !ok {
			unmatched = append(unmatched, entry)

		}
	}

	t.Logf("Unmatched count: %d", len(unmatched))
	for _, e := range unmatched {
		entryJson, _ := json.MarshalIndent(e, "", "  ")
		t.Logf("Unmatched: %s", string(entryJson))
	}

	t.Errorf("autofail")
}

func TestHashMatchmakerEntries(t *testing.T) {
	// Mock implementation of runtime.MatchmakerEntry

	// Test cases
	tests := []struct {
		name     string
		entries  []runtime.MatchmakerEntry
		expected uint64
	}{
		{
			name:     "Empty entries",
			entries:  []runtime.MatchmakerEntry{},
			expected: 0,
		},
		{
			name: "Single entry",
			entries: []runtime.MatchmakerEntry{
				&MatchmakerEntry{Ticket: "ticket1"},
			},
			expected: HashMatchmakerEntries([]*MatchmakerEntry{{Ticket: "ticket1"}}),
		},
		{
			name: "Multiple entries, same order",
			entries: []runtime.MatchmakerEntry{
				&MatchmakerEntry{Ticket: "ticket1"},
				&MatchmakerEntry{Ticket: "ticket2"},
			},
			expected: HashMatchmakerEntries([]*MatchmakerEntry{
				{Ticket: "ticket1"},
				{Ticket: "ticket2"},
			}),
		},
		{
			name: "Multiple entries, different order",
			entries: []runtime.MatchmakerEntry{
				&MatchmakerEntry{Ticket: "ticket2"},
				&MatchmakerEntry{Ticket: "ticket1"},
			},
			expected: HashMatchmakerEntries([]*MatchmakerEntry{
				{Ticket: "ticket1"},
				{Ticket: "ticket2"},
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash := HashMatchmakerEntries(tt.entries)
			if hash != tt.expected {
				t.Logf("HashMatchmakerEntries() = %v, expected %v", hash, tt.expected)
				t.Errorf("HashMatchmakerEntries() = %v, expected %v", hash, tt.expected)
			}
		})
	}
	t.Fail()
}
