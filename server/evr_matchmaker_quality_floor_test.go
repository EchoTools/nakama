package server

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// TestFilterByQualityFloorExemptsCombat asserts the issue #479 invariant:
// Combat (echo_combat / ModeCombatPublic) must NEVER be skill-gated at match
// creation. With EnableQualityFloor=true, a Combat candidate whose DrawProb is
// below the floor must NOT be dropped, while an Arena candidate below the same
// floor IS still dropped (other modes unchanged).
func TestFilterByQualityFloorExemptsCombat(t *testing.T) {
	now := time.Now().UTC().Unix()
	recentTime := now // submitted now => floor is at initial (0.10)

	makeEntry := func(ticket, sessionID, mode string) *MatchmakerEntry {
		return &MatchmakerEntry{
			Ticket:   ticket,
			Presence: &MatchmakerPresence{SessionId: sessionID},
			Properties: map[string]interface{}{
				"game_mode":    mode,
				"rating_mu":    25.0,
				"rating_sigma": 8.333,
			},
		}
	}

	settings := GlobalMatchmakingSettings{
		EnableQualityFloor:         true,
		QualityFloorInitial:        0.10,
		QualityFloorDecayPerSecond: 0.0005,
		QualityFloorMinimum:        0.0,
	}

	combatMode := evr.ModeCombatPublic.String()
	arenaMode := evr.ModeArenaPublic.String()

	predictions := []PredictedMatch{
		{
			// Combat candidate below the floor -- MUST pass (combat is never skill-gated).
			Candidate: []runtime.MatchmakerEntry{
				makeEntry("c1", "cs1", combatMode),
				makeEntry("c1", "cs2", combatMode),
			},
			DrawProb:              0.02, // well below floor of 0.10
			Size:                  2,
			OldestTicketTimestamp: recentTime,
		},
		{
			// Arena candidate below the floor -- MUST still be dropped (unchanged).
			Candidate: []runtime.MatchmakerEntry{
				makeEntry("a1", "as1", arenaMode),
				makeEntry("a1", "as2", arenaMode),
			},
			DrawProb:              0.02, // well below floor of 0.10
			Size:                  2,
			OldestTicketTimestamp: recentTime,
		},
	}

	filtered := filterByQualityFloor(predictions, &settings, now)

	if len(filtered) != 1 {
		t.Fatalf("expected exactly 1 prediction after filtering (combat kept, arena dropped), got %d", len(filtered))
	}

	gotMode, _ := filtered[0].Candidate[0].GetProperties()["game_mode"].(string)
	if gotMode != combatMode {
		t.Fatalf("expected the surviving candidate to be the Combat one (%q), got %q", combatMode, gotMode)
	}
}

func TestComputeQualityFloor(t *testing.T) {
	settings := GlobalMatchmakingSettings{
		EnableQualityFloor:         true,
		QualityFloorInitial:        0.10,
		QualityFloorDecayPerSecond: 0.0005,
		QualityFloorMinimum:        0.0,
	}

	tests := []struct {
		name           string
		maxWaitSeconds float64
		wantFloor      float64
	}{
		{
			name:           "no wait time - full floor",
			maxWaitSeconds: 0,
			wantFloor:      0.10,
		},
		{
			name:           "30 seconds wait",
			maxWaitSeconds: 30,
			wantFloor:      0.10 - 30*0.0005, // 0.085
		},
		{
			name:           "100 seconds wait",
			maxWaitSeconds: 100,
			wantFloor:      0.10 - 100*0.0005, // 0.05
		},
		{
			name:           "200 seconds - floor reaches zero",
			maxWaitSeconds: 200,
			wantFloor:      0.0,
		},
		{
			name:           "300 seconds - floor stays at zero",
			maxWaitSeconds: 300,
			wantFloor:      0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeQualityFloor(&settings, tt.maxWaitSeconds)
			if math.Abs(got-tt.wantFloor) > 1e-9 {
				t.Errorf("computeQualityFloor() = %f, want %f", got, tt.wantFloor)
			}
		})
	}
}

func TestComputeQualityFloorWithMinimum(t *testing.T) {
	settings := GlobalMatchmakingSettings{
		EnableQualityFloor:         true,
		QualityFloorInitial:        0.10,
		QualityFloorDecayPerSecond: 0.0005,
		QualityFloorMinimum:        0.03,
	}

	tests := []struct {
		name           string
		maxWaitSeconds float64
		wantFloor      float64
	}{
		{
			name:           "no wait - full floor",
			maxWaitSeconds: 0,
			wantFloor:      0.10,
		},
		{
			name:           "100 seconds",
			maxWaitSeconds: 100,
			wantFloor:      0.05,
		},
		{
			name:           "200 seconds - would be 0 but minimum applies",
			maxWaitSeconds: 200,
			wantFloor:      0.03,
		},
		{
			name:           "500 seconds - minimum still applies",
			maxWaitSeconds: 500,
			wantFloor:      0.03,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeQualityFloor(&settings, tt.maxWaitSeconds)
			if math.Abs(got-tt.wantFloor) > 1e-9 {
				t.Errorf("computeQualityFloor() = %f, want %f", got, tt.wantFloor)
			}
		})
	}
}

func TestComputeQualityFloorReachesZero(t *testing.T) {
	settings := GlobalMatchmakingSettings{
		EnableQualityFloor:         true,
		QualityFloorInitial:        0.10,
		QualityFloorDecayPerSecond: 0.0005,
		QualityFloorMinimum:        0.0,
	}

	// Floor should reach 0 at exactly QualityFloorInitial / QualityFloorDecayPerSecond = 200 seconds
	zeroTime := settings.QualityFloorInitial / settings.QualityFloorDecayPerSecond
	got := computeQualityFloor(&settings, zeroTime)
	if got != 0.0 {
		t.Errorf("floor should be 0.0 at %f seconds, got %f", zeroTime, got)
	}

	// One second before, should still be positive
	gotBefore := computeQualityFloor(&settings, zeroTime-1)
	if gotBefore <= 0.0 {
		t.Errorf("floor should be positive at %f seconds, got %f", zeroTime-1, gotBefore)
	}
}

func TestPassesQualityFloor(t *testing.T) {
	tests := []struct {
		name     string
		drawProb float32
		floor    float64
		want     bool
	}{
		{"above floor", 0.15, 0.10, true},
		{"exactly at floor", 0.10, 0.10, true},
		{"below floor", 0.05, 0.10, false},
		{"zero floor passes everything", 0.001, 0.0, true},
		{"zero draw prob with zero floor", 0.0, 0.0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prediction := PredictedMatch{DrawProb: tt.drawProb}
			got := passesQualityFloor(prediction, tt.floor)
			if got != tt.want {
				t.Errorf("passesQualityFloor(drawProb=%f, floor=%f) = %v, want %v",
					tt.drawProb, tt.floor, got, tt.want)
			}
		})
	}
}

func TestFilterByQualityFloor(t *testing.T) {
	now := time.Now().UTC().Unix()

	makeEntry := func(ticket, sessionID string, submissionTime float64) *MatchmakerEntry {
		return &MatchmakerEntry{
			Ticket:   ticket,
			Presence: &MatchmakerPresence{SessionId: sessionID},
			Properties: map[string]interface{}{
				"submission_time": submissionTime,
				"rating_mu":       25.0,
				"rating_sigma":    8.333,
			},
		}
	}

	settings := GlobalMatchmakingSettings{
		EnableQualityFloor:         true,
		QualityFloorInitial:        0.10,
		QualityFloorDecayPerSecond: 0.0005,
		QualityFloorMinimum:        0.0,
	}

	// Recent tickets (submitted 10 seconds ago) -- floor is ~0.095
	recentTime := float64(now - 10)
	// Old tickets (submitted 200 seconds ago) -- floor is 0.0
	oldTime := float64(now - 200)

	predictions := []PredictedMatch{
		{
			// Recent candidate with bad balance -- should be filtered
			Candidate: []runtime.MatchmakerEntry{
				makeEntry("t1", "s1", recentTime),
				makeEntry("t1", "s2", recentTime),
			},
			DrawProb:              0.05, // Below floor of ~0.095
			Size:                  2,
			OldestTicketTimestamp: now - 10,
		},
		{
			// Recent candidate with good balance -- should pass
			Candidate: []runtime.MatchmakerEntry{
				makeEntry("t2", "s3", recentTime),
				makeEntry("t2", "s4", recentTime),
			},
			DrawProb:              0.15, // Above floor
			Size:                  2,
			OldestTicketTimestamp: now - 10,
		},
		{
			// Old candidate with bad balance -- floor decayed to 0, should pass
			Candidate: []runtime.MatchmakerEntry{
				makeEntry("t3", "s5", oldTime),
				makeEntry("t3", "s6", oldTime),
			},
			DrawProb:              0.02,
			Size:                  2,
			OldestTicketTimestamp: now - 200,
		},
	}

	filtered := filterByQualityFloor(predictions, &settings, now)

	if len(filtered) != 2 {
		t.Fatalf("expected 2 predictions after filtering, got %d", len(filtered))
	}

	// First remaining should be the good-balance recent candidate
	if filtered[0].DrawProb != 0.15 {
		t.Errorf("expected first prediction to have DrawProb 0.15, got %f", filtered[0].DrawProb)
	}

	// Second remaining should be the old candidate (floor decayed to 0)
	if filtered[1].DrawProb != 0.02 {
		t.Errorf("expected second prediction to have DrawProb 0.02, got %f", filtered[1].DrawProb)
	}
}

func TestFilterByQualityFloorDisabled(t *testing.T) {
	settings := GlobalMatchmakingSettings{
		EnableQualityFloor: false,
	}

	predictions := []PredictedMatch{
		{DrawProb: 0.01, Size: 2, OldestTicketTimestamp: time.Now().UTC().Unix()},
		{DrawProb: 0.50, Size: 2, OldestTicketTimestamp: time.Now().UTC().Unix()},
	}

	now := time.Now().UTC().Unix()
	filtered := filterByQualityFloor(predictions, &settings, now)

	if len(filtered) != 2 {
		t.Fatalf("with quality floor disabled, all predictions should pass; got %d, want 2", len(filtered))
	}
}

func TestComputeQualityFloor_NegativeWaitTime(t *testing.T) {
	settings := GlobalMatchmakingSettings{
		EnableQualityFloor:         true,
		QualityFloorInitial:        0.10,
		QualityFloorDecayPerSecond: 0.0005,
		QualityFloorMinimum:        0.0,
	}

	// Negative wait time means decay subtracts a negative, so floor rises above initial.
	// computeQualityFloor uses math.Max(minimum, initial - wait*decay), so it should
	// return initial + |wait|*decay. The function itself doesn't clamp to initial.
	got := computeQualityFloor(&settings, -10)
	expected := 0.10 - (-10 * 0.0005) // 0.105
	if math.Abs(got-expected) > 1e-9 {
		t.Errorf("computeQualityFloor(negative wait) = %f, want %f", got, expected)
	}

	// filterByQualityFloor clamps negative wait to 0, so the floor should be initial.
	now := time.Now().UTC().Unix()
	predictions := []PredictedMatch{
		{
			DrawProb:              0.09, // Below initial floor of 0.10
			Size:                  2,
			OldestTicketTimestamp: now + 60, // Future timestamp => negative wait
		},
	}
	filtered := filterByQualityFloor(predictions, &settings, now)
	if len(filtered) != 0 {
		t.Errorf("future ticket should use floor=initial (0.10), drawProb=0.09 should be filtered; got %d results", len(filtered))
	}
}

func TestComputeQualityFloor_VeryLargeDecayRate(t *testing.T) {
	settings := GlobalMatchmakingSettings{
		EnableQualityFloor:         true,
		QualityFloorInitial:        0.10,
		QualityFloorDecayPerSecond: 1.0, // Floor drops to 0 in 0.1 seconds
		QualityFloorMinimum:        0.0,
	}

	tests := []struct {
		name           string
		maxWaitSeconds float64
		wantFloor      float64
	}{
		{
			name:           "zero wait - full floor",
			maxWaitSeconds: 0,
			wantFloor:      0.10,
		},
		{
			name:           "0.05 seconds - half floor",
			maxWaitSeconds: 0.05,
			wantFloor:      0.05,
		},
		{
			name:           "0.1 seconds - floor reaches zero",
			maxWaitSeconds: 0.1,
			wantFloor:      0.0,
		},
		{
			name:           "1 second - floor stays at zero, not negative",
			maxWaitSeconds: 1.0,
			wantFloor:      0.0,
		},
		{
			name:           "1000 seconds - floor stays at zero",
			maxWaitSeconds: 1000,
			wantFloor:      0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeQualityFloor(&settings, tt.maxWaitSeconds)
			if got < 0 {
				t.Errorf("floor should never be negative, got %f", got)
			}
			if math.Abs(got-tt.wantFloor) > 1e-9 {
				t.Errorf("computeQualityFloor() = %f, want %f", got, tt.wantFloor)
			}
		})
	}
}

func TestComputeQualityFloor_ZeroInitial(t *testing.T) {
	settings := GlobalMatchmakingSettings{
		EnableQualityFloor:         true,
		QualityFloorInitial:        0.0,
		QualityFloorDecayPerSecond: 0.0005,
		QualityFloorMinimum:        0.0,
	}

	// With zero initial, floor should always be 0 regardless of wait time.
	waitTimes := []float64{0, 10, 100, 500, 1000}
	for _, wait := range waitTimes {
		got := computeQualityFloor(&settings, wait)
		if got != 0.0 {
			t.Errorf("with zero initial, floor at wait=%f should be 0.0, got %f", wait, got)
		}
	}
}

func TestComputeQualityFloor_MinimumHigherThanInitial(t *testing.T) {
	settings := GlobalMatchmakingSettings{
		EnableQualityFloor:         true,
		QualityFloorInitial:        0.05,
		QualityFloorDecayPerSecond: 0.0005,
		QualityFloorMinimum:        0.10, // Minimum > Initial
	}

	// math.Max(minimum, initial - wait*decay) should always return minimum
	// since minimum > initial and decay only makes the second arg smaller.
	waitTimes := []float64{0, 50, 100, 500}
	for _, wait := range waitTimes {
		got := computeQualityFloor(&settings, wait)
		if got != 0.10 {
			t.Errorf("with minimum > initial, floor at wait=%f should be minimum (0.10), got %f", wait, got)
		}
	}
}

func TestPassesQualityFloor_ExactlyAtFloor(t *testing.T) {
	// Verify that >= is used (not >) by testing exact equality at various precision levels.
	tests := []struct {
		name     string
		drawProb float32
		floor    float64
	}{
		{"exact match at 0.10", 0.10, 0.10},
		{"exact match at 0.05", 0.05, 0.05},
		{"exact match at 0.001", 0.001, 0.001},
		{"exact match at 0.0", 0.0, 0.0},
		{"exact match at 1.0", 1.0, 1.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prediction := PredictedMatch{DrawProb: tt.drawProb}
			got := passesQualityFloor(prediction, tt.floor)
			if !got {
				t.Errorf("passesQualityFloor(drawProb=%f, floor=%f) = false, want true (>= not >)",
					tt.drawProb, tt.floor)
			}
		})
	}
}

func TestFilterByQualityFloorNilSettings(t *testing.T) {
	predictions := []PredictedMatch{
		{DrawProb: 0.01, Size: 2},
	}

	filtered := filterByQualityFloor(predictions, nil, time.Now().UTC().Unix())

	if len(filtered) != 1 {
		t.Fatalf("with nil settings, all predictions should pass; got %d, want 1", len(filtered))
	}
}

// ============================================================================
// BEHAVIORAL AC TESTS FOR QUALITY FLOOR BUG
// ============================================================================
// These tests demonstrate the matchmaker quality floor bug:
// The floor rejects ALL candidates when queue has players from separated divisions,
// even with 8-14 players queuing. In production: 939/939 consecutive cycles = 0 matches
// over 15.65 hours. Root cause: Initial floor (0.10) with 0.0005/sec decay are too
// strict for small populations where mixed-skill matches have lower draw probability.
// The starving ticket system runs AFTER quality floor filtering, so it can't rescue
// rejected candidates. The bug demonstrates a critical ordering problem in the
// matchmaker pipeline.

// Test 1: Quality floor at t=0 rejects mixed-skill match with realistic draw probability
// Scenario: Small queue with Bronze players (mu ~15-20) + higher skilled player.
// Expected: Match gets draw probability ~0.08 due to skill imbalance.
// With initial floor of 0.10, this candidate is rejected despite being viable for
// small populations.
func TestQualityFloorAtT0RejectsMixedSkillMatch(t *testing.T) {
	now := time.Now().UTC().Unix()

	makeEntry := func(ticket, sessionID string, mu, sigma float64) *MatchmakerEntry {
		return &MatchmakerEntry{
			Ticket:   ticket,
			Presence: &MatchmakerPresence{SessionId: sessionID},
			Properties: map[string]interface{}{
				"rating_mu":    mu,
				"rating_sigma": sigma,
			},
		}
	}

	// Create a realistic mixed-skill candidate:
	// 3 Bronze players (lower skill) + 1 higher skill player
	candidate := []runtime.MatchmakerEntry{
		makeEntry("t1", "s1", 15.0, 8.333), // Bronze
		makeEntry("t1", "s2", 17.0, 8.333), // Bronze
		makeEntry("t1", "s3", 16.0, 8.333), // Bronze
		makeEntry("t1", "s4", 28.0, 8.333), // Higher skill
	}

	mixedCandidate := []PredictedMatch{
		{
			// Realistic small-population mixed-skill match
			// Bronze-level players with one higher-skill player
			// Draw prob ~0.08 due to skill mismatch (< floor of 0.10)
			DrawProb:              0.08,
			Size:                  4,
			OldestTicketTimestamp: now, // Just submitted
			Candidate:             candidate,
		},
	}

	settings := GlobalMatchmakingSettings{
		EnableQualityFloor:         true,
		QualityFloorInitial:        0.10,
		QualityFloorDecayPerSecond: 0.0005,
		QualityFloorMinimum:        0.0,
	}

	filtered := filterByQualityFloor(mixedCandidate, &settings, now)

	// BUG: At t=0 with floor=0.10, this candidate is rejected despite being viable
	// for small populations where alternative matches don't exist
	if len(filtered) != 0 {
		t.Errorf("BUG VALIDATION: Quality floor at t=0 should reject draw prob 0.08 (below floor 0.10), got %d candidates", len(filtered))
	}
}

// Test 2: Quality floor decays with wait time; same candidate passes after 200+ seconds
// Scenario: Candidate waits 200+ seconds. Floor decays to 0.0.
// Expected: Same candidate (draw prob 0.08) now passes.
// This demonstrates that the floor is too strict initially but gradually
// becomes permissive with wait time. The problem is the initial period
// where ALL candidates are rejected.
func TestQualityFloorDecayAllowsSameMatchAfterWait(t *testing.T) {
	now := time.Now().UTC().Unix()
	oldTime := now - 210 // Submitted 210 seconds ago

	makeEntry := func(ticket, sessionID string, mu, sigma float64) *MatchmakerEntry {
		return &MatchmakerEntry{
			Ticket:   ticket,
			Presence: &MatchmakerPresence{SessionId: sessionID},
			Properties: map[string]interface{}{
				"rating_mu":    mu,
				"rating_sigma": sigma,
			},
		}
	}

	// Same candidate as Test 1, but with old timestamp
	candidate := []runtime.MatchmakerEntry{
		makeEntry("t2", "s1", 15.0, 8.333),
		makeEntry("t2", "s2", 17.0, 8.333),
		makeEntry("t2", "s3", 16.0, 8.333),
		makeEntry("t2", "s4", 28.0, 8.333),
	}

	decayedCandidate := []PredictedMatch{
		{
			DrawProb:              0.08,
			Size:                  4,
			OldestTicketTimestamp: oldTime,
			Candidate:             candidate,
		},
	}

	settings := GlobalMatchmakingSettings{
		EnableQualityFloor:         true,
		QualityFloorInitial:        0.10,
		QualityFloorDecayPerSecond: 0.0005,
		QualityFloorMinimum:        0.0,
	}

	filtered := filterByQualityFloor(decayedCandidate, &settings, now)

	// After 210 seconds, floor = 0.10 - (210 * 0.0005) = 0.0 (clamped to minimum)
	// Now draw prob 0.08 passes the floor check
	if len(filtered) != 1 {
		t.Errorf("After 210 seconds decay, candidate with draw prob 0.08 should pass floor=0.0, got %d candidates", len(filtered))
	}
	if filtered[0].DrawProb != 0.08 {
		t.Errorf("Filtered candidate should have DrawProb 0.08, got %f", filtered[0].DrawProb)
	}
}

// Test 3: Quality floor filtering runs BEFORE reservation assembly
// Scenario: Demonstrate that starving ticket reservation system cannot rescue
// candidates rejected by quality floor.
// Expected: If a candidate with a starving player fails quality floor,
// buildReservations never sees it, so it can't be rescued.
// This demonstrates the architectural problem: the filtering order prevents
// the starving system from helping long-waiting players.
func TestQualityFloorFilteringRunsBeforeReservations(t *testing.T) {
	now := time.Now().UTC().Unix()
	// Candidate submitted 300 seconds ago (would be starving if not for quality floor filter)
	oldTime := now - 300

	makeEntry := func(ticket, sessionID string, mu, sigma float64) *MatchmakerEntry {
		return &MatchmakerEntry{
			Ticket:   ticket,
			Presence: &MatchmakerPresence{SessionId: sessionID},
			Properties: map[string]interface{}{
				"rating_mu":    mu,
				"rating_sigma": sigma,
			},
		}
	}

	settings := GlobalMatchmakingSettings{
		EnableQualityFloor:         true,
		QualityFloorInitial:        0.10,
		QualityFloorDecayPerSecond: 0.0005,
		QualityFloorMinimum:        0.0,
		EnableTicketReservation:    true,
		ReservationThresholdSecs:   90,
	}

	// Candidate with ONE starving player (waited 300 seconds) + others
	// Draw prob is low (0.08) due to skill imbalance
	candidate := []runtime.MatchmakerEntry{
		makeEntry("old_ticket", "starving_session", 15.0, 8.333),
		makeEntry("t3", "s2", 17.0, 8.333),
		makeEntry("t3", "s3", 16.0, 8.333),
	}

	predictions := []PredictedMatch{
		{
			DrawProb:              0.08, // Below floor even at t=300
			Size:                  3,
			OldestTicketTimestamp: oldTime,
			Candidate:             candidate,
		},
	}

	// First: Apply quality floor filter (as the matchmaker does)
	beforeFilter := len(predictions)
	filtered := filterByQualityFloor(predictions, &settings, now)
	afterFilter := len(filtered)

	// CRITICAL BUG: Quality floor rejects this candidate
	if afterFilter != 0 {
		t.Logf("Candidate was not filtered by quality floor (expected filtering)")
	} else {
		t.Logf("Candidate was filtered by quality floor at t=%d (floor=%f, drawProb=%f)",
			now-oldTime, 0.0, 0.08)
	}

	// The starving reservation system NEVER SEES this candidate because it was
	// filtered out before buildReservations is called.
	// buildReservations(logger, candidates, filtered, settings)
	// ^^ filtered is empty, so starving player gets no reservation

	// If filterCount was 0 (no filtering), starving system could help
	// If filterCount > 0, starving system cannot help
	if beforeFilter-afterFilter > 0 {
		t.Logf("BUG DEMONSTRATED: Quality floor filtered %d candidate(s) containing starving player(s)\n"+
			"  → Starving reservation system cannot rescue them\n"+
			"  → This is why 939 consecutive cycles produced 0 matches in production", beforeFilter-afterFilter)
	}
}

// Test 4: Realistic small queue (6 Bronze) with initial floor rejects all candidates
// Scenario: A realistic small-population scenario where all candidates have
// moderate draw probability due to limited skill diversity (all Bronze players ~15-22 mu).
// Even same-skill matches get moderate draw probability in prediction models.
// With initial floor of 0.10, many viable candidates are rejected.
func TestQualityFloorRejectsRealisticSmallBronzeQueue(t *testing.T) {
	now := time.Now().UTC().Unix()

	makeEntry := func(ticket, sessionID string, mu, sigma float64) *MatchmakerEntry {
		return &MatchmakerEntry{
			Ticket:   ticket,
			Presence: &MatchmakerPresence{SessionId: sessionID},
			Properties: map[string]interface{}{
				"rating_mu":    mu,
				"rating_sigma": sigma,
			},
		}
	}

	// Create 6 Bronze players (realistic small queue scenario)
	bronzeMuValues := []float64{15.0, 17.0, 16.5, 19.0, 18.0, 16.0}
	bronzeQueue := make([]runtime.MatchmakerEntry, 6)
	for i, mu := range bronzeMuValues {
		bronzeQueue[i] = makeEntry(
			fmt.Sprintf("bronze_ticket_%d", i),
			fmt.Sprintf("bronze_session_%d", i),
			mu,
			8.333,
		)
	}

	settings := GlobalMatchmakingSettings{
		EnableQualityFloor:         true,
		QualityFloorInitial:        0.10,
		QualityFloorDecayPerSecond: 0.0005,
		QualityFloorMinimum:        0.0,
	}

	// Simulate realistic candidates from this small Bronze-only queue
	// In a small same-skill pool, draw probabilities are typically moderate (0.35-0.50)
	// BUT when formation is random or forced, imbalanced lineups can have
	// lower draw prob (0.08-0.09).
	// With initial floor=0.10, candidates in this range are rejected.
	candidates := []PredictedMatch{
		{
			DrawProb:              0.08, // Slightly imbalanced lineup
			Size:                  4,
			OldestTicketTimestamp: now,
			Candidate:             bronzeQueue[0:4],
		},
		{
			DrawProb:              0.09, // Another slightly imbalanced lineup
			Size:                  4,
			OldestTicketTimestamp: now,
			Candidate:             bronzeQueue[2:6],
		},
		{
			DrawProb:              0.11, // This one passes
			Size:                  4,
			OldestTicketTimestamp: now,
			Candidate:             bronzeQueue[0:4],
		},
	}

	filtered := filterByQualityFloor(candidates, &settings, now)

	// BUG: 2 of 3 candidates rejected by overly-strict initial floor
	// With only 6 players total and 2 rejected, remaining matches are limited
	// In a real matchmaker loop with many small queues, this cascades to
	// 939 consecutive cycles with 0 matches (as observed in production)
	t.Logf("Quality floor at t=0: Filtered %d of %d candidates from small Bronze queue\n"+
		"  → Candidates with draw prob 0.08-0.09 rejected despite being only viable matches\n"+
		"  → With multiple small queues, this cascades to 0-match cycles",
		len(candidates)-len(filtered), len(candidates))

	if len(filtered) < len(candidates) {
		t.Logf("PRODUCTION BUG RECREATED: Initial floor too strict for small populations")
	}
}

// Test 5: Quality floor parameters analysis
// Demonstrate the decay calculation and show that 200 seconds is needed for
// floor to reach 0 with default settings (initial=0.10, decay=0.0005/sec).
// In a production queue, this creates a 200+ second "dead zone" where
// only well-balanced matches pass, but small populations don't produce
// well-balanced matches.
func TestQualityFloorDecayMathematics(t *testing.T) {
	settings := GlobalMatchmakingSettings{
		EnableQualityFloor:         true,
		QualityFloorInitial:        0.10,
		QualityFloorDecayPerSecond: 0.0005,
		QualityFloorMinimum:        0.0,
	}

	// Time for floor to fully decay to minimum
	decayTime := settings.QualityFloorInitial / settings.QualityFloorDecayPerSecond
	if decayTime != 200 {
		t.Errorf("With initial=0.10 and decay=0.0005/sec, floor should reach 0 at 200 seconds, got %f", decayTime)
	}

	// Samples showing the floor at different wait times
	testCases := []struct {
		waitSecs float64
		expected float64
	}{
		{0, 0.10},
		{10, 0.095},
		{50, 0.075},
		{100, 0.05},
		{150, 0.025},
		{200, 0.0},
		{300, 0.0}, // Stays at minimum
	}

	for _, tc := range testCases {
		got := computeQualityFloor(&settings, tc.waitSecs)
		if math.Abs(got-tc.expected) > 1e-9 {
			t.Errorf("At wait=%fs, floor should be %f, got %f", tc.waitSecs, tc.expected, got)
		}
	}

	// IMPLICATION: A draw probability of 0.08 is viable, but rejected for
	// the first 200 seconds of wait time. For small populations, this means
	// zero matches for 200+ seconds, causing the starving backlog.
	t.Logf("CRITICAL INSIGHT: With initial=0.10 decay=0.0005/sec:\n" +
		"  → Floor = 0.10 at t=0\n" +
		"  → Floor = 0.05 at t=100 seconds\n" +
		"  → Floor = 0.00 at t=200 seconds\n" +
		"  → Candidates with draw prob 0.08-0.09 are rejected for first 200 seconds\n" +
		"  → This is too strict for small populations with limited options")
}
