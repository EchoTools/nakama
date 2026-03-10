package server

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/heroiclabs/nakama-common/runtime"
)

func TestIntegrationProcessorHappyPath(t *testing.T) {
	logger := loggerForTest(t)
	matchmaker, cleanup := createProcessorTestMatchmaker(t, logger)
	defer cleanup()

	wireEvrProcessor(matchmaker, NewRuntimeGoLogger(logger))

	for i := range 8 {
		addIntegrationProcessorTicket(t, matchmaker, i, 40)
	}

	matched, expired := runProcessorCycle(matchmaker)
	if len(expired) != 0 {
		t.Fatalf("expected no expired tickets, got %d", len(expired))
	}

	gotSizes := matchedGroupSizes(matched)
	if diff := cmp.Diff([]int{8}, gotSizes); diff != "" {
		t.Fatalf("unexpected match sizes (-want +got):\n%s", diff)
	}
}

func TestIntegrationProcessorMultipleMatches(t *testing.T) {
	logger := loggerForTest(t)
	matchmaker, cleanup := createProcessorTestMatchmaker(t, logger)
	defer cleanup()

	wireEvrProcessor(matchmaker, NewRuntimeGoLogger(logger))

	for i := range 16 {
		addIntegrationProcessorTicket(t, matchmaker, i, 40)
	}

	matched, expired := runProcessorCycle(matchmaker)
	if len(expired) != 0 {
		t.Fatalf("expected no expired tickets, got %d", len(expired))
	}

	gotSizes := matchedGroupSizes(matched)
	if diff := cmp.Diff([]int{8, 8}, gotSizes); diff != "" {
		t.Fatalf("unexpected match sizes (-want +got):\n%s", diff)
	}
}

func TestIntegrationProcessorLargePool(t *testing.T) {
	logger := loggerForTest(t)
	matchmaker, cleanup := createProcessorTestMatchmaker(t, logger)
	defer cleanup()

	wireEvrProcessor(matchmaker, NewRuntimeGoLogger(logger))

	for i := range 100 {
		addIntegrationProcessorTicket(t, matchmaker, i, 40)
	}

	start := time.Now()
	matched, expired := runProcessorCycle(matchmaker)
	duration := time.Since(start)

	if len(expired) != 0 {
		t.Fatalf("expected no expired tickets, got %d", len(expired))
	}
	if duration >= 5*time.Second {
		t.Fatalf("expected processing to finish in <5s, took %s", duration)
	}

	gotSizes := matchedGroupSizes(matched)
	if diff := cmp.Diff([]int{4, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8}, gotSizes); diff != "" {
		t.Fatalf("unexpected match sizes (-want +got):\n%s", diff)
	}
}

func TestIntegrationProcessorRTTFiltering(t *testing.T) {
	logger := loggerForTest(t)
	matchmaker, cleanup := createProcessorTestMatchmaker(t, logger)
	defer cleanup()

	wireEvrProcessor(matchmaker, NewRuntimeGoLogger(logger))

	lowRTTTickets := make(map[string]struct{}, 24)
	for i := range 24 {
		ticket := addIntegrationProcessorTicket(t, matchmaker, i, 40)
		lowRTTTickets[ticket] = struct{}{}
	}
	for i := range 2 {
		addIntegrationProcessorTicket(t, matchmaker, i+1000, 600)
	}

	matched, expired := runProcessorCycle(matchmaker)
	if len(expired) != 0 {
		t.Fatalf("expected no expired tickets, got %d", len(expired))
	}

	groups := normalizeMatchedTickets(matched)
	if len(groups) == 0 {
		t.Fatalf("expected at least one low-RTT match")
	}
	for _, group := range groups {
		for _, ticket := range group {
			if _, ok := lowRTTTickets[ticket]; !ok {
				t.Fatalf("matched high-RTT ticket %q unexpectedly", ticket)
			}
		}
	}
}

func TestIntegrationProcessorNoMatchBelowMin(t *testing.T) {
	logger := loggerForTest(t)
	matchmaker, cleanup := createProcessorTestMatchmaker(t, logger)
	defer cleanup()

	wireEvrProcessor(matchmaker, NewRuntimeGoLogger(logger))

	addIntegrationProcessorTicket(t, matchmaker, 0, 40)

	matched, expired := runProcessorCycle(matchmaker)
	if len(expired) != 0 {
		t.Fatalf("expected no expired tickets, got %d", len(expired))
	}
	if len(matched) != 0 {
		t.Fatalf("expected no matches with only one entry, got %d", len(matched))
	}
}

func wireEvrProcessor(matchmaker *LocalMatchmaker, logger runtime.Logger) {
	skillMatchmaker := NewSkillBasedMatchmaker()

	matchmaker.runtime.matchmakerProcessorFunction = func(ctx context.Context, entries []*MatchmakerEntry) [][]*MatchmakerEntry {
		runtimeEntries := make([]runtime.MatchmakerEntry, len(entries))
		for i, entry := range entries {
			runtimeEntries[i] = runtime.MatchmakerEntry(entry)
		}

		matches := skillMatchmaker.EvrMatchmakerFn(ctx, logger, nil, nil, runtimeEntries)
		result := make([][]*MatchmakerEntry, 0, len(matches))
		for _, group := range matches {
			converted := make([]*MatchmakerEntry, 0, len(group))
			for _, entry := range group {
				internal, ok := entry.(*MatchmakerEntry)
				if !ok || internal == nil {
					continue
				}
				converted = append(converted, internal)
			}
			if len(converted) > 0 {
				result = append(result, converted)
			}
		}

		return result
	}
}

func addIntegrationProcessorTicket(t *testing.T, matchmaker *LocalMatchmaker, i int, rtt float64) string {
	t.Helper()

	sessionID := fmt.Sprintf("integration-session-%03d", i)
	ticket, _, err := matchmaker.Add(
		context.Background(),
		[]*MatchmakerPresence{{
			UserId:    sessionID,
			SessionId: sessionID,
			Username:  fmt.Sprintf("integration-user-%03d", i),
			Node:      "integration",
		}},
		sessionID,
		"",
		"*",
		2,
		100,
		2,
		map[string]string{
			"game_mode": "echo_arena",
			"group_id":  "integration-group",
		},
		map[string]float64{
			"max_count":       8,
			"count_multiple":  2,
			"max_rtt":         250,
			"rtt_test":        rtt,
			"rating_mu":       25,
			"rating_sigma":    8.33,
			"submission_time": float64(time.Now().UTC().Unix()) - float64(i),
		},
	)
	if err != nil {
		t.Fatalf("add integration ticket: %v", err)
	}

	return ticket
}

func runProcessorCycle(matchmaker *LocalMatchmaker) ([][]*MatchmakerEntry, []string) {
	activeCount, activeCopy, indexCount, indexesCopy := copyIndexesForProcessorTest(matchmaker)
	return matchmaker.processWithProcessor(activeCount, activeCopy, indexCount, indexesCopy)
}

func matchedGroupSizes(matched [][]*MatchmakerEntry) []int {
	sizes := make([]int, len(matched))
	for i, group := range matched {
		sizes[i] = len(group)
	}
	sort.Ints(sizes)
	return sizes
}

func addIntegrationProcessorTicketWithProps(t *testing.T, matchmaker *LocalMatchmaker, sessionSuffix string, extraString map[string]string, extraNumeric map[string]float64) string {
	t.Helper()
	sessionID := "intprop-" + sessionSuffix
	stringProps := map[string]string{
		"game_mode": "echo_arena",
		"group_id":  "integration-group",
	}
	for k, v := range extraString {
		stringProps[k] = v
	}
	numericProps := map[string]float64{
		"max_count":      8,
		"count_multiple": 2,
		"max_rtt":        250,
		"rtt_test":       40,
		"rating_mu":      25,
		"rating_sigma":   8.33,
		"timestamp":      float64(time.Now().UTC().Unix()),
	}
	for k, v := range extraNumeric {
		numericProps[k] = v
	}
	ticket, _, err := matchmaker.Add(
		context.Background(),
		[]*MatchmakerPresence{{
			UserId:    sessionID,
			SessionId: sessionID,
			Username:  sessionID,
			Node:      "integration",
		}},
		sessionID, "", "*", 2, 100, 2,
		stringProps, numericProps,
	)
	if err != nil {
		t.Fatalf("add ticket: %v", err)
	}
	return ticket
}

func TestIntegrationProcessorCrossModeIsolation(t *testing.T) {
	logger := loggerForTest(t)
	matchmaker, cleanup := createProcessorTestMatchmaker(t, logger)
	defer cleanup()

	wireEvrProcessor(matchmaker, NewRuntimeGoLogger(logger))

	for i := range 8 {
		addIntegrationProcessorTicketWithProps(t, matchmaker, fmt.Sprintf("arena-%02d", i), map[string]string{"game_mode": "echo_arena"}, nil)
	}
	for i := range 8 {
		addIntegrationProcessorTicketWithProps(t, matchmaker, fmt.Sprintf("combat-%02d", i), map[string]string{"game_mode": "echo_combat"}, map[string]float64{"max_count": 10})
	}

	matched, expired := runProcessorCycle(matchmaker)
	if len(expired) != 0 {
		t.Fatalf("expected no expired tickets, got %d", len(expired))
	}
	if len(matched) != 2 {
		t.Fatalf("expected exactly 2 matches, got %d", len(matched))
	}
	for _, group := range matched {
		modes := map[string]struct{}{}
		for _, entry := range group {
			if mode, ok := entry.StringProperties["game_mode"]; ok {
				modes[mode] = struct{}{}
			}
		}
		if len(modes) != 1 {
			t.Fatalf("match contains entries from multiple game modes: %v", modes)
		}
	}
}

func TestIntegrationProcessorUndersizedRejection(t *testing.T) {
	logger := loggerForTest(t)
	matchmaker, cleanup := createProcessorTestMatchmaker(t, logger)
	defer cleanup()

	wireEvrProcessor(matchmaker, NewRuntimeGoLogger(logger))

	for i := range 6 {
		addIntegrationProcessorTicketWithProps(t, matchmaker, fmt.Sprintf("reject-%02d", i), nil, map[string]float64{
			"failsafe_timeout": 300,
			"min_team_size":    4,
			"max_count":        8,
			"count_multiple":   2,
			"timestamp":        float64(time.Now().UTC().Unix()),
		})
	}

	matched, _ := runProcessorCycle(matchmaker)
	if len(matched) != 0 {
		t.Fatalf("expected no matches (undersized, failsafe not expired), got %d", len(matched))
	}
}

func TestIntegrationProcessorUndersizedWithFailsafe(t *testing.T) {
	logger := loggerForTest(t)
	matchmaker, cleanup := createProcessorTestMatchmaker(t, logger)
	defer cleanup()

	wireEvrProcessor(matchmaker, NewRuntimeGoLogger(logger))

	for i := range 6 {
		addIntegrationProcessorTicketWithProps(t, matchmaker, fmt.Sprintf("failsafe-%02d", i), nil, map[string]float64{
			"failsafe_timeout": 60,
			"min_team_size":    4,
			"timestamp":        float64(time.Now().UTC().Unix()) - 120,
		})
	}

	matched, _ := runProcessorCycle(matchmaker)
	if len(matched) != 1 {
		t.Fatalf("expected 1 match (failsafe expired), got %d", len(matched))
	}
	if len(matched[0]) != 6 {
		t.Fatalf("expected match with 6 players, got %d", len(matched[0]))
	}
}

func TestIntegrationProcessorUndersizedZeroFailsafe(t *testing.T) {
	logger := loggerForTest(t)
	matchmaker, cleanup := createProcessorTestMatchmaker(t, logger)
	defer cleanup()

	wireEvrProcessor(matchmaker, NewRuntimeGoLogger(logger))

	for i := range 6 {
		addIntegrationProcessorTicketWithProps(t, matchmaker, fmt.Sprintf("zerofail-%02d", i), nil, map[string]float64{
			"failsafe_timeout": 0,
			"min_team_size":    4,
			"timestamp":        float64(time.Now().UTC().Unix()),
		})
	}

	matched, _ := runProcessorCycle(matchmaker)
	if len(matched) != 1 {
		t.Fatalf("expected 1 match (zero failsafe = bypass), got %d", len(matched))
	}
	if len(matched[0]) != 6 {
		t.Fatalf("expected match with 6 players, got %d", len(matched[0]))
	}
}

func TestIntegrationProcessorCombatTeamSizes(t *testing.T) {
	logger := loggerForTest(t)
	matchmaker, cleanup := createProcessorTestMatchmaker(t, logger)
	defer cleanup()

	wireEvrProcessor(matchmaker, NewRuntimeGoLogger(logger))

	for i := range 6 {
		addIntegrationProcessorTicketWithProps(t, matchmaker, fmt.Sprintf("combat-exact-%02d", i), map[string]string{"game_mode": "echo_combat"}, map[string]float64{
			"failsafe_timeout": 300,
			"min_team_size":    3,
			"max_team_size":    5,
			"max_count":        10,
			"count_multiple":   2,
			"timestamp":        float64(time.Now().UTC().Unix()),
		})
	}

	matched, _ := runProcessorCycle(matchmaker)
	if len(matched) != 1 {
		t.Fatalf("expected 1 match (6 >= 3x2=6, exact minimum met), got %d", len(matched))
	}
	if len(matched[0]) != 6 {
		t.Fatalf("expected match with 6 players, got %d", len(matched[0]))
	}
}

func TestIntegrationProcessorCombatUndersized(t *testing.T) {
	logger := loggerForTest(t)
	matchmaker, cleanup := createProcessorTestMatchmaker(t, logger)
	defer cleanup()

	wireEvrProcessor(matchmaker, NewRuntimeGoLogger(logger))

	for i := range 4 {
		addIntegrationProcessorTicketWithProps(t, matchmaker, fmt.Sprintf("combat-small-%02d", i), map[string]string{"game_mode": "echo_combat"}, map[string]float64{
			"failsafe_timeout": 300,
			"min_team_size":    3,
			"max_team_size":    5,
			"max_count":        10,
			"count_multiple":   2,
			"timestamp":        float64(time.Now().UTC().Unix()),
		})
	}

	matched, _ := runProcessorCycle(matchmaker)
	if len(matched) != 0 {
		t.Fatalf("expected no matches (4 < 3x2=6, failsafe not expired), got %d", len(matched))
	}
}
