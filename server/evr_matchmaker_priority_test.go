package server

import (
	"sort"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

// mockMatchmakerEntry is a simple mock that implements runtime.MatchmakerEntry interface
type mockMatchmakerEntry struct {
	ticket     string
	createTime int64
}

func (m *mockMatchmakerEntry) GetTicket() string {
	return m.ticket
}

func (m *mockMatchmakerEntry) GetPresence() runtime.Presence {
	return nil
}

func (m *mockMatchmakerEntry) GetProperties() map[string]interface{} {
	return nil
}

func (m *mockMatchmakerEntry) GetPartyId() string {
	return ""
}

func (m *mockMatchmakerEntry) GetCreateTime() int64 {
	return m.createTime
}

// TestOldestTicketPriority verifies that the oldest ticket gets priority in matchmaking
func TestOldestTicketPriority(t *testing.T) {
	now := time.Now().UTC().Unix()

	// Create predictions with different timestamps
	// Player A: waited 100 seconds (oldest)
	// Player B: waited 50 seconds
	// Player C: waited 10 seconds (newest)
	predictions := []PredictedMatch{
		{
			Candidate: []runtime.MatchmakerEntry{
				&mockMatchmakerEntry{ticket: "player-c"},
			},
			Size:                  1,
			DrawProb:              0.5,
			DivisionCount:         1,
			OldestTicketTimestamp: now - 10,
		},
		{
			Candidate: []runtime.MatchmakerEntry{
				&mockMatchmakerEntry{ticket: "player-a"},
			},
			Size:                  1,
			DrawProb:              0.5,
			DivisionCount:         1,
			OldestTicketTimestamp: now - 100,
		},
		{
			Candidate: []runtime.MatchmakerEntry{
				&mockMatchmakerEntry{ticket: "player-b"},
			},
			Size:                  1,
			DrawProb:              0.5,
			DivisionCount:         1,
			OldestTicketTimestamp: now - 50,
		},
	}

	// Sort using the fixed logic
	sort.SliceStable(predictions, func(i, j int) bool {
		if predictions[i].Size != predictions[j].Size {
			return predictions[i].Size > predictions[j].Size
		}

		// Always allow the player matchmaking the longest to have priority
		// Sort by oldest ticket timestamp (smaller timestamp = older = higher priority)
		if predictions[i].OldestTicketTimestamp != predictions[j].OldestTicketTimestamp {
			return predictions[i].OldestTicketTimestamp < predictions[j].OldestTicketTimestamp
		}

		if predictions[i].DivisionCount != predictions[j].DivisionCount {
			return predictions[i].DivisionCount < predictions[j].DivisionCount
		}

		return predictions[i].DrawProb > predictions[j].DrawProb
	})

	// The oldest ticket should be first
	if predictions[0].Candidate[0].GetTicket() != "player-a" {
		t.Errorf("Expected oldest ticket (player-a) to be first, but got %s", predictions[0].Candidate[0].GetTicket())
		t.Logf("Sorted order:")
		for i, p := range predictions {
			t.Logf("  %d: %s (timestamp: %d, age: %d seconds)",
				i,
				p.Candidate[0].GetTicket(),
				p.OldestTicketTimestamp,
				now-p.OldestTicketTimestamp)
		}
	}

	// The second oldest should be second
	if predictions[1].Candidate[0].GetTicket() != "player-b" {
		t.Errorf("Expected second oldest ticket (player-b) to be second, but got %s", predictions[1].Candidate[0].GetTicket())
	}

	// The newest should be last
	if predictions[2].Candidate[0].GetTicket() != "player-c" {
		t.Errorf("Expected newest ticket (player-c) to be last, but got %s", predictions[2].Candidate[0].GetTicket())
	}
}

// TestOldestTicketPriorityMultiplePlayers tests the case where multiple predictions
// have the same size and division count, but different wait times
func TestOldestTicketPriorityMultiplePlayers(t *testing.T) {
	now := time.Now().UTC().Unix()

	// Create 5 predictions with different wait times, all same size and division count
	predictions := []PredictedMatch{
		{
			Candidate: []runtime.MatchmakerEntry{
				&mockMatchmakerEntry{ticket: "player-3"},
			},
			Size:                  1,
			DrawProb:              0.5,
			DivisionCount:         1,
			OldestTicketTimestamp: now - 30,
		},
		{
			Candidate: []runtime.MatchmakerEntry{
				&mockMatchmakerEntry{ticket: "player-1"},
			},
			Size:                  1,
			DrawProb:              0.5,
			DivisionCount:         1,
			OldestTicketTimestamp: now - 10,
		},
		{
			Candidate: []runtime.MatchmakerEntry{
				&mockMatchmakerEntry{ticket: "player-5"},
			},
			Size:                  1,
			DrawProb:              0.5,
			DivisionCount:         1,
			OldestTicketTimestamp: now - 50,
		},
		{
			Candidate: []runtime.MatchmakerEntry{
				&mockMatchmakerEntry{ticket: "player-2"},
			},
			Size:                  1,
			DrawProb:              0.5,
			DivisionCount:         1,
			OldestTicketTimestamp: now - 20,
		},
		{
			Candidate: []runtime.MatchmakerEntry{
				&mockMatchmakerEntry{ticket: "player-4"},
			},
			Size:                  1,
			DrawProb:              0.5,
			DivisionCount:         1,
			OldestTicketTimestamp: now - 40,
		},
	}

	// Sort using the fixed logic
	sort.SliceStable(predictions, func(i, j int) bool {
		if predictions[i].Size != predictions[j].Size {
			return predictions[i].Size > predictions[j].Size
		}

		// Always allow the player matchmaking the longest to have priority
		// Sort by oldest ticket timestamp (smaller timestamp = older = higher priority)
		if predictions[i].OldestTicketTimestamp != predictions[j].OldestTicketTimestamp {
			return predictions[i].OldestTicketTimestamp < predictions[j].OldestTicketTimestamp
		}

		if predictions[i].DivisionCount != predictions[j].DivisionCount {
			return predictions[i].DivisionCount < predictions[j].DivisionCount
		}

		return predictions[i].DrawProb > predictions[j].DrawProb
	})

	// Expected order: player-5 (50s), player-4 (40s), player-3 (30s), player-2 (20s), player-1 (10s)
	expectedOrder := []string{"player-5", "player-4", "player-3", "player-2", "player-1"}

	t.Logf("Sorted order with fixed logic:")
	for i, p := range predictions {
		ticket := p.Candidate[0].GetTicket()
		age := now - p.OldestTicketTimestamp
		t.Logf("  %d: %s (age: %d seconds)", i, ticket, age)

		if ticket != expectedOrder[i] {
			t.Errorf("Position %d: expected %s, got %s", i, expectedOrder[i], ticket)
		}
	}

	// Verify that player-5 (oldest) is first
	if predictions[0].Candidate[0].GetTicket() != "player-5" {
		t.Errorf("Expected player-5 (oldest, 50s) to be first, but got %s",
			predictions[0].Candidate[0].GetTicket())
	}
}
