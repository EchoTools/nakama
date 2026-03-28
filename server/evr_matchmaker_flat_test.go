package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

func testFlatMatchmakerEntry(i int, groupID string, overrides map[string]any) runtime.MatchmakerEntry {
	now := float64(time.Now().UTC().Unix())
	props := map[string]any{
		"group_id":        groupID,
		"game_mode":       "echo_arena",
		"max_count":       8.0,
		"count_multiple":  2.0,
		"max_rtt":         250.0,
		"submission_time": now - float64(i),
		"rating_mu":       25.0,
		"rating_sigma":    8.33,
		"rtt_test":        40.0,
	}

	for k, v := range overrides {
		props[k] = v
	}

	return &MatchmakerEntry{
		Ticket: fmt.Sprintf("ticket-%03d", i),
		Presence: &MatchmakerPresence{
			UserId:    fmt.Sprintf("user-%03d", i),
			SessionId: fmt.Sprintf("session-%03d", i),
			Username:  fmt.Sprintf("player-%03d", i),
		},
		Properties: props,
	}
}

func testFlatMatchmakerEntries(count int, groupID string, overrides map[string]any) []runtime.MatchmakerEntry {
	entries := make([]runtime.MatchmakerEntry, 0, count)
	for i := 0; i < count; i++ {
		entries = append(entries, testFlatMatchmakerEntry(i, groupID, overrides))
	}
	return entries
}

func TestEvrMatchmakerFn(t *testing.T) {
	t.Run("empty entries returns nil", func(t *testing.T) {
		m := NewSkillBasedMatchmaker()
		logger := NewRuntimeGoLogger(loggerForTest(t))

		matches := m.EvrMatchmakerFn(context.Background(), logger, nil, nil, nil)
		if matches != nil {
			t.Fatalf("expected nil matches, got %v", matches)
		}
	})

	t.Run("missing group_id returns nil", func(t *testing.T) {
		m := NewSkillBasedMatchmaker()
		logger := NewRuntimeGoLogger(loggerForTest(t))

		entries := testFlatMatchmakerEntries(8, "", map[string]any{"group_id": ""})
		matches := m.EvrMatchmakerFn(context.Background(), logger, nil, nil, entries)
		if matches != nil {
			t.Fatalf("expected nil matches, got %v", matches)
		}
	})

	t.Run("valid flat entries produce a match", func(t *testing.T) {
		m := NewSkillBasedMatchmaker()
		logger := NewRuntimeGoLogger(loggerForTest(t))

		entries := testFlatMatchmakerEntries(8, "group-1", nil)
		matches := m.EvrMatchmakerFn(context.Background(), logger, nil, nil, entries)
		if len(matches) != 1 {
			t.Fatalf("expected 1 match, got %d", len(matches))
		}
		if len(matches[0]) != 8 {
			t.Fatalf("expected match size 8, got %d", len(matches[0]))
		}
	})
}

func TestGroupEntriesPartyAtomicity(t *testing.T) {
	t.Run("party members stay in same candidate", func(t *testing.T) {
		// Create 10 entries: 6 solo players + 1 party of 4 (same ticket).
		// With maxCount=8, the party must not be split across candidates.
		entries := make([]runtime.MatchmakerEntry, 0, 10)
		now := float64(time.Now().UTC().Unix())
		baseProps := map[string]any{
			"group_id":        "group-1",
			"game_mode":       "echo_arena",
			"max_count":       8.0,
			"count_multiple":  2.0,
			"max_rtt":         250.0,
			"rtt_test":        40.0,
			"rating_mu":       25.0,
			"rating_sigma":    8.33,
			"submission_time": now,
		}

		// 6 solo players (each with a unique ticket)
		for i := 0; i < 6; i++ {
			props := make(map[string]any)
			for k, v := range baseProps {
				props[k] = v
			}
			entries = append(entries, &MatchmakerEntry{
				Ticket: fmt.Sprintf("solo-%d", i),
				Presence: &MatchmakerPresence{
					UserId:    fmt.Sprintf("user-solo-%d", i),
					SessionId: fmt.Sprintf("session-solo-%d", i),
					Username:  fmt.Sprintf("solo-%d", i),
				},
				Properties: props,
			})
		}
		// 4-player party (all share "party-ticket")
		for i := 0; i < 4; i++ {
			props := make(map[string]any)
			for k, v := range baseProps {
				props[k] = v
			}
			entries = append(entries, &MatchmakerEntry{
				Ticket: "party-ticket",
				Presence: &MatchmakerPresence{
					UserId:    fmt.Sprintf("user-party-%d", i),
					SessionId: fmt.Sprintf("session-party-%d", i),
					Username:  fmt.Sprintf("party-%d", i),
				},
				Properties: props,
			})
		}

		candidates := groupEntriesSequentially(entries)

		// Verify no candidate splits the party ticket across boundaries
		for _, candidate := range candidates {
			partyCount := 0
			for _, e := range candidate {
				if e.GetTicket() == "party-ticket" {
					partyCount++
				}
			}
			if partyCount > 0 && partyCount != 4 {
				t.Fatalf("party was split: found %d of 4 party members in a single candidate", partyCount)
			}
		}

		// Verify all entries accounted for (10 total, in groups divisible by 2)
		total := 0
		for _, c := range candidates {
			if len(c)%2 != 0 {
				t.Fatalf("candidate size %d not divisible by count_multiple 2", len(c))
			}
			total += len(c)
		}
		if total != 10 {
			t.Fatalf("expected 10 total entries across candidates, got %d", total)
		}
	})

	t.Run("parties packed before solos regardless of insertion order", func(t *testing.T) {
		// Regression: groupEntriesSequentially used to pack in insertion order,
		// so a party added after solos would be starved if solos filled the
		// first candidate. The fix sorts largest tickets first.
		now := float64(time.Now().UTC().Unix())
		baseProps := map[string]any{
			"group_id":        "group-1",
			"game_mode":       "echo_arena",
			"max_count":       8.0,
			"count_multiple":  2.0,
			"max_rtt":         250.0,
			"rtt_test":        40.0,
			"rating_mu":       25.0,
			"rating_sigma":    8.33,
			"submission_time": now,
		}

		entries := make([]runtime.MatchmakerEntry, 0, 12)

		// Add 8 solos FIRST (older timestamps)
		for i := 0; i < 8; i++ {
			props := make(map[string]any)
			for k, v := range baseProps {
				props[k] = v
			}
			entries = append(entries, &MatchmakerEntry{
				Ticket: fmt.Sprintf("solo-%d", i),
				Presence: &MatchmakerPresence{
					UserId:    fmt.Sprintf("user-solo-%d", i),
					SessionId: fmt.Sprintf("session-solo-%d", i),
					Username:  fmt.Sprintf("solo-%d", i),
				},
				Properties: props,
			})
		}

		// Add party of 4 AFTER solos
		for i := 0; i < 4; i++ {
			props := make(map[string]any)
			for k, v := range baseProps {
				props[k] = v
			}
			entries = append(entries, &MatchmakerEntry{
				Ticket: "late-party",
				Presence: &MatchmakerPresence{
					UserId:    fmt.Sprintf("user-party-%d", i),
					SessionId: fmt.Sprintf("session-party-%d", i),
					Username:  fmt.Sprintf("party-%d", i),
				},
				Properties: props,
			})
		}

		candidates := groupEntriesSequentially(entries)

		// The party (size 4) must appear in the first candidate, not be
		// pushed to a leftover remainder by 8 solos filling it first.
		if len(candidates) == 0 {
			t.Fatal("expected at least one candidate")
		}
		partyInFirst := 0
		for _, e := range candidates[0] {
			if e.GetTicket() == "late-party" {
				partyInFirst++
			}
		}
		if partyInFirst != 4 {
			t.Fatalf("expected all 4 party members in first candidate, got %d", partyInFirst)
		}
	})

	t.Run("multiple parties sorted largest first", func(t *testing.T) {
		now := float64(time.Now().UTC().Unix())
		baseProps := map[string]any{
			"group_id":        "group-1",
			"game_mode":       "echo_arena",
			"max_count":       8.0,
			"count_multiple":  2.0,
			"max_rtt":         250.0,
			"rtt_test":        40.0,
			"rating_mu":       25.0,
			"rating_sigma":    8.33,
			"submission_time": now,
		}

		entries := make([]runtime.MatchmakerEntry, 0, 10)

		// Small party of 2
		for i := 0; i < 2; i++ {
			props := make(map[string]any)
			for k, v := range baseProps {
				props[k] = v
			}
			entries = append(entries, &MatchmakerEntry{
				Ticket: "small-party",
				Presence: &MatchmakerPresence{
					UserId:    fmt.Sprintf("user-small-%d", i),
					SessionId: fmt.Sprintf("session-small-%d", i),
					Username:  fmt.Sprintf("small-%d", i),
				},
				Properties: props,
			})
		}

		// 4 solos between the parties
		for i := 0; i < 4; i++ {
			props := make(map[string]any)
			for k, v := range baseProps {
				props[k] = v
			}
			entries = append(entries, &MatchmakerEntry{
				Ticket: fmt.Sprintf("solo-%d", i),
				Presence: &MatchmakerPresence{
					UserId:    fmt.Sprintf("user-solo-%d", i),
					SessionId: fmt.Sprintf("session-solo-%d", i),
					Username:  fmt.Sprintf("solo-%d", i),
				},
				Properties: props,
			})
		}

		// Large party of 4
		for i := 0; i < 4; i++ {
			props := make(map[string]any)
			for k, v := range baseProps {
				props[k] = v
			}
			entries = append(entries, &MatchmakerEntry{
				Ticket: "large-party",
				Presence: &MatchmakerPresence{
					UserId:    fmt.Sprintf("user-large-%d", i),
					SessionId: fmt.Sprintf("session-large-%d", i),
					Username:  fmt.Sprintf("large-%d", i),
				},
				Properties: props,
			})
		}

		candidates := groupEntriesSequentially(entries)

		// First candidate should contain the large party (4) + small party (2) + 2 solos = 8
		if len(candidates) == 0 {
			t.Fatal("expected at least one candidate")
		}
		largeInFirst := 0
		smallInFirst := 0
		for _, e := range candidates[0] {
			switch e.GetTicket() {
			case "large-party":
				largeInFirst++
			case "small-party":
				smallInFirst++
			}
		}
		if largeInFirst != 4 {
			t.Fatalf("expected 4 large-party members in first candidate, got %d", largeInFirst)
		}
		if smallInFirst != 2 {
			t.Fatalf("expected 2 small-party members in first candidate, got %d", smallInFirst)
		}
	})

	t.Run("party exactly fills candidate", func(t *testing.T) {
		now := float64(time.Now().UTC().Unix())
		baseProps := map[string]any{
			"group_id":        "group-1",
			"game_mode":       "echo_arena",
			"max_count":       4.0,
			"count_multiple":  2.0,
			"max_rtt":         250.0,
			"rtt_test":        40.0,
			"rating_mu":       25.0,
			"rating_sigma":    8.33,
			"submission_time": now,
		}

		entries := make([]runtime.MatchmakerEntry, 0, 4)
		for i := 0; i < 4; i++ {
			props := make(map[string]any)
			for k, v := range baseProps {
				props[k] = v
			}
			entries = append(entries, &MatchmakerEntry{
				Ticket: "exact-party",
				Presence: &MatchmakerPresence{
					UserId:    fmt.Sprintf("user-%d", i),
					SessionId: fmt.Sprintf("session-%d", i),
					Username:  fmt.Sprintf("player-%d", i),
				},
				Properties: props,
			})
		}

		candidates := groupEntriesSequentially(entries)
		if len(candidates) != 1 {
			t.Fatalf("expected 1 candidate, got %d", len(candidates))
		}
		if len(candidates[0]) != 4 {
			t.Fatalf("expected candidate size 4, got %d", len(candidates[0]))
		}
	})

	t.Run("party larger than maxCount is skipped", func(t *testing.T) {
		now := float64(time.Now().UTC().Unix())
		baseProps := map[string]any{
			"group_id":        "group-1",
			"game_mode":       "echo_arena",
			"max_team_size":   2.0, // maxCount = max_team_size * 2 = 4
			"count_multiple":  2.0,
			"max_rtt":         250.0,
			"rtt_test":        40.0,
			"rating_mu":       25.0,
			"rating_sigma":    8.33,
			"submission_time": now,
		}
		entries := make([]runtime.MatchmakerEntry, 0, 6)
		// 5-player party that exceeds maxCount of 4
		for i := 0; i < 5; i++ {
			props := make(map[string]any)
			for k, v := range baseProps {
				props[k] = v
			}
			entries = append(entries, &MatchmakerEntry{
				Ticket: "big-party",
				Presence: &MatchmakerPresence{
					UserId:    fmt.Sprintf("user-%d", i),
					SessionId: fmt.Sprintf("session-%d", i),
					Username:  fmt.Sprintf("player-%d", i),
				},
				Properties: props,
			})
		}

		candidates := groupEntriesSequentially(entries)
		// The oversized party should be skipped, no candidates produced
		if len(candidates) != 0 {
			t.Fatalf("expected 0 candidates for oversized party, got %d", len(candidates))
		}
	})
}

func TestProcessPotentialMatches(t *testing.T) {
	t.Run("groups flat entries sequentially respecting max_count and count_multiple", func(t *testing.T) {
		m := NewSkillBasedMatchmaker()
		entries := testFlatMatchmakerEntries(10, "group-1", map[string]any{
			"max_count":      8.0,
			"count_multiple": 2.0,
		})

		candidates, matches, _, predictions := m.processPotentialMatches(NewRuntimeGoLogger(zap.NewNop()), entries)

		sizes := make([]int, 0, len(candidates))
		for _, candidate := range candidates {
			if candidate == nil {
				continue
			}
			sizes = append(sizes, len(candidate))
		}

		if diff := cmp.Diff([]int{8, 2}, sizes); diff != "" {
			t.Fatalf("unexpected grouped candidate sizes (-want,+got):\n%s", diff)
		}
		if len(predictions) != len(sizes) {
			t.Fatalf("expected %d predictions, got %d", len(sizes), len(predictions))
		}
		if len(matches) == 0 {
			t.Fatalf("expected at least one assembled match")
		}
	})

	t.Run("100 entries processes under one second", func(t *testing.T) {
		m := NewSkillBasedMatchmaker()
		entries := testFlatMatchmakerEntries(100, "group-1", map[string]any{
			"max_count":      8.0,
			"count_multiple": 2.0,
		})

		start := time.Now()
		candidates, _, _, _ := m.processPotentialMatches(NewRuntimeGoLogger(zap.NewNop()), entries)
		duration := time.Since(start)

		if len(candidates) == 0 {
			t.Fatalf("expected non-empty grouped candidates")
		}
		if duration >= time.Second {
			t.Fatalf("expected processing to complete in <1s, took %s", duration)
		}
	})
}
