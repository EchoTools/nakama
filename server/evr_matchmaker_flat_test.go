package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/heroiclabs/nakama-common/runtime"
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

func TestProcessPotentialMatches(t *testing.T) {
	t.Run("groups flat entries sequentially respecting max_count and count_multiple", func(t *testing.T) {
		m := NewSkillBasedMatchmaker()
		entries := testFlatMatchmakerEntries(10, "group-1", map[string]any{
			"max_count":      8.0,
			"count_multiple": 2.0,
		})

		candidates, matches, _, predictions := m.processPotentialMatches(entries)

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
		candidates, _, _, _ := m.processPotentialMatches(entries)
		duration := time.Since(start)

		if len(candidates) == 0 {
			t.Fatalf("expected non-empty grouped candidates")
		}
		if duration >= time.Second {
			t.Fatalf("expected processing to complete in <1s, took %s", duration)
		}
	})
}
