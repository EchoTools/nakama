package server

import (
	"context"
	"maps"
	"sort"
	"testing"

	"github.com/blugelabs/bluge"
	"github.com/gofrs/uuid/v5"
	"github.com/google/go-cmp/cmp"
	"go.uber.org/atomic"
	"go.uber.org/zap"
)

func TestProcessWithProcessor(t *testing.T) {
	t.Run("empty_entries", func(t *testing.T) {
		logger := loggerForTest(t)
		matchmaker, cleanup := createProcessorTestMatchmaker(t, logger)
		defer cleanup()

		var called int
		matchmaker.runtime.matchmakerProcessorFunction = func(_ context.Context, entries []*MatchmakerEntry) [][]*MatchmakerEntry {
			called++
			if len(entries) != 0 {
				t.Fatalf("expected empty entries, got %d", len(entries))
			}
			return nil
		}

		matched, expired := matchmaker.processWithProcessor(0, map[string]*MatchmakerIndex{}, 0, map[string]*MatchmakerIndex{})
		if called != 1 {
			t.Fatalf("expected processor call count 1, got %d", called)
		}
		if matched != nil {
			t.Fatalf("expected no matched entries, got %d", len(matched))
		}
		if len(expired) != 0 {
			t.Fatalf("expected no expired entries, got %d", len(expired))
		}
	})

	t.Run("single_match", func(t *testing.T) {
		logger := loggerForTest(t)
		matchmaker, cleanup := createProcessorTestMatchmaker(t, logger)
		defer cleanup()

		ticketA := addProcessorTestTicket(t, matchmaker, "a")
		ticketB := addProcessorTestTicket(t, matchmaker, "b")
		activeCount, activeCopy, indexCount, indexesCopy := copyIndexesForProcessorTest(matchmaker)

		matchmaker.runtime.matchmakerProcessorFunction = func(_ context.Context, entries []*MatchmakerEntry) [][]*MatchmakerEntry {
			wantTickets := []string{ticketA, ticketB}
			sort.Strings(wantTickets)
			if diff := cmp.Diff(wantTickets, sortedTickets(entries)); diff != "" {
				t.Fatalf("processor entries mismatch (-want +got):\n%s", diff)
			}
			return [][]*MatchmakerEntry{entries}
		}

		matched, expired := matchmaker.processWithProcessor(activeCount, activeCopy, indexCount, indexesCopy)
		if len(expired) != 0 {
			t.Fatalf("expected no expired entries, got %d", len(expired))
		}

		wantGroup := []string{ticketA, ticketB}
		sort.Strings(wantGroup)
		if diff := cmp.Diff([][]string{wantGroup}, normalizeMatchedTickets(matched)); diff != "" {
			t.Fatalf("matched entries mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("multiple_matches", func(t *testing.T) {
		logger := loggerForTest(t)
		matchmaker, cleanup := createProcessorTestMatchmaker(t, logger)
		defer cleanup()

		tickets := []string{
			addProcessorTestTicket(t, matchmaker, "a"),
			addProcessorTestTicket(t, matchmaker, "b"),
			addProcessorTestTicket(t, matchmaker, "c"),
			addProcessorTestTicket(t, matchmaker, "d"),
		}
		sort.Strings(tickets)
		activeCount, activeCopy, indexCount, indexesCopy := copyIndexesForProcessorTest(matchmaker)

		matchmaker.runtime.matchmakerProcessorFunction = func(_ context.Context, entries []*MatchmakerEntry) [][]*MatchmakerEntry {
			indexByTicket := make(map[string]*MatchmakerEntry, len(entries))
			for _, entry := range entries {
				indexByTicket[entry.Ticket] = entry
			}
			return [][]*MatchmakerEntry{
				{indexByTicket[tickets[0]], indexByTicket[tickets[1]]},
				{indexByTicket[tickets[2]], indexByTicket[tickets[3]]},
			}
		}

		matched, _ := matchmaker.processWithProcessor(activeCount, activeCopy, indexCount, indexesCopy)
		if diff := cmp.Diff([][]string{{tickets[0], tickets[1]}, {tickets[2], tickets[3]}}, normalizeMatchedTickets(matched)); diff != "" {
			t.Fatalf("matched entries mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("interval_expiry", func(t *testing.T) {
		logger := loggerForTest(t)
		matchmaker, cleanup := createProcessorTestMatchmaker(t, logger)
		defer cleanup()

		ticket := addProcessorTestTicket(t, matchmaker, "a")

		matchmaker.Lock()
		matchmaker.activeIndexes[ticket].Intervals = matchmaker.config.GetMatchmaker().MaxIntervals - 1
		matchmaker.Unlock()

		activeCount, activeCopy, indexCount, indexesCopy := copyIndexesForProcessorTest(matchmaker)
		matchmaker.runtime.matchmakerProcessorFunction = func(_ context.Context, entries []*MatchmakerEntry) [][]*MatchmakerEntry {
			if len(entries) != 0 {
				t.Fatalf("expected no compatible entries, got %d", len(entries))
			}
			return nil
		}

		_, expired := matchmaker.processWithProcessor(activeCount, activeCopy, indexCount, indexesCopy)
		if diff := cmp.Diff([]string{ticket}, expired); diff != "" {
			t.Fatalf("expired entries mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("span_multiple_indexes", func(t *testing.T) {
		logger := loggerForTest(t)
		matchmaker, cleanup := createProcessorTestMatchmaker(t, logger)
		defer cleanup()

		tickets := []string{
			addProcessorTestTicket(t, matchmaker, "a"),
			addProcessorTestTicket(t, matchmaker, "b"),
			addProcessorTestTicket(t, matchmaker, "c"),
		}
		sort.Strings(tickets)
		activeCount, activeCopy, indexCount, indexesCopy := copyIndexesForProcessorTest(matchmaker)

		var called int
		matchmaker.runtime.matchmakerProcessorFunction = func(_ context.Context, entries []*MatchmakerEntry) [][]*MatchmakerEntry {
			called++
			if diff := cmp.Diff(tickets, sortedTickets(entries)); diff != "" {
				t.Fatalf("processor entries mismatch (-want +got):\n%s", diff)
			}
			return nil
		}

		matched, _ := matchmaker.processWithProcessor(activeCount, activeCopy, indexCount, indexesCopy)
		if called != 1 {
			t.Fatalf("expected processor call count 1, got %d", called)
		}
		if len(matched) != 0 {
			t.Fatalf("expected no matched entries, got %d", len(matched))
		}
	})

	t.Run("selected_tickets_excluded", func(t *testing.T) {
		logger := loggerForTest(t)
		matchmaker, cleanup := createProcessorTestMatchmaker(t, logger)
		defer cleanup()

		tickets := []string{
			addProcessorTestTicket(t, matchmaker, "a"),
			addProcessorTestTicket(t, matchmaker, "b"),
			addProcessorTestTicket(t, matchmaker, "c"),
		}
		sort.Strings(tickets)

		selected := map[string]struct{}{
			tickets[0]: {},
			tickets[1]: {},
		}
		var callCount int
		matchmaker.runtime.matchmakerProcessorFunction = func(_ context.Context, entries []*MatchmakerEntry) [][]*MatchmakerEntry {
			callCount++
			if callCount == 1 {
				var selectedEntries []*MatchmakerEntry
				for _, entry := range entries {
					if _, ok := selected[entry.Ticket]; ok {
						selectedEntries = append(selectedEntries, entry)
					}
				}
				return [][]*MatchmakerEntry{selectedEntries}
			}
			return nil
		}

		activeCount, activeCopy, indexCount, indexesCopy := copyIndexesForProcessorTest(matchmaker)
		matchmaker.processWithProcessor(activeCount, activeCopy, indexCount, indexesCopy)

		activeCount, activeCopy, indexCount, indexesCopy = copyIndexesForProcessorTest(matchmaker)
		matchmaker.processWithProcessor(activeCount, activeCopy, indexCount, indexesCopy)

		if callCount != 2 {
			t.Fatalf("expected processor call count 2, got %d", callCount)
		}

		matchmaker.runtime.matchmakerProcessorFunction = func(_ context.Context, entries []*MatchmakerEntry) [][]*MatchmakerEntry {
			for _, entry := range entries {
				if _, ok := selected[entry.Ticket]; ok {
					t.Fatalf("selected ticket %q should be excluded from future consideration", entry.Ticket)
				}
			}
			return nil
		}

		activeCount, activeCopy, indexCount, indexesCopy = copyIndexesForProcessorTest(matchmaker)
		matchmaker.processWithProcessor(activeCount, activeCopy, indexCount, indexesCopy)
	})
}

func addProcessorTestTicket(t *testing.T, matchmaker *LocalMatchmaker, sessionID string) string {
	t.Helper()

	uid, err := uuid.NewV4()
	if err != nil {
		t.Fatalf("new uuid: %v", err)
	}

	ticket, _, err := matchmaker.Add(context.Background(), []*MatchmakerPresence{{
		UserId:    sessionID,
		SessionId: sessionID,
		Username:  sessionID,
		Node:      sessionID,
		SessionID: uid,
	}}, sessionID, "", "*", 2, 4, 1, map[string]string{"role": sessionID}, map[string]float64{})
	if err != nil {
		t.Fatalf("add ticket: %v", err)
	}

	return ticket
}

func createProcessorTestMatchmaker(t *testing.T, logger *zap.Logger) (*LocalMatchmaker, func()) {
	t.Helper()

	cfg := NewConfig(logger)
	cfg.Matchmaker.MaxIntervals = 5

	indexWriter, err := bluge.OpenWriter(BlugeInMemoryConfig())
	if err != nil {
		t.Fatalf("open index writer: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	matchmaker := &LocalMatchmaker{
		logger: logger,
		node:   cfg.GetName(),
		config: cfg,
		runtime: &Runtime{
			matchmakerProcessorFunction: nil,
		},
		active:         atomic.NewUint32(1),
		stopped:        atomic.NewBool(false),
		ctx:            ctx,
		ctxCancelFn:    cancel,
		indexWriter:    indexWriter,
		sessionTickets: make(map[string]map[string]struct{}),
		partyTickets:   make(map[string]map[string]struct{}),
		indexes:        make(map[string]*MatchmakerIndex),
		activeIndexes:  make(map[string]*MatchmakerIndex),
		revCache:       &MapOf[string, map[string]bool]{},
	}

	return matchmaker, func() {
		cancel()
		_ = indexWriter.Close()
	}
}

func copyIndexesForProcessorTest(matchmaker *LocalMatchmaker) (int, map[string]*MatchmakerIndex, int, map[string]*MatchmakerIndex) {
	matchmaker.Lock()
	defer matchmaker.Unlock()

	activeCount := len(matchmaker.activeIndexes)
	activeCopy := make(map[string]*MatchmakerIndex, activeCount)
	maps.Copy(activeCopy, matchmaker.activeIndexes)

	indexCount := len(matchmaker.indexes)
	indexesCopy := make(map[string]*MatchmakerIndex, indexCount)
	maps.Copy(indexesCopy, matchmaker.indexes)

	return activeCount, activeCopy, indexCount, indexesCopy
}

func sortedTickets(entries []*MatchmakerEntry) []string {
	tickets := make([]string, 0, len(entries))
	for _, entry := range entries {
		tickets = append(tickets, entry.Ticket)
	}
	sort.Strings(tickets)
	return tickets
}

func normalizeMatchedTickets(matched [][]*MatchmakerEntry) [][]string {
	out := make([][]string, 0, len(matched))
	for _, group := range matched {
		tickets := sortedTickets(group)
		out = append(out, tickets)
	}
	sort.Slice(out, func(i, j int) bool {
		for idx := 0; idx < len(out[i]) && idx < len(out[j]); idx++ {
			if out[i][idx] != out[j][idx] {
				return out[i][idx] < out[j][idx]
			}
		}
		return len(out[i]) < len(out[j])
	})
	return out
}
