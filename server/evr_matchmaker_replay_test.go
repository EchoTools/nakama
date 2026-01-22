package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

// TestMatchmakerStateCapture tests the state capture and save functionality
func TestMatchmakerStateCapture(t *testing.T) {
	// Create a temporary directory for test files
	tmpDir := filepath.Join(os.TempDir(), "matchmaker_replay_test")
	defer os.RemoveAll(tmpDir)

	// Create mock matchmaker entries
	mockEntries := createMockMatchmakerEntries(t, 8)
	
	// Create mock candidates (2 potential matches)
	candidates := [][]runtime.MatchmakerEntry{
		mockEntries[0:4],  // First potential match (4 players)
		mockEntries[4:8],  // Second potential match (4 players)
	}

	// Mock matches (first candidate was selected)
	matches := [][]runtime.MatchmakerEntry{
		mockEntries[0:4],
	}

	// Mock filter counts
	filterCounts := map[string]int{
		"max_rtt": 1,
	}

	// Mock predictions
	predictions := []PredictedMatch{
		{
			Candidate:             mockEntries[0:4],
			Size:                  4,
			DivisionCount:         2,
			OldestTicketTimestamp: time.Now().UTC().Unix() - 300, // 5 minutes ago
			DrawProb:              0.48,
		},
		{
			Candidate:             mockEntries[4:8],
			Size:                  4,
			DivisionCount:         3,
			OldestTicketTimestamp: time.Now().UTC().Unix() - 120, // 2 minutes ago
			DrawProb:              0.52,
		},
	}

	// Create matchmaker and capture state
	sbmm := NewSkillBasedMatchmaker()
	processingTime := time.Duration(150) * time.Millisecond

	state := sbmm.CaptureMatchmakerState(
		context.Background(),
		"echo_arena_public",
		"test-group-id",
		candidates,
		matches,
		filterCounts,
		processingTime,
		predictions,
	)

	// Verify basic state properties
	if state.Mode != "echo_arena_public" {
		t.Errorf("Expected mode 'echo_arena_public', got '%s'", state.Mode)
	}

	if state.GroupID != "test-group-id" {
		t.Errorf("Expected group_id 'test-group-id', got '%s'", state.GroupID)
	}

	if state.ProcessingTimeMs != 150 {
		t.Errorf("Expected processing time 150ms, got %d", state.ProcessingTimeMs)
	}

	if state.TotalPlayers != 8 {
		t.Errorf("Expected 8 total players, got %d", state.TotalPlayers)
	}

	if state.MatchedPlayers != 4 {
		t.Errorf("Expected 4 matched players, got %d", state.MatchedPlayers)
	}

	if state.UnmatchedPlayers != 4 {
		t.Errorf("Expected 4 unmatched players, got %d", state.UnmatchedPlayers)
	}

	if state.MatchCount != 1 {
		t.Errorf("Expected 1 match, got %d", state.MatchCount)
	}

	// Verify candidates were captured
	if len(state.Candidates) != 2 {
		t.Errorf("Expected 2 candidates, got %d", len(state.Candidates))
	}

	// Verify matches were captured
	if len(state.Matches) != 1 {
		t.Errorf("Expected 1 match, got %d", len(state.Matches))
	}

	// Verify filter counts
	if state.FilterCounts["max_rtt"] != 1 {
		t.Errorf("Expected max_rtt filter count 1, got %d", state.FilterCounts["max_rtt"])
	}

	// Verify prediction details
	if len(state.PredictionDetails) != 2 {
		t.Errorf("Expected 2 prediction details, got %d", len(state.PredictionDetails))
	}

	// Verify first prediction is marked as selected
	if !state.PredictionDetails[0].Selected {
		t.Error("Expected first prediction to be selected")
	}

	// Verify second prediction is not selected
	if state.PredictionDetails[1].Selected {
		t.Error("Expected second prediction to not be selected")
	}

	// Test saving state to file
	filepath, err := SaveMatchmakerState(state, tmpDir)
	if err != nil {
		t.Fatalf("Failed to save matchmaker state: %v", err)
	}

	// Verify file was created
	if _, err := os.Stat(filepath); os.IsNotExist(err) {
		t.Fatalf("State file was not created: %s", filepath)
	}

	// Test loading state from file
	loadedState, err := LoadMatchmakerState(filepath)
	if err != nil {
		t.Fatalf("Failed to load matchmaker state: %v", err)
	}

	// Verify loaded state matches original
	if loadedState.Mode != state.Mode {
		t.Errorf("Loaded mode doesn't match: expected '%s', got '%s'", state.Mode, loadedState.Mode)
	}

	if loadedState.TotalPlayers != state.TotalPlayers {
		t.Errorf("Loaded player count doesn't match: expected %d, got %d", state.TotalPlayers, loadedState.TotalPlayers)
	}

	if loadedState.ProcessingTimeMs != state.ProcessingTimeMs {
		t.Errorf("Loaded processing time doesn't match: expected %d, got %d", state.ProcessingTimeMs, loadedState.ProcessingTimeMs)
	}

	// Verify JSON structure is valid
	jsonData, err := json.MarshalIndent(loadedState, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal loaded state to JSON: %v", err)
	}

	// Verify key fields exist in JSON
	jsonStr := string(jsonData)
	requiredFields := []string{
		"\"timestamp\"",
		"\"mode\"",
		"\"group_id\"",
		"\"candidates\"",
		"\"matches\"",
		"\"total_players\"",
		"\"matched_players\"",
		"\"unmatched_players\"",
		"\"prediction_details\"",
	}

	for _, field := range requiredFields {
		if !contains(jsonStr, field) {
			t.Errorf("JSON output missing required field: %s", field)
		}
	}
}

// TestPredictionDetailCapture tests that prediction details are captured correctly
func TestPredictionDetailCapture(t *testing.T) {
	mockEntries := createMockMatchmakerEntries(t, 4)
	
	// Set specific rating and wait time properties
	now := time.Now().UTC().Unix()
	for i, entry := range mockEntries {
		props := entry.GetProperties()
		props["rating_mu"] = float64(20 + i*2)  // 20, 22, 24, 26
		props["rating_sigma"] = float64(3)
		props["submission_time"] = float64(now - int64(60*(i+1))) // 1, 2, 3, 4 minutes ago
		props["divisions"] = "gold"
	}

	prediction := PredictedMatch{
		Candidate:             mockEntries,
		Size:                  4,
		DivisionCount:         1,
		OldestTicketTimestamp: now - 240, // 4 minutes ago
		DrawProb:              0.5,
	}

	sbmm := NewSkillBasedMatchmaker()
	state := sbmm.CaptureMatchmakerState(
		context.Background(),
		"echo_arena_public",
		"test-group",
		[][]runtime.MatchmakerEntry{mockEntries},
		[][]runtime.MatchmakerEntry{},
		map[string]int{},
		time.Millisecond*100,
		[]PredictedMatch{prediction},
	)

	// Verify prediction details
	if len(state.PredictionDetails) != 1 {
		t.Fatalf("Expected 1 prediction detail, got %d", len(state.PredictionDetails))
	}

	detail := state.PredictionDetails[0]
	
	if detail.Size != 4 {
		t.Errorf("Expected size 4, got %d", detail.Size)
	}

	if detail.DivisionCount != 1 {
		t.Errorf("Expected division count 1, got %d", detail.DivisionCount)
	}

	if detail.DrawProb != 0.5 {
		t.Errorf("Expected draw prob 0.5, got %f", detail.DrawProb)
	}

	// Verify ticket summaries
	if len(detail.Tickets) == 0 {
		t.Fatal("Expected ticket summaries, got none")
	}

	// Check that wait times were calculated
	for i, ticket := range detail.Tickets {
		if ticket.WaitTimeSeconds <= 0 {
			t.Errorf("Ticket %d has invalid wait time: %f", i, ticket.WaitTimeSeconds)
		}

		if ticket.RatingMu < 20 || ticket.RatingMu > 26 {
			t.Errorf("Ticket %d has unexpected rating: %f", i, ticket.RatingMu)
		}

		if ticket.Divisions != "gold" {
			t.Errorf("Ticket %d has unexpected divisions: %s", i, ticket.Divisions)
		}
	}
}

// Helper function to create mock matchmaker entries for testing
func createMockMatchmakerEntries(t *testing.T, count int) []runtime.MatchmakerEntry {
	entries := make([]runtime.MatchmakerEntry, count)
	now := time.Now().UTC().Unix()

	for i := 0; i < count; i++ {
		entries[i] = &mockMatchmakerEntry{
			ticket: generateTicketID(),
			presence: &mockPresence{
				userId:    generateUserID(),
				sessionId: generateSessionID(),
				username:  "player" + string(rune('A'+i)),
			},
			properties: map[string]interface{}{
				"rating_mu":       float64(20 + i),
				"rating_sigma":    float64(3),
				"submission_time": float64(now - int64(60*i)), // Decreasing wait times
				"divisions":       "gold,silver",
				"max_rtt":         float64(200),
			},
			partyId: "",
		}
	}

	return entries
}

// Helper function to check if string contains substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && 
		(s[:len(substr)] == substr || contains(s[1:], substr)))
}
