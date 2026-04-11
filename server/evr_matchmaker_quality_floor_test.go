package server

import (
	"math"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

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
				"rating_mu":      25.0,
				"rating_sigma":   8.333,
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
			DrawProb:             0.05, // Below floor of ~0.095
			Size:                 2,
			OldestTicketTimestamp: now - 10,
		},
		{
			// Recent candidate with good balance -- should pass
			Candidate: []runtime.MatchmakerEntry{
				makeEntry("t2", "s3", recentTime),
				makeEntry("t2", "s4", recentTime),
			},
			DrawProb:             0.15, // Above floor
			Size:                 2,
			OldestTicketTimestamp: now - 10,
		},
		{
			// Old candidate with bad balance -- floor decayed to 0, should pass
			Candidate: []runtime.MatchmakerEntry{
				makeEntry("t3", "s5", oldTime),
				makeEntry("t3", "s6", oldTime),
			},
			DrawProb:             0.02,
			Size:                 2,
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

func TestFilterByQualityFloorNilSettings(t *testing.T) {
	predictions := []PredictedMatch{
		{DrawProb: 0.01, Size: 2},
	}

	filtered := filterByQualityFloor(predictions, nil, time.Now().UTC().Unix())

	if len(filtered) != 1 {
		t.Fatalf("with nil settings, all predictions should pass; got %d, want 1", len(filtered))
	}
}
