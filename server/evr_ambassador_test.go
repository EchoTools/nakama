package server

import (
	"testing"
	"time"
)

func TestIsEligibleAmbassador(t *testing.T) {
	tests := []struct {
		name        string
		gamesPlayed int
		mu          float64
		minGames    int
		minMu       float64
		want        bool
	}{
		{
			name:        "eligible veteran",
			gamesPlayed: 300,
			mu:          35.0,
			minGames:    200,
			minMu:       30.0,
			want:        true,
		},
		{
			name:        "exactly at thresholds",
			gamesPlayed: 200,
			mu:          30.0,
			minGames:    200,
			minMu:       30.0,
			want:        true,
		},
		{
			name:        "not enough games",
			gamesPlayed: 199,
			mu:          35.0,
			minGames:    200,
			minMu:       30.0,
			want:        false,
		},
		{
			name:        "mu too low",
			gamesPlayed: 300,
			mu:          29.9,
			minGames:    200,
			minMu:       30.0,
			want:        false,
		},
		{
			name:        "both below thresholds",
			gamesPlayed: 50,
			mu:          15.0,
			minGames:    200,
			minMu:       30.0,
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsEligibleAmbassador(tt.gamesPlayed, tt.mu, tt.minGames, tt.minMu)
			if got != tt.want {
				t.Errorf("IsEligibleAmbassador(%d, %f, %d, %f) = %v, want %v",
					tt.gamesPlayed, tt.mu, tt.minGames, tt.minMu, got, tt.want)
			}
		})
	}
}

func TestShouldAmbassadorThisMatch(t *testing.T) {
	tests := []struct {
		name                string
		matchesSinceLastAmb int
		cooldown            int
		want                bool
	}{
		{
			name:                "never ambassadored (fresh state)",
			matchesSinceLastAmb: -1, // signals never used
			cooldown:            1,
			want:                true,
		},
		{
			name:                "cooldown satisfied",
			matchesSinceLastAmb: 2,
			cooldown:            1,
			want:                true,
		},
		{
			name:                "exactly at cooldown",
			matchesSinceLastAmb: 1,
			cooldown:            1,
			want:                true,
		},
		{
			name:                "still on cooldown",
			matchesSinceLastAmb: 0,
			cooldown:            1,
			want:                false,
		},
		{
			name:                "zero cooldown always allows",
			matchesSinceLastAmb: 0,
			cooldown:            0,
			want:                true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &AmbassadorState{
				IsActive:               true,
				TotalAmbassadorMatches: 5,
			}
			if tt.matchesSinceLastAmb < 0 {
				// Never ambassadored: zero last match time
				state.LastAmbassadorMatch = time.Time{}
				state.MatchesSinceLastAmbassador = 0
			} else {
				state.LastAmbassadorMatch = time.Now().Add(-time.Hour)
				state.MatchesSinceLastAmbassador = tt.matchesSinceLastAmb
			}
			got := ShouldAmbassadorThisMatch(state, tt.cooldown)
			if got != tt.want {
				t.Errorf("ShouldAmbassadorThisMatch(matchesSince=%d, cooldown=%d) = %v, want %v",
					tt.matchesSinceLastAmb, tt.cooldown, got, tt.want)
			}
		})
	}
}

func TestGetAmbassadorDivision(t *testing.T) {
	names := []string{"Bronze", "Silver", "Gold", "Diamond"}

	tests := []struct {
		name            string
		currentDivision string
		want            string
	}{
		{
			name:            "Gold drops to Silver",
			currentDivision: "Gold",
			want:            "Silver",
		},
		{
			name:            "Diamond drops to Gold",
			currentDivision: "Diamond",
			want:            "Gold",
		},
		{
			name:            "Silver drops to Bronze",
			currentDivision: "Silver",
			want:            "Bronze",
		},
		{
			name:            "Bronze stays at Bronze (lowest)",
			currentDivision: "Bronze",
			want:            "Bronze",
		},
		{
			name:            "unknown division returns empty",
			currentDivision: "Unknown",
			want:            "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetAmbassadorDivision(tt.currentDivision, names)
			if got != tt.want {
				t.Errorf("GetAmbassadorDivision(%q) = %q, want %q",
					tt.currentDivision, got, tt.want)
			}
		})
	}
}

func TestGetAmbassadorDivision_EmptyNames(t *testing.T) {
	got := GetAmbassadorDivision("Gold", nil)
	if got != "" {
		t.Errorf("GetAmbassadorDivision with nil names = %q, want empty", got)
	}
}

func TestAmbassadorMuReduction(t *testing.T) {
	tests := []struct {
		name      string
		mu        float64
		reduction float64
		wantMu    float64
	}{
		{
			name:      "normal reduction",
			mu:        35.0,
			reduction: 10.0,
			wantMu:    25.0,
		},
		{
			name:      "reduction would go negative, clamp to zero",
			mu:        5.0,
			reduction: 10.0,
			wantMu:    0.0,
		},
		{
			name:      "zero reduction",
			mu:        35.0,
			reduction: 0.0,
			wantMu:    35.0,
		},
		{
			name:      "exact reduction to zero",
			mu:        10.0,
			reduction: 10.0,
			wantMu:    0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ApplyAmbassadorMuReduction(tt.mu, tt.reduction)
			if got != tt.wantMu {
				t.Errorf("ApplyAmbassadorMuReduction(%f, %f) = %f, want %f",
					tt.mu, tt.reduction, got, tt.wantMu)
			}
		})
	}
}

func TestAmbassadorStateToggle(t *testing.T) {
	state := &AmbassadorState{}

	if state.IsActive {
		t.Error("new AmbassadorState should not be active")
	}

	state.IsActive = true
	if !state.IsActive {
		t.Error("state should be active after setting IsActive to true")
	}

	state.IsActive = false
	if state.IsActive {
		t.Error("state should be inactive after setting IsActive to false")
	}
}

func TestAmbassadorProgramEnabled(t *testing.T) {
	settings := GlobalMatchmakingSettings{}

	if settings.AmbassadorProgramEnabled() {
		t.Error("AmbassadorProgramEnabled should return false by default (nil pointer)")
	}

	enabled := true
	settings.EnableAmbassadorProgram = &enabled
	if !settings.AmbassadorProgramEnabled() {
		t.Error("AmbassadorProgramEnabled should return true when set to true")
	}

	disabled := false
	settings.EnableAmbassadorProgram = &disabled
	if settings.AmbassadorProgramEnabled() {
		t.Error("AmbassadorProgramEnabled should return false when set to false")
	}
}

func TestAmbassadorStateStorageMeta(t *testing.T) {
	state := NewAmbassadorState()
	meta := state.StorageMeta()

	if meta.Collection != StorageCollectionAmbassador {
		t.Errorf("Collection = %q, want %q", meta.Collection, StorageCollectionAmbassador)
	}
	if meta.Key != StorageKeyAmbassador {
		t.Errorf("Key = %q, want %q", meta.Key, StorageKeyAmbassador)
	}
}

func TestRecordAmbassadorMatch(t *testing.T) {
	state := NewAmbassadorState()
	state.IsActive = true

	if state.TotalAmbassadorMatches != 0 {
		t.Errorf("initial TotalAmbassadorMatches = %d, want 0", state.TotalAmbassadorMatches)
	}

	now := time.Now().UTC()
	state.RecordAmbassadorMatch()

	if state.TotalAmbassadorMatches != 1 {
		t.Errorf("after one match TotalAmbassadorMatches = %d, want 1", state.TotalAmbassadorMatches)
	}
	if state.MatchesSinceLastAmbassador != 0 {
		t.Errorf("MatchesSinceLastAmbassador = %d, want 0 (just ambassadored)", state.MatchesSinceLastAmbassador)
	}
	if state.LastAmbassadorMatch.Before(now) {
		t.Error("LastAmbassadorMatch should be at or after the current time")
	}

	// Record a normal match (non-ambassador)
	state.RecordNormalMatch()
	if state.MatchesSinceLastAmbassador != 1 {
		t.Errorf("after normal match MatchesSinceLastAmbassador = %d, want 1", state.MatchesSinceLastAmbassador)
	}
}
