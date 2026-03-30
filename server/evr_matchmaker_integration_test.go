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

// addIntegrationPartyTicket adds a party ticket with the given size and MaxCount.
func addIntegrationPartyTicket(t *testing.T, matchmaker *LocalMatchmaker, partySize int, partyPrefix string, maxCount int) string {
	t.Helper()

	presences := make([]*MatchmakerPresence, partySize)
	for i := range partySize {
		presences[i] = &MatchmakerPresence{
			UserId:    fmt.Sprintf("%s-member-%d", partyPrefix, i),
			SessionId: fmt.Sprintf("%s-session-%d", partyPrefix, i),
			Username:  fmt.Sprintf("%s-user-%d", partyPrefix, i),
			Node:      "integration",
		}
	}
	ticket, _, err := matchmaker.Add(
		context.Background(),
		presences,
		"",
		partyPrefix+".node",
		"+properties.game_mode:echo_arena",
		2,
		maxCount,
		2,
		map[string]string{
			"game_mode": "echo_arena",
			"group_id":  "integration-group",
		},
		map[string]float64{
			"max_count":      float64(maxCount),
			"count_multiple": 2,
			"max_rtt":        250,
			"rtt_test":       40,
			"max_team_size":  4,
			"min_team_size":  4,
			"rating_mu":      25,
			"rating_sigma":   8.33,
			"timestamp":      float64(time.Now().UTC().Unix()) - 180,
		},
	)
	if err != nil {
		t.Fatalf("add party ticket (%s, size=%d): %v", partyPrefix, partySize, err)
	}
	return ticket
}

// TestIntegrationProcessorPartyStarvedByOrdering reproduces the production bug
// where a party of 4 (MaxCount=8) is starved because enough solo tickets were
// created BEFORE the party's ticket. When the party's fallback timer fires and
// replaces its ticket, the new ticket gets a fresh CreatedAt — making it
// younger than solos already in the queue. groupEntriesSequentially packs
// entries in CreatedAt order, so solos fill the first candidate, and the party
// ends up in an undersized candidate (< 8 players) that gets rejected.
func TestIntegrationProcessorPartyStarvedByOrdering(t *testing.T) {
	logger := loggerForTest(t)
	matchmaker, cleanup := createProcessorTestMatchmaker(t, logger)
	defer cleanup()

	wireEvrProcessor(matchmaker, NewRuntimeGoLogger(logger))

	// Add 9 solos FIRST — these get older CreatedAt timestamps. With map
	// iteration shifting one ticket to the end, 8 solos precede the party.
	// Those 8 fill Candidate 1. The remaining entries are:
	//   party(4) + 1 leftover solo + 1 newer solo + 1 shifted solo = 7
	//   7 % 2 = 1 → trimmed to 6 → undersized (< 8)
	for i := range 9 {
		addIntegrationProcessorTicket(t, matchmaker, i, 40)
	}

	// Add the party of 4 with MaxCount=8. It gets a newer CreatedAt than the
	// 9 solos above, simulating the fallback timer replacing the ticket.
	partyTicket := addIntegrationPartyTicket(t, matchmaker, 4, "ordering-party", 8)

	// Add only 1 solo AFTER the party. Total: 10 solos + party(4) = 14 entries.
	// Enough for one 8-player match containing the party (4 solos needed).
	// But groupEntriesSequentially's sequential packing starves the party.
	addIntegrationProcessorTicket(t, matchmaker, 100, 40)

	var partyMatched bool
	for cycle := range 3 {
		matched, _ := runProcessorCycle(matchmaker)
		for _, group := range matched {
			for _, entry := range group {
				if entry.Ticket == partyTicket {
					partyMatched = true
					if len(group) != 8 {
						t.Fatalf("cycle %d: party matched in group of %d, want 8", cycle, len(group))
					}
					break
				}
			}
		}
		if partyMatched {
			break
		}
	}

	if !partyMatched {
		t.Fatalf("party of 4 (MaxCount=8) was never matched — groupEntriesSequentially placed it in an undersized candidate due to entry ordering (5 older solos packed first)")
	}
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

// =============================================================================
// Matchmaking Wait Time: Acceptance & Regression Tests
//
// These tests document the bugs and intended behaviors for the matchmaking
// failsafe timeout system. The key mechanism:
//
//   - isUndersizedMatch (evr_matchmaker_process.go) rejects match candidates
//     with fewer players than min_team_size*2 UNLESS the oldest ticket has
//     been waiting longer than failsafe_timeout seconds.
//
//   - lobbyMatchMakeWithFallback (evr_lobby_matchmake.go) periodically replaces
//     the matchmaking ticket, reducing the failsafe_timeout property each cycle
//     so that undersized matches are allowed progressively sooner.
//
// Production evidence (from /var/tmp/nakama.log):
//   - User l0affer waited 551 seconds for echo_combat (sid 6b230492)
//   - failsafe_timeout=540 was never reduced despite fallback at 300s
//   - The fallback only changed MinCount/MaxCount, leaving failsafe_timeout
//     at 540 — isUndersizedMatch blocked the match for the full duration
// =============================================================================

// --- Bug reproduction tests ---

// TestUndersizedMatch_BlockedByHighFailsafe reproduces the core bug:
// With failsafe_timeout=300 and only 60 seconds elapsed, an undersized match
// (4 players for a min_team_size=4 game) is rejected by isUndersizedMatch.
// This is the state BEFORE the fix — the failsafe was never reduced.
func TestUndersizedMatch_BlockedByHighFailsafe(t *testing.T) {
	logger := loggerForTest(t)
	matchmaker, cleanup := createProcessorTestMatchmaker(t, logger)
	defer cleanup()
	wireEvrProcessor(matchmaker, NewRuntimeGoLogger(logger))

	ticketTime := float64(time.Now().UTC().Unix()) - 60 // 60s ago

	for i := range 4 {
		addIntegrationProcessorTicketWithProps(t, matchmaker, fmt.Sprintf("blocked-%02d", i), nil, map[string]float64{
			"failsafe_timeout": 300, // 300 >> 60 elapsed → blocked
			"min_team_size":    4,
			"max_count":        8,
			"count_multiple":   2,
			"timestamp":        ticketTime,
		})
	}

	matched, _ := runProcessorCycle(matchmaker)
	if len(matched) != 0 {
		t.Fatalf("expected 0 matches (failsafe=300 > elapsed=60), got %d", len(matched))
	}
}

// TestUndersizedMatch_AllowedWhenFailsafeReduced verifies the fix:
// After the periodic fallback reduces failsafe_timeout below the elapsed wait
// time, isUndersizedMatch allows the match. This simulates what happens when
// lobbyMatchMakeWithFallback reduces FailsafeTimeout on each cycle.
func TestUndersizedMatch_AllowedWhenFailsafeReduced(t *testing.T) {
	logger := loggerForTest(t)
	matchmaker, cleanup := createProcessorTestMatchmaker(t, logger)
	defer cleanup()
	wireEvrProcessor(matchmaker, NewRuntimeGoLogger(logger))

	ticketTime := float64(time.Now().UTC().Unix()) - 60 // 60s ago

	for i := range 4 {
		addIntegrationProcessorTicketWithProps(t, matchmaker, fmt.Sprintf("allowed-%02d", i), nil, map[string]float64{
			"failsafe_timeout": 30, // 30 < 60 elapsed → allowed
			"min_team_size":    4,
			"max_count":        8,
			"count_multiple":   2,
			"timestamp":        ticketTime,
		})
	}

	matched, _ := runProcessorCycle(matchmaker)
	if len(matched) != 1 {
		t.Fatalf("expected 1 match (failsafe=30 <= elapsed=60), got %d", len(matched))
	}
	if len(matched[0]) != 4 {
		t.Fatalf("expected 4 players in undersized match, got %d", len(matched[0]))
	}
}

// TestUndersizedMatch_AllowedWhenFailsafeZero verifies that failsafe_timeout=0
// (the end state of progressive reduction) bypasses the undersized check
// entirely, regardless of elapsed time.
func TestUndersizedMatch_AllowedWhenFailsafeZero(t *testing.T) {
	logger := loggerForTest(t)
	matchmaker, cleanup := createProcessorTestMatchmaker(t, logger)
	defer cleanup()
	wireEvrProcessor(matchmaker, NewRuntimeGoLogger(logger))

	// Even with timestamp = now (0 seconds elapsed), failsafe_timeout=0
	// should bypass the check entirely.
	for i := range 4 {
		addIntegrationProcessorTicketWithProps(t, matchmaker, fmt.Sprintf("zero-%02d", i), nil, map[string]float64{
			"failsafe_timeout": 0,
			"min_team_size":    4,
			"max_count":        8,
			"count_multiple":   2,
			"timestamp":        float64(time.Now().UTC().Unix()),
		})
	}

	matched, _ := runProcessorCycle(matchmaker)
	if len(matched) != 1 {
		t.Fatalf("expected 1 match (failsafe=0 bypasses check), got %d", len(matched))
	}
}

// --- Acceptance tests for progressive failsafe reduction ---

// TestUndersizedMatch_ProgressiveReduction simulates the full fallback sequence:
// 1. At t=0: failsafe_timeout=300, undersized match blocked
// 2. At t=120: failsafe reduced to 180 (300-120), still blocked
// 3. At t=300: failsafe reduced to 0, undersized match allowed
//
// Each step creates a fresh matchmaker to simulate the ticket being replaced
// with a new failsafe_timeout value (as lobbyMatchMakeWithFallback does).
func TestUndersizedMatch_ProgressiveReduction(t *testing.T) {
	logger := loggerForTest(t)

	// Step 1: Fresh tickets, failsafe=300, elapsed=10s → blocked
	t.Run("early (failsafe=300, elapsed=10s)", func(t *testing.T) {
		mm, cleanup := createProcessorTestMatchmaker(t, logger)
		defer cleanup()
		wireEvrProcessor(mm, NewRuntimeGoLogger(logger))

		ticketTime := float64(time.Now().UTC().Unix()) - 10
		for i := range 6 {
			addIntegrationProcessorTicketWithProps(t, mm, fmt.Sprintf("prog1-%02d", i), nil, map[string]float64{
				"failsafe_timeout": 300,
				"min_team_size":    4,
				"max_count":        8,
				"count_multiple":   2,
				"timestamp":        ticketTime,
			})
		}
		matched, _ := runProcessorCycle(mm)
		if len(matched) != 0 {
			t.Fatalf("expected 0 matches, got %d", len(matched))
		}
	})

	// Step 2: After 120s, failsafe reduced to 180, elapsed=120s → still blocked
	t.Run("mid (failsafe=180, elapsed=120s)", func(t *testing.T) {
		mm, cleanup := createProcessorTestMatchmaker(t, logger)
		defer cleanup()
		wireEvrProcessor(mm, NewRuntimeGoLogger(logger))

		ticketTime := float64(time.Now().UTC().Unix()) - 120
		for i := range 6 {
			addIntegrationProcessorTicketWithProps(t, mm, fmt.Sprintf("prog2-%02d", i), nil, map[string]float64{
				"failsafe_timeout": 180, // 300 - 120 = 180 > 120 → blocked
				"min_team_size":    4,
				"max_count":        8,
				"count_multiple":   2,
				"timestamp":        ticketTime,
			})
		}
		matched, _ := runProcessorCycle(mm)
		if len(matched) != 0 {
			t.Fatalf("expected 0 matches, got %d", len(matched))
		}
	})

	// Step 3: After 300s, failsafe reduced to 0 → allowed
	t.Run("late (failsafe=0, elapsed=300s)", func(t *testing.T) {
		mm, cleanup := createProcessorTestMatchmaker(t, logger)
		defer cleanup()
		wireEvrProcessor(mm, NewRuntimeGoLogger(logger))

		ticketTime := float64(time.Now().UTC().Unix()) - 300
		for i := range 6 {
			addIntegrationProcessorTicketWithProps(t, mm, fmt.Sprintf("prog3-%02d", i), nil, map[string]float64{
				"failsafe_timeout": 0, // Fully reduced → allowed
				"min_team_size":    4,
				"max_count":        8,
				"count_multiple":   2,
				"timestamp":        ticketTime,
			})
		}
		matched, _ := runProcessorCycle(mm)
		if len(matched) != 1 {
			t.Fatalf("expected 1 match (failsafe=0), got %d", len(matched))
		}
	})
}

// --- Regression tests ---

// TestUndersizedMatch_FullSizeMatchNotAffected ensures that a full-size match
// (>= min_team_size*2 players) is never blocked by failsafe_timeout, regardless
// of its value. The failsafe only gates undersized matches.
func TestUndersizedMatch_FullSizeMatchNotAffected(t *testing.T) {
	logger := loggerForTest(t)
	matchmaker, cleanup := createProcessorTestMatchmaker(t, logger)
	defer cleanup()
	wireEvrProcessor(matchmaker, NewRuntimeGoLogger(logger))

	// 8 players = 4*2 = full match. failsafe_timeout=9999 should not matter.
	for i := range 8 {
		addIntegrationProcessorTicketWithProps(t, matchmaker, fmt.Sprintf("full-%02d", i), nil, map[string]float64{
			"failsafe_timeout": 9999,
			"min_team_size":    4,
			"max_count":        8,
			"count_multiple":   2,
			"timestamp":        float64(time.Now().UTC().Unix()),
		})
	}

	matched, _ := runProcessorCycle(matchmaker)
	if len(matched) != 1 {
		t.Fatalf("expected 1 match (full size, failsafe irrelevant), got %d", len(matched))
	}
	if len(matched[0]) != 8 {
		t.Fatalf("expected 8 players, got %d", len(matched[0]))
	}
}

// TestUndersizedMatch_CombatMinTeamSize3 verifies that combat mode
// (min_team_size=3, so min match = 6) uses its own threshold, not arena's 8.
// 4 players in combat should be undersized (4 < 3*2=6).
func TestUndersizedMatch_CombatMinTeamSize3(t *testing.T) {
	logger := loggerForTest(t)
	matchmaker, cleanup := createProcessorTestMatchmaker(t, logger)
	defer cleanup()
	wireEvrProcessor(matchmaker, NewRuntimeGoLogger(logger))

	for i := range 4 {
		addIntegrationProcessorTicketWithProps(t, matchmaker, fmt.Sprintf("combat4-%02d", i),
			map[string]string{"game_mode": "echo_combat"},
			map[string]float64{
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
		t.Fatalf("expected 0 matches (4 < 3*2=6, failsafe not expired), got %d", len(matched))
	}
}

// TestUndersizedMatch_CombatMinTeamSize3_AllowedAfterFailsafe verifies that
// 4 combat players with an expired failsafe ARE matched.
func TestUndersizedMatch_CombatMinTeamSize3_AllowedAfterFailsafe(t *testing.T) {
	logger := loggerForTest(t)
	matchmaker, cleanup := createProcessorTestMatchmaker(t, logger)
	defer cleanup()
	wireEvrProcessor(matchmaker, NewRuntimeGoLogger(logger))

	for i := range 4 {
		addIntegrationProcessorTicketWithProps(t, matchmaker, fmt.Sprintf("combat4ok-%02d", i),
			map[string]string{"game_mode": "echo_combat"},
			map[string]float64{
				"failsafe_timeout": 0, // Expired/reduced → allowed
				"min_team_size":    3,
				"max_team_size":    5,
				"max_count":        10,
				"count_multiple":   2,
				"timestamp":        float64(time.Now().UTC().Unix()) - 120,
			})
	}

	matched, _ := runProcessorCycle(matchmaker)
	if len(matched) != 1 {
		t.Fatalf("expected 1 match (failsafe expired), got %d", len(matched))
	}
	if len(matched[0]) != 4 {
		t.Fatalf("expected 4 players, got %d", len(matched[0]))
	}
}

// TestUndersizedMatch_SinglePlayerNeverMatches ensures that a single player
// (below the absolute minimum of 2 entries) is never matched regardless of
// failsafe settings. The matchmaker's min_count=2 gate prevents this.
func TestUndersizedMatch_SinglePlayerNeverMatches(t *testing.T) {
	logger := loggerForTest(t)
	matchmaker, cleanup := createProcessorTestMatchmaker(t, logger)
	defer cleanup()
	wireEvrProcessor(matchmaker, NewRuntimeGoLogger(logger))

	addIntegrationProcessorTicketWithProps(t, matchmaker, "solo-00", nil, map[string]float64{
		"failsafe_timeout": 0, // Even with failsafe bypassed
		"min_team_size":    4,
		"max_count":        8,
		"count_multiple":   2,
		"timestamp":        float64(time.Now().UTC().Unix()) - 600,
	})

	matched, _ := runProcessorCycle(matchmaker)
	if len(matched) != 0 {
		t.Fatalf("expected 0 matches (single player can never match), got %d", len(matched))
	}
}

// TestUndersizedMatch_TwoPlayersWithExpiredFailsafe verifies the minimum
// viable match: 2 players with failsafe expired should form a match even
// though 2 < min_team_size*2 (8 for arena).
func TestUndersizedMatch_TwoPlayersWithExpiredFailsafe(t *testing.T) {
	logger := loggerForTest(t)
	matchmaker, cleanup := createProcessorTestMatchmaker(t, logger)
	defer cleanup()
	wireEvrProcessor(matchmaker, NewRuntimeGoLogger(logger))

	ticketTime := float64(time.Now().UTC().Unix()) - 120

	for i := range 2 {
		addIntegrationProcessorTicketWithProps(t, matchmaker, fmt.Sprintf("duo-%02d", i), nil, map[string]float64{
			"failsafe_timeout": 0,
			"min_team_size":    4,
			"max_count":        8,
			"count_multiple":   2,
			"timestamp":        ticketTime,
		})
	}

	matched, _ := runProcessorCycle(matchmaker)
	if len(matched) != 1 {
		t.Fatalf("expected 1 match (2 players, failsafe bypassed), got %d", len(matched))
	}
	if len(matched[0]) != 2 {
		t.Fatalf("expected 2 players, got %d", len(matched[0]))
	}
}
