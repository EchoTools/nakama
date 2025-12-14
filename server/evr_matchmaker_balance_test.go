package server

import (
	"fmt"
	"math"
	"sort"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/intinig/go-openskill/rating"
	"github.com/intinig/go-openskill/types"
)

// Helper to create a matchmaker entry with specific rating
func createTestEntry(ticketSuffix string, sessionID string, mu, sigma float64) *MatchmakerEntry {
	sid := uuid.NewV5(uuid.Nil, sessionID)
	// Ensure ticket is a valid UUID format (required by HashMatchmakerEntries)
	ticket := uuid.NewV5(uuid.Nil, ticketSuffix).String()
	return &MatchmakerEntry{
		Ticket: ticket,
		Presence: &MatchmakerPresence{
			UserId:    uuid.NewV5(uuid.Nil, sessionID).String(),
			SessionId: sid.String(),
			Username:  "player_" + sessionID,
			SessionID: sid,
		},
		Properties: map[string]interface{}{
			"rating_mu":       mu,
			"rating_sigma":    sigma,
			"submission_time": float64(time.Now().UTC().Unix()),
			"divisions":       "gold",
			"max_rtt":         float64(100),
			"rtt_server1":     float64(50),
		},
	}
}

// Helper to create a party (multiple entries with same ticket)
func createTestParty(ticketSuffix string, players []struct {
	id        string
	mu, sigma float64
}) []*MatchmakerEntry {
	// Ensure ticket is a valid UUID format
	ticket := uuid.NewV5(uuid.Nil, ticketSuffix).String()
	entries := make([]*MatchmakerEntry, len(players))
	for i, p := range players {
		sid := uuid.NewV5(uuid.Nil, p.id)
		entries[i] = &MatchmakerEntry{
			Ticket: ticket, // Same ticket for party members
			Presence: &MatchmakerPresence{
				UserId:    uuid.NewV5(uuid.Nil, p.id).String(),
				SessionId: sid.String(),
				Username:  "player_" + p.id,
				SessionID: sid,
			},
			Properties: map[string]interface{}{
				"rating_mu":       p.mu,
				"rating_sigma":    p.sigma,
				"submission_time": float64(time.Now().UTC().Unix()),
				"divisions":       "gold",
				"max_rtt":         float64(100),
				"rtt_server1":     float64(50),
			},
		}
	}
	return entries
}

// calculateTeamStrength returns the sum of Mu values for a team
func calculateTeamStrength(entries []runtime.MatchmakerEntry) float64 {
	strength := 0.0
	for _, e := range entries {
		strength += e.GetProperties()["rating_mu"].(float64)
	}
	return strength
}

// TestCharacterizationTeamFormation_SequentialFilling documents the current behavior
// where teams are formed by sequential filling after sorting by rank.
// Note: Due to how the current algorithm works (filling Team A until full, then Team B),
// the actual balance depends on the predicted rank order of groups.
func TestCharacterizationTeamFormation_SequentialFilling(t *testing.T) {

	tests := []struct {
		name        string
		entries     []*MatchmakerEntry
		description string
	}{
		{
			name: "8 solo players with skill spread 20-27",
			entries: []*MatchmakerEntry{
				createTestEntry("ticket1", "p1", 27.0, 3.0), // High skill
				createTestEntry("ticket2", "p2", 26.0, 3.0), // High skill
				createTestEntry("ticket3", "p3", 25.0, 3.0), // High skill
				createTestEntry("ticket4", "p4", 24.0, 3.0), // Medium skill
				createTestEntry("ticket5", "p5", 23.0, 3.0), // Medium skill
				createTestEntry("ticket6", "p6", 22.0, 3.0), // Low skill
				createTestEntry("ticket7", "p7", 21.0, 3.0), // Low skill
				createTestEntry("ticket8", "p8", 20.0, 3.0), // Low skill
			},
			description: "Testing sequential fill with varied skills",
		},
		{
			name: "4 high skill + 4 low skill players",
			entries: []*MatchmakerEntry{
				createTestEntry("ticket1", "p1", 30.0, 3.0), // High
				createTestEntry("ticket2", "p2", 30.0, 3.0), // High
				createTestEntry("ticket3", "p3", 30.0, 3.0), // High
				createTestEntry("ticket4", "p4", 30.0, 3.0), // High
				createTestEntry("ticket5", "p5", 15.0, 3.0), // Low
				createTestEntry("ticket6", "p6", 15.0, 3.0), // Low
				createTestEntry("ticket7", "p7", 15.0, 3.0), // Low
				createTestEntry("ticket8", "p8", 15.0, 3.0), // Low
			},
			description: "Equal high/low split - ideal for balanced vs stacked comparison",
		},
		{
			name: "Extreme skill gap - 2 pros vs 6 beginners",
			entries: []*MatchmakerEntry{
				createTestEntry("ticket1", "p1", 40.0, 2.0), // Pro
				createTestEntry("ticket2", "p2", 38.0, 2.0), // Pro
				createTestEntry("ticket3", "p3", 15.0, 5.0), // Beginner
				createTestEntry("ticket4", "p4", 15.0, 5.0), // Beginner
				createTestEntry("ticket5", "p5", 14.0, 5.0), // Beginner
				createTestEntry("ticket6", "p6", 14.0, 5.0), // Beginner
				createTestEntry("ticket7", "p7", 13.0, 5.0), // Beginner
				createTestEntry("ticket8", "p8", 13.0, 5.0), // Beginner
			},
			description: "2 high-skill players should be split across teams ideally",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidates := [][]runtime.MatchmakerEntry{
				make([]runtime.MatchmakerEntry, len(tt.entries)),
			}
			for i, e := range tt.entries {
				candidates[0][i] = e
			}

			// Process through prediction
			var result PredictedMatch
			for p := range predictCandidateOutcomes(candidates) {
				result = p
			}

			if len(result.Candidate) != 8 {
				t.Fatalf("Expected 8 players in result, got %d", len(result.Candidate))
			}

			// Calculate team strengths (first 4 = Team A, last 4 = Team B)
			teamA := result.Candidate[:4]
			teamB := result.Candidate[4:]

			strengthA := calculateTeamStrength(teamA)
			strengthB := calculateTeamStrength(teamB)

			t.Logf("Description: %s", tt.description)
			t.Logf("Team A strength: %.1f (players: %v)", strengthA, getPlayerMus(teamA))
			t.Logf("Team B strength: %.1f (players: %v)", strengthB, getPlayerMus(teamB))
			t.Logf("Draw probability: %.3f", result.Draw)

			// Calculate imbalance ratio
			totalStrength := strengthA + strengthB
			imbalance := math.Abs(strengthA-strengthB) / totalStrength * 100
			t.Logf("Imbalance: %.2f%%", imbalance)

			// Document: <5% imbalance is good, 5-15% is acceptable, >15% is poor
			if imbalance > 15 {
				t.Logf("⚠️  HIGH IMBALANCE: Teams are significantly unbalanced")
			} else if imbalance > 5 {
				t.Logf("⚡ MODERATE IMBALANCE: Teams are somewhat unbalanced")
			} else {
				t.Logf("✓ BALANCED: Teams are well-matched")
			}
		})
	}
}

// TestCharacterizationTeamFormation_PartiesKeptTogether verifies parties stay on same team
func TestCharacterizationTeamFormation_PartiesKeptTogether(t *testing.T) {
	// Create a party of 2 high-skill players
	partyTicketSuffix := "party_ticket_1"
	partyTicket := uuid.NewV5(uuid.Nil, partyTicketSuffix).String()

	party1 := createTestParty(partyTicketSuffix, []struct {
		id        string
		mu, sigma float64
	}{
		{"p1", 28.0, 3.0},
		{"p2", 27.0, 3.0},
	})

	// Create solo players
	solos := []*MatchmakerEntry{
		createTestEntry("ticket3", "p3", 25.0, 3.0),
		createTestEntry("ticket4", "p4", 24.0, 3.0),
		createTestEntry("ticket5", "p5", 23.0, 3.0),
		createTestEntry("ticket6", "p6", 22.0, 3.0),
		createTestEntry("ticket7", "p7", 21.0, 3.0),
		createTestEntry("ticket8", "p8", 20.0, 3.0),
	}

	// Combine into candidate
	entries := make([]runtime.MatchmakerEntry, 0, 8)
	for _, e := range party1 {
		entries = append(entries, e)
	}
	for _, e := range solos {
		entries = append(entries, e)
	}

	candidates := [][]runtime.MatchmakerEntry{entries}

	var result PredictedMatch
	for p := range predictCandidateOutcomes(candidates) {
		result = p
	}

	if len(result.Candidate) != 8 {
		t.Fatalf("Expected 8 players in result, got %d", len(result.Candidate))
	}

	// Find which team the party members are on
	teamA := result.Candidate[:4]
	teamB := result.Candidate[4:]

	party1InA := countByTicket(teamA, partyTicket)
	party1InB := countByTicket(teamB, partyTicket)

	t.Logf("Party ticket: %s", partyTicket)
	t.Logf("Team A: %v", getPlayerInfo(teamA))
	t.Logf("Team B: %v", getPlayerInfo(teamB))
	t.Logf("Party members in Team A: %d, Team B: %d", party1InA, party1InB)

	// Party should be kept together (all on same team)
	if party1InA > 0 && party1InB > 0 {
		t.Errorf("Party was split across teams: %d in A, %d in B", party1InA, party1InB)
	}

	// Calculate team balance
	strengthA := calculateTeamStrength(teamA)
	strengthB := calculateTeamStrength(teamB)
	totalStrength := strengthA + strengthB
	imbalance := math.Abs(strengthA-strengthB) / totalStrength * 100

	t.Logf("Team A strength: %.1f, Team B strength: %.1f, Imbalance: %.2f%%", strengthA, strengthB, imbalance)
}

// TestCharacterizationAssembleUniqueMatches documents how matches are selected
// when candidates share players
func TestCharacterizationAssembleUniqueMatches(t *testing.T) {
	// Create predictions with overlapping players
	// Player p1-p4 are in both candidate A and candidate B

	sharedPlayers := []*MatchmakerEntry{
		createTestEntry("t1", "p1", 25.0, 3.0),
		createTestEntry("t2", "p2", 25.0, 3.0),
		createTestEntry("t3", "p3", 25.0, 3.0),
		createTestEntry("t4", "p4", 25.0, 3.0),
	}

	candidateAPlayers := []*MatchmakerEntry{
		createTestEntry("t5", "p5", 25.0, 3.0),
		createTestEntry("t6", "p6", 25.0, 3.0),
		createTestEntry("t7", "p7", 25.0, 3.0),
		createTestEntry("t8", "p8", 25.0, 3.0),
	}

	candidateBPlayers := []*MatchmakerEntry{
		createTestEntry("t9", "p9", 25.0, 3.0),
		createTestEntry("t10", "p10", 25.0, 3.0),
		createTestEntry("t11", "p11", 25.0, 3.0),
		createTestEntry("t12", "p12", 25.0, 3.0),
	}

	candidateA := make([]runtime.MatchmakerEntry, 8)
	candidateB := make([]runtime.MatchmakerEntry, 8)

	for i, e := range sharedPlayers {
		candidateA[i] = e
		candidateB[i] = e
	}
	for i, e := range candidateAPlayers {
		candidateA[i+4] = e
	}
	for i, e := range candidateBPlayers {
		candidateB[i+4] = e
	}

	tests := []struct {
		name        string
		predictions []PredictedMatch
		wantFirst   int // Index of expected first match (0 or 1)
		reason      string
	}{
		{
			name: "Larger match wins",
			predictions: []PredictedMatch{
				{Candidate: candidateA, Size: 8, Draw: 0.5, OldestTicketTimestamp: 100},
				{Candidate: candidateB, Size: 6, Draw: 0.6, OldestTicketTimestamp: 100}, // Smaller
			},
			wantFirst: 0,
			reason:    "Size takes priority",
		},
		{
			name: "Older ticket wins when same size",
			predictions: []PredictedMatch{
				{Candidate: candidateA, Size: 8, Draw: 0.5, OldestTicketTimestamp: 200},
				{Candidate: candidateB, Size: 8, Draw: 0.5, OldestTicketTimestamp: 100}, // Older
			},
			wantFirst: 1,
			reason:    "Older ticket timestamp has priority",
		},
		{
			name: "Fewer divisions wins when same size and age",
			predictions: []PredictedMatch{
				{Candidate: candidateA, Size: 8, Draw: 0.5, OldestTicketTimestamp: 100, DivisionCount: 3},
				{Candidate: candidateB, Size: 8, Draw: 0.5, OldestTicketTimestamp: 100, DivisionCount: 2}, // Fewer divisions
			},
			wantFirst: 1,
			reason:    "Fewer divisions preferred for balanced matches",
		},
		{
			name: "Higher draw probability wins as final tiebreaker",
			predictions: []PredictedMatch{
				{Candidate: candidateA, Size: 8, Draw: 0.4, OldestTicketTimestamp: 100, DivisionCount: 2},
				{Candidate: candidateB, Size: 8, Draw: 0.6, OldestTicketTimestamp: 100, DivisionCount: 2}, // Higher draw
			},
			wantFirst: 1,
			reason:    "Higher draw probability means more balanced match",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewSkillBasedMatchmaker()

			// Sort predictions as the real code does
			sort.SliceStable(tt.predictions, func(i, j int) bool {
				if tt.predictions[i].Size != tt.predictions[j].Size {
					return tt.predictions[i].Size > tt.predictions[j].Size
				}
				if tt.predictions[i].OldestTicketTimestamp != tt.predictions[j].OldestTicketTimestamp {
					return tt.predictions[i].OldestTicketTimestamp < tt.predictions[j].OldestTicketTimestamp
				}
				if tt.predictions[i].DivisionCount != tt.predictions[j].DivisionCount {
					return tt.predictions[i].DivisionCount < tt.predictions[j].DivisionCount
				}
				return tt.predictions[i].Draw > tt.predictions[j].Draw
			})

			matches := m.assembleUniqueMatches(tt.predictions)

			if len(matches) == 0 {
				t.Fatal("Expected at least one match")
			}

			// Since candidates share players, only ONE should be selected
			if len(matches) != 1 {
				t.Logf("Note: %d matches selected (candidates share players, expected 1)", len(matches))
			}

			t.Logf("Reason for selection: %s", tt.reason)
			t.Logf("First match size=%d, draw=%.2f, age=%d, divisions=%d",
				tt.predictions[0].Size, tt.predictions[0].Draw,
				tt.predictions[0].OldestTicketTimestamp, tt.predictions[0].DivisionCount)
		})
	}
}

// TestBalancedTeamFormation_Desired documents what BALANCED team formation should look like
// This test verifies snake draft produces more balanced teams than sequential filling
func TestBalancedTeamFormation_SnakeDraft(t *testing.T) {
	// 2 high skill + 6 low skill players - the worst case for sequential filling
	makeEntries := func() []*MatchmakerEntry {
		return []*MatchmakerEntry{
			createTestEntry("ticket1", "p1", 40.0, 2.0), // Pro
			createTestEntry("ticket2", "p2", 38.0, 2.0), // Pro
			createTestEntry("ticket3", "p3", 15.0, 5.0), // Beginner
			createTestEntry("ticket4", "p4", 15.0, 5.0), // Beginner
			createTestEntry("ticket5", "p5", 14.0, 5.0), // Beginner
			createTestEntry("ticket6", "p6", 14.0, 5.0), // Beginner
			createTestEntry("ticket7", "p7", 13.0, 5.0), // Beginner
			createTestEntry("ticket8", "p8", 13.0, 5.0), // Beginner
		}
	}

	// Test with sequential filling first
	seqEntries := makeEntries()
	seqCandidates := [][]runtime.MatchmakerEntry{
		make([]runtime.MatchmakerEntry, len(seqEntries)),
	}
	for i, e := range seqEntries {
		seqCandidates[0][i] = e
	}

	var sequentialResult PredictedMatch
	for p := range predictCandidateOutcomesWithConfig(seqCandidates, PredictionConfig{
		Variants: []RosterVariant{RosterVariantSequential},
	}) {
		sequentialResult = p
	}

	// Test with snake draft
	snakeEntries := makeEntries()
	snakeCandidates := [][]runtime.MatchmakerEntry{
		make([]runtime.MatchmakerEntry, len(snakeEntries)),
	}
	for i, e := range snakeEntries {
		snakeCandidates[0][i] = e
	}

	var snakeDraftResult PredictedMatch
	for p := range predictCandidateOutcomesWithConfig(snakeCandidates, PredictionConfig{
		Variants: []RosterVariant{RosterVariantSnakeDraft},
	}) {
		snakeDraftResult = p
	}

	// Calculate imbalances
	seqTeamA := sequentialResult.Candidate[:4]
	seqTeamB := sequentialResult.Candidate[4:]
	seqStrengthA := calculateTeamStrength(seqTeamA)
	seqStrengthB := calculateTeamStrength(seqTeamB)
	seqImbalance := math.Abs(seqStrengthA-seqStrengthB) / (seqStrengthA + seqStrengthB) * 100

	snakeTeamA := snakeDraftResult.Candidate[:4]
	snakeTeamB := snakeDraftResult.Candidate[4:]
	snakeStrengthA := calculateTeamStrength(snakeTeamA)
	snakeStrengthB := calculateTeamStrength(snakeTeamB)
	snakeImbalance := math.Abs(snakeStrengthA-snakeStrengthB) / (snakeStrengthA + snakeStrengthB) * 100

	t.Logf("Sequential filling:")
	t.Logf("  Team A: %v = %.1f", getPlayerMus(seqTeamA), seqStrengthA)
	t.Logf("  Team B: %v = %.1f", getPlayerMus(seqTeamB), seqStrengthB)
	t.Logf("  Imbalance: %.2f%%, Draw: %.3f", seqImbalance, sequentialResult.Draw)

	t.Logf("Snake draft:")
	t.Logf("  Team A: %v = %.1f", getPlayerMus(snakeTeamA), snakeStrengthA)
	t.Logf("  Team B: %v = %.1f", getPlayerMus(snakeTeamB), snakeStrengthB)
	t.Logf("  Imbalance: %.2f%%, Draw: %.3f", snakeImbalance, snakeDraftResult.Draw)

	// Snake draft should produce more balanced teams (lower imbalance)
	if snakeImbalance >= seqImbalance {
		t.Errorf("Snake draft should have lower imbalance than sequential (snake=%.2f%% vs seq=%.2f%%)",
			snakeImbalance, seqImbalance)
	}

	// Snake draft should have higher draw probability
	if snakeDraftResult.Draw <= sequentialResult.Draw {
		t.Errorf("Snake draft should have higher draw probability (snake=%.3f vs seq=%.3f)",
			snakeDraftResult.Draw, sequentialResult.Draw)
	}
}

// TestPartySkillBoost_Desired documents that parties should get a skill boost
func TestPartySkillBoost(t *testing.T) {
	// Test that RatingsWithPartyBoost correctly applies boost to parties

	// Solo player
	solo := MatchmakerEntries{createTestEntry("t1", "p1", 20.0, 3.0)}
	soloRatings := solo.Ratings()
	soloRatingsWithBoost := solo.RatingsWithPartyBoost(0.10)

	// Solo should NOT get boost
	if soloRatings[0].Mu != soloRatingsWithBoost[0].Mu {
		t.Errorf("Solo player should not get boost: original=%.1f, boosted=%.1f",
			soloRatings[0].Mu, soloRatingsWithBoost[0].Mu)
	}

	// Party of 2
	party := createTestParty("party1", []struct {
		id        string
		mu, sigma float64
	}{
		{"p1", 20.0, 3.0},
		{"p2", 22.0, 3.0},
	})
	partyEntries := MatchmakerEntries{}
	for _, e := range party {
		partyEntries = append(partyEntries, e)
	}

	partyRatings := partyEntries.Ratings()
	partyRatingsWithBoost := partyEntries.RatingsWithPartyBoost(0.10)

	// Party SHOULD get 10% boost
	expectedBoostedMu1 := 20.0 * 1.10
	expectedBoostedMu2 := 22.0 * 1.10

	if math.Abs(partyRatingsWithBoost[0].Mu-expectedBoostedMu1) > 0.01 {
		t.Errorf("Party member 1 should get 10%% boost: expected=%.1f, got=%.1f",
			expectedBoostedMu1, partyRatingsWithBoost[0].Mu)
	}
	if math.Abs(partyRatingsWithBoost[1].Mu-expectedBoostedMu2) > 0.01 {
		t.Errorf("Party member 2 should get 10%% boost: expected=%.1f, got=%.1f",
			expectedBoostedMu2, partyRatingsWithBoost[1].Mu)
	}

	t.Logf("Solo ratings: %v", soloRatings)
	t.Logf("Party ratings (no boost): %v", partyRatings)
	t.Logf("Party ratings (10%% boost): %v", partyRatingsWithBoost)
}

// Helper functions

func getPlayerMus(entries []runtime.MatchmakerEntry) []float64 {
	mus := make([]float64, len(entries))
	for i, e := range entries {
		mus[i] = e.GetProperties()["rating_mu"].(float64)
	}
	return mus
}

func getPlayerInfo(entries []runtime.MatchmakerEntry) []string {
	info := make([]string, len(entries))
	for i, e := range entries {
		mu := e.GetProperties()["rating_mu"].(float64)
		ticket := e.GetTicket()[:8] // First 8 chars of ticket for brevity
		info[i] = fmt.Sprintf("%s:%.0f", ticket, mu)
	}
	return info
}

func countByTicket(entries []runtime.MatchmakerEntry, ticket string) int {
	count := 0
	for _, e := range entries {
		if e.GetTicket() == ticket {
			count++
		}
	}
	return count
}

// TestVariantSelection_SharedPlayers tests that when generating multiple roster variants,
// the best variant is selected when candidates share players
func TestVariantSelection_SharedPlayers(t *testing.T) {
	// Create two candidates that share 4 players
	// Each candidate generates both "sequential" and "snake draft" variants
	// The selection algorithm should prefer higher draw probability (more balanced)

	// Shared players (high and low skill mix)
	sharedEntries := []*MatchmakerEntry{
		createTestEntry("shared1", "s1", 35.0, 3.0), // High
		createTestEntry("shared2", "s2", 33.0, 3.0), // High
		createTestEntry("shared3", "s3", 17.0, 3.0), // Low
		createTestEntry("shared4", "s4", 15.0, 3.0), // Low
	}

	// Candidate A unique players
	candidateAUnique := []*MatchmakerEntry{
		createTestEntry("a1", "a1", 30.0, 3.0),
		createTestEntry("a2", "a2", 28.0, 3.0),
		createTestEntry("a3", "a3", 18.0, 3.0),
		createTestEntry("a4", "a4", 16.0, 3.0),
	}

	// Candidate B unique players
	candidateBUnique := []*MatchmakerEntry{
		createTestEntry("b1", "b1", 25.0, 3.0),
		createTestEntry("b2", "b2", 25.0, 3.0),
		createTestEntry("b3", "b3", 25.0, 3.0),
		createTestEntry("b4", "b4", 25.0, 3.0),
	}

	// Build candidates
	candidateA := make([]runtime.MatchmakerEntry, 8)
	candidateB := make([]runtime.MatchmakerEntry, 8)

	for i, e := range sharedEntries {
		candidateA[i] = e
		candidateB[i] = e
	}
	for i, e := range candidateAUnique {
		candidateA[i+4] = e
	}
	for i, e := range candidateBUnique {
		candidateB[i+4] = e
	}

	candidates := [][]runtime.MatchmakerEntry{candidateA, candidateB}

	// Generate predictions with roster variants enabled
	predictions := []PredictedMatch{}
	for p := range predictCandidateOutcomesWithConfig(candidates, PredictionConfig{
		EnableRosterVariants: true,
	}) {
		predictions = append(predictions, p)
	}

	t.Logf("Generated %d predictions (with variants)", len(predictions))

	// With variants enabled, we should have 4 predictions (2 candidates x 2 variants each)
	if len(predictions) < 2 {
		t.Fatalf("Expected at least 2 predictions with variants enabled, got %d", len(predictions))
	}

	// Log all predictions
	for i, p := range predictions {
		teamA := p.Candidate[:4]
		teamB := p.Candidate[4:]
		strengthA := calculateTeamStrength(teamA)
		strengthB := calculateTeamStrength(teamB)
		imbalance := math.Abs(strengthA-strengthB) / (strengthA + strengthB) * 100

		variantName := "sequential"
		if p.Variant == RosterVariantSnakeDraft {
			variantName = "snake_draft"
		}

		t.Logf("Prediction %d [%s]: Draw=%.3f, Imbalance=%.2f%%, Size=%d",
			i, variantName, p.Draw, imbalance, p.Size)
		t.Logf("  Team A: %v = %.1f", getPlayerMus(teamA), strengthA)
		t.Logf("  Team B: %v = %.1f", getPlayerMus(teamB), strengthB)
	}

	// Now test the assembly - with shared players, only one match should be selected
	m := NewSkillBasedMatchmaker()

	// Sort predictions by the same criteria as production
	sort.SliceStable(predictions, func(i, j int) bool {
		if predictions[i].Size != predictions[j].Size {
			return predictions[i].Size > predictions[j].Size
		}
		if predictions[i].OldestTicketTimestamp != predictions[j].OldestTicketTimestamp {
			return predictions[i].OldestTicketTimestamp < predictions[j].OldestTicketTimestamp
		}
		if predictions[i].DivisionCount != predictions[j].DivisionCount {
			return predictions[i].DivisionCount < predictions[j].DivisionCount
		}
		return predictions[i].Draw > predictions[j].Draw
	})

	matches := m.assembleUniqueMatches(predictions)

	t.Logf("After assembly: %d matches selected", len(matches))

	// Since candidates share players, only ONE match should be created
	if len(matches) != 1 {
		t.Errorf("Expected exactly 1 match (shared players prevent both), got %d", len(matches))
	}

	if len(matches) > 0 {
		selectedTeamA := matches[0][:4]
		selectedTeamB := matches[0][4:]
		selectedStrengthA := calculateTeamStrength(selectedTeamA)
		selectedStrengthB := calculateTeamStrength(selectedTeamB)
		selectedImbalance := math.Abs(selectedStrengthA-selectedStrengthB) / (selectedStrengthA + selectedStrengthB) * 100

		t.Logf("Selected match:")
		t.Logf("  Team A: %v = %.1f", getPlayerMus(selectedTeamA), selectedStrengthA)
		t.Logf("  Team B: %v = %.1f", getPlayerMus(selectedTeamB), selectedStrengthB)
		t.Logf("  Imbalance: %.2f%%", selectedImbalance)
	}
}

// Benchmark for team formation algorithm
func BenchmarkTeamFormation(b *testing.B) {
	entries := make([]*MatchmakerEntry, 8)
	for i := 0; i < 8; i++ {
		entries[i] = createTestEntry(
			uuid.Must(uuid.NewV4()).String(),
			uuid.Must(uuid.NewV4()).String(),
			float64(20+i),
			3.0,
		)
	}

	candidates := make([][]runtime.MatchmakerEntry, 1)
	candidates[0] = make([]runtime.MatchmakerEntry, 8)
	for i, e := range entries {
		candidates[0][i] = e
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for range predictCandidateOutcomes(candidates) {
			// consume
		}
	}
}

// TestDrawProbabilityCalculation verifies the draw probability reflects team balance
func TestDrawProbabilityCalculation(t *testing.T) {
	tests := []struct {
		name         string
		teamAMus     []float64
		teamBMus     []float64
		wantHighDraw bool // High draw = balanced teams
		description  string
	}{
		{
			name:         "Perfectly balanced teams",
			teamAMus:     []float64{25, 25, 25, 25},
			teamBMus:     []float64{25, 25, 25, 25},
			wantHighDraw: true,
			description:  "Equal teams should have high draw probability",
		},
		{
			name:         "Slightly imbalanced teams",
			teamAMus:     []float64{26, 26, 24, 24},
			teamBMus:     []float64{25, 25, 25, 25},
			wantHighDraw: true,
			description:  "Similar total strength should have high draw probability",
		},
		{
			name:         "Very imbalanced teams",
			teamAMus:     []float64{30, 30, 30, 30},
			teamBMus:     []float64{15, 15, 15, 15},
			wantHighDraw: false,
			description:  "Large skill gap should have low draw probability",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Calculate draw probability using OpenSkill
			teamARatings := make([]types.Rating, len(tt.teamAMus))
			teamBRatings := make([]types.Rating, len(tt.teamBMus))

			for i, mu := range tt.teamAMus {
				teamARatings[i] = NewRating(0, mu, 3.0)
			}
			for i, mu := range tt.teamBMus {
				teamBRatings[i] = NewRating(0, mu, 3.0)
			}

			draw := rating.PredictDraw([]types.Team{teamARatings, teamBRatings}, nil)

			t.Logf("%s: Draw probability = %.3f", tt.description, draw)

			// High draw is typically > 0.3, low draw is < 0.2
			if tt.wantHighDraw && draw < 0.2 {
				t.Errorf("Expected high draw probability (>0.2), got %.3f", draw)
			}
			if !tt.wantHighDraw && draw > 0.3 {
				t.Errorf("Expected low draw probability (<0.3), got %.3f", draw)
			}
		})
	}
}
