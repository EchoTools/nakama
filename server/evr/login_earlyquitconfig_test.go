package evr

import (
	"testing"
)

func TestEarlyQuitServiceConfig_Validate_PenaltyLevels(t *testing.T) {
	tests := []struct {
		name     string
		initial  []EarlyQuitPenaltyLevelConfig
		expected []EarlyQuitPenaltyLevelConfig
	}{
		{
			name: "overlapping ranges get adjusted",
			initial: []EarlyQuitPenaltyLevelConfig{
				{PenaltyLevel: 1, MinEarlyQuits: 0, MaxEarlyQuits: 10},
				{PenaltyLevel: 2, MinEarlyQuits: 5, MaxEarlyQuits: 15}, // Overlaps with level 1
			},
			expected: []EarlyQuitPenaltyLevelConfig{
				{PenaltyLevel: 1, MinEarlyQuits: 0, MaxEarlyQuits: 10},
				{PenaltyLevel: 2, MinEarlyQuits: 11, MaxEarlyQuits: 15}, // Adjusted
			},
		},
		{
			name: "min > max gets swapped",
			initial: []EarlyQuitPenaltyLevelConfig{
				{PenaltyLevel: 1, MinEarlyQuits: 10, MaxEarlyQuits: 5}, // Invalid: min > max
			},
			expected: []EarlyQuitPenaltyLevelConfig{
				{PenaltyLevel: 1, MinEarlyQuits: 5, MaxEarlyQuits: 10}, // Swapped
			},
		},
		{
			name: "negative values get clamped to zero",
			initial: []EarlyQuitPenaltyLevelConfig{
				{PenaltyLevel: 1, MinEarlyQuits: -5, MaxEarlyQuits: 10},
				{PenaltyLevel: 2, MinEarlyQuits: 11, MaxEarlyQuits: -1},
			},
			expected: []EarlyQuitPenaltyLevelConfig{
				{PenaltyLevel: 1, MinEarlyQuits: 0, MaxEarlyQuits: 10},  // Min clamped
				{PenaltyLevel: 2, MinEarlyQuits: 11, MaxEarlyQuits: 11}, // Max clamped and adjusted
			},
		},
		{
			name: "unsorted levels get sorted by penalty level",
			initial: []EarlyQuitPenaltyLevelConfig{
				{PenaltyLevel: 3, MinEarlyQuits: 20, MaxEarlyQuits: 30},
				{PenaltyLevel: 1, MinEarlyQuits: 0, MaxEarlyQuits: 10},
				{PenaltyLevel: 2, MinEarlyQuits: 11, MaxEarlyQuits: 19},
			},
			expected: []EarlyQuitPenaltyLevelConfig{
				{PenaltyLevel: 1, MinEarlyQuits: 0, MaxEarlyQuits: 10},
				{PenaltyLevel: 2, MinEarlyQuits: 11, MaxEarlyQuits: 19},
				{PenaltyLevel: 3, MinEarlyQuits: 20, MaxEarlyQuits: 30},
			},
		},
		{
			name: "overlapping after sort gets fixed",
			initial: []EarlyQuitPenaltyLevelConfig{
				{PenaltyLevel: 2, MinEarlyQuits: 5, MaxEarlyQuits: 15},
				{PenaltyLevel: 1, MinEarlyQuits: 0, MaxEarlyQuits: 10},
			},
			expected: []EarlyQuitPenaltyLevelConfig{
				{PenaltyLevel: 1, MinEarlyQuits: 0, MaxEarlyQuits: 10},
				{PenaltyLevel: 2, MinEarlyQuits: 11, MaxEarlyQuits: 15}, // Adjusted after sort
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &EarlyQuitServiceConfig{
				PenaltyLevels: tt.initial,
			}
			config.Validate()

			if len(config.PenaltyLevels) != len(tt.expected) {
				t.Fatalf("expected %d levels, got %d", len(tt.expected), len(config.PenaltyLevels))
			}

			for i := range tt.expected {
				actual := config.PenaltyLevels[i]
				expected := tt.expected[i]

				if actual.PenaltyLevel != expected.PenaltyLevel {
					t.Errorf("level %d: expected penalty level %d, got %d", i, expected.PenaltyLevel, actual.PenaltyLevel)
				}
				if actual.MinEarlyQuits != expected.MinEarlyQuits {
					t.Errorf("level %d: expected min %d, got %d", i, expected.MinEarlyQuits, actual.MinEarlyQuits)
				}
				if actual.MaxEarlyQuits != expected.MaxEarlyQuits {
					t.Errorf("level %d: expected max %d, got %d", i, expected.MaxEarlyQuits, actual.MaxEarlyQuits)
				}
			}
		})
	}
}

func TestEarlyQuitServiceConfig_Validate_SteadyPlayerLevels(t *testing.T) {
	tests := []struct {
		name     string
		initial  []EarlyQuitSteadyPlayerLevelConfig
		expected []EarlyQuitSteadyPlayerLevelConfig
	}{
		{
			name: "negative min matches get clamped",
			initial: []EarlyQuitSteadyPlayerLevelConfig{
				{SteadyPlayerLevel: 1, MinNumMatches: -5, MinSteadyRatio: 0.5},
			},
			expected: []EarlyQuitSteadyPlayerLevelConfig{
				{SteadyPlayerLevel: 1, MinNumMatches: 0, MinSteadyRatio: 0.5},
			},
		},
		{
			name: "ratios > 1.0 get clamped",
			initial: []EarlyQuitSteadyPlayerLevelConfig{
				{SteadyPlayerLevel: 1, MinNumMatches: 10, MinSteadyRatio: 1.5},
			},
			expected: []EarlyQuitSteadyPlayerLevelConfig{
				{SteadyPlayerLevel: 1, MinNumMatches: 10, MinSteadyRatio: 1.0},
			},
		},
		{
			name: "ratios < 0 get clamped",
			initial: []EarlyQuitSteadyPlayerLevelConfig{
				{SteadyPlayerLevel: 1, MinNumMatches: 10, MinSteadyRatio: -0.5},
			},
			expected: []EarlyQuitSteadyPlayerLevelConfig{
				{SteadyPlayerLevel: 1, MinNumMatches: 10, MinSteadyRatio: 0.0},
			},
		},
		{
			name: "unsorted levels get sorted",
			initial: []EarlyQuitSteadyPlayerLevelConfig{
				{SteadyPlayerLevel: 3, MinNumMatches: 30, MinSteadyRatio: 0.7},
				{SteadyPlayerLevel: 1, MinNumMatches: 10, MinSteadyRatio: 0.9},
				{SteadyPlayerLevel: 2, MinNumMatches: 20, MinSteadyRatio: 0.8},
			},
			expected: []EarlyQuitSteadyPlayerLevelConfig{
				{SteadyPlayerLevel: 1, MinNumMatches: 10, MinSteadyRatio: 0.9},
				{SteadyPlayerLevel: 2, MinNumMatches: 20, MinSteadyRatio: 0.9}, // Adjusted to be >= previous
				{SteadyPlayerLevel: 3, MinNumMatches: 30, MinSteadyRatio: 0.9}, // Adjusted to be >= previous
			},
		},
		{
			name: "decreasing ratios get adjusted to be non-decreasing",
			initial: []EarlyQuitSteadyPlayerLevelConfig{
				{SteadyPlayerLevel: 1, MinNumMatches: 10, MinSteadyRatio: 0.9},
				{SteadyPlayerLevel: 2, MinNumMatches: 20, MinSteadyRatio: 0.5}, // Decreases
			},
			expected: []EarlyQuitSteadyPlayerLevelConfig{
				{SteadyPlayerLevel: 1, MinNumMatches: 10, MinSteadyRatio: 0.9},
				{SteadyPlayerLevel: 2, MinNumMatches: 20, MinSteadyRatio: 0.9}, // Adjusted to be >= previous
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &EarlyQuitServiceConfig{
				SteadyPlayerLevels: tt.initial,
			}
			config.Validate()

			if len(config.SteadyPlayerLevels) != len(tt.expected) {
				t.Fatalf("expected %d levels, got %d", len(tt.expected), len(config.SteadyPlayerLevels))
			}

			for i := range tt.expected {
				actual := config.SteadyPlayerLevels[i]
				expected := tt.expected[i]

				if actual.SteadyPlayerLevel != expected.SteadyPlayerLevel {
					t.Errorf("level %d: expected steady player level %d, got %d", i, expected.SteadyPlayerLevel, actual.SteadyPlayerLevel)
				}
				if actual.MinNumMatches != expected.MinNumMatches {
					t.Errorf("level %d: expected min matches %d, got %d", i, expected.MinNumMatches, actual.MinNumMatches)
				}
				if actual.MinSteadyRatio != expected.MinSteadyRatio {
					t.Errorf("level %d: expected min ratio %.2f, got %.2f", i, expected.MinSteadyRatio, actual.MinSteadyRatio)
				}
			}
		})
	}
}

func TestEarlyQuitServiceConfig_Validate_EmptyConfig(t *testing.T) {
	config := &EarlyQuitServiceConfig{}
	config.Validate()

	// Empty config should be populated with defaults
	if len(config.PenaltyLevels) == 0 {
		t.Error("expected penalty levels to be populated from defaults")
	}
	if len(config.SteadyPlayerLevels) == 0 {
		t.Error("expected steady player levels to be populated from defaults")
	}
}

func TestEarlyQuitServiceConfig_Validate_ComplexScenario(t *testing.T) {
	// Test a complex scenario with multiple issues
	config := &EarlyQuitServiceConfig{
		PenaltyLevels: []EarlyQuitPenaltyLevelConfig{
			{PenaltyLevel: 3, MinEarlyQuits: 25, MaxEarlyQuits: 20}, // Unsorted, min > max
			{PenaltyLevel: 1, MinEarlyQuits: 0, MaxEarlyQuits: 10},
			{PenaltyLevel: 2, MinEarlyQuits: 8, MaxEarlyQuits: 15}, // Overlaps with level 1
		},
		SteadyPlayerLevels: []EarlyQuitSteadyPlayerLevelConfig{
			{SteadyPlayerLevel: 2, MinNumMatches: 20, MinSteadyRatio: 0.5},
			{SteadyPlayerLevel: 1, MinNumMatches: -10, MinSteadyRatio: 1.5}, // Negative, ratio > 1
		},
	}

	config.Validate()

	// Check penalty levels are sorted and fixed
	if config.PenaltyLevels[0].PenaltyLevel != 1 {
		t.Error("expected penalty levels to be sorted")
	}
	if config.PenaltyLevels[1].MinEarlyQuits <= config.PenaltyLevels[0].MaxEarlyQuits {
		t.Error("expected no overlapping ranges")
	}
	if config.PenaltyLevels[2].MinEarlyQuits != 20 {
		t.Errorf("expected level 3 min/max to be swapped, got min=%d", config.PenaltyLevels[2].MinEarlyQuits)
	}

	// Check steady player levels are sorted and fixed
	if config.SteadyPlayerLevels[0].SteadyPlayerLevel != 1 {
		t.Error("expected steady player levels to be sorted")
	}
	if config.SteadyPlayerLevels[0].MinNumMatches != 0 {
		t.Error("expected negative min matches to be clamped to 0")
	}
	if config.SteadyPlayerLevels[0].MinSteadyRatio != 1.0 {
		t.Error("expected ratio > 1 to be clamped to 1.0")
	}
}
