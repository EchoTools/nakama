package server

import (
	"encoding/json"
	"testing"
)

func TestEVRMatchmakerConfig_DefaultValues(t *testing.T) {
	config := NewEVRMatchmakerConfig()
	config.SetDefaults()

	tests := []struct {
		name     string
		got      any
		expected any
	}{
		{"TimeoutConfig.MatchmakingTimeoutSecs", config.Timeout.MatchmakingTimeoutSecs, 360},
		{"TimeoutConfig.FailsafeTimeoutSecs", config.Timeout.FailsafeTimeoutSecs, 300},
		{"TimeoutConfig.FallbackTimeoutSecs", config.Timeout.FallbackTimeoutSecs, 150},
		{"BackfillConfig.ArenaBackfillMaxAgeSecs", config.Backfill.ArenaBackfillMaxAgeSecs, 270},
		{"ReducingPrecisionConfig.IntervalSecs", config.ReducingPrecision.IntervalSecs, 30},
		{"ReducingPrecisionConfig.MaxCycles", config.ReducingPrecision.MaxCycles, 5},
		{"SBMMConfig.MinPlayerCount", config.SBMM.MinPlayerCount, 24},
		{"SBMMConfig.PartySkillBoostPercent", config.SBMM.PartySkillBoostPercent, 0.10},
		{"SBMMConfig.RatingRangeExpansionPerMinute", config.SBMM.RatingRangeExpansionPerMinute, 0.5},
		{"SBMMConfig.MaxRatingRangeExpansion", config.SBMM.MaxRatingRangeExpansion, 5.0},
		{"ServerSelectionConfig.MaxServerRTT", config.ServerSelection.MaxServerRTT, 180},
		{"DebuggingConfig.MatchmakerStateCaptureDir", config.Debugging.MatchmakerStateCaptureDir, "/tmp/matchmaker_replay"},
		{"PriorityConfig.WaitTimePriorityThresholdSecs", config.Priority.WaitTimePriorityThresholdSecs, 120},
		{"PriorityConfig.MaxMatchmakingTickets", config.Priority.MaxMatchmakingTickets, 24},
		{"SkillRatingConfig.Defaults.Z", config.SkillRating.Defaults.Z, 3},
		{"SkillRatingConfig.Defaults.Mu", config.SkillRating.Defaults.Mu, 10.0},
		{"SkillRatingConfig.Defaults.Tau", config.SkillRating.Defaults.Tau, 0.3},
		{"SkillRatingConfig.WinningTeamBonus", config.SkillRating.WinningTeamBonus, 4.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.expected {
				t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.expected)
			}
		})
	}

	if config.SkillRating.Defaults.Sigma == 0 {
		t.Error("SkillRating.Defaults.Sigma should be calculated from Mu/Z")
	}

	expectedSigma := config.SkillRating.Defaults.Mu / float64(config.SkillRating.Defaults.Z)
	if config.SkillRating.Defaults.Sigma != expectedSigma {
		t.Errorf("SkillRating.Defaults.Sigma = %v, want %v", config.SkillRating.Defaults.Sigma, expectedSigma)
	}

	tier1 := int32(-3)
	if config.EarlyQuit.EarlyQuitTier1Threshold == nil || *config.EarlyQuit.EarlyQuitTier1Threshold != tier1 {
		t.Errorf("EarlyQuitTier1Threshold = %v, want %d", config.EarlyQuit.EarlyQuitTier1Threshold, tier1)
	}

	tier2 := int32(-10)
	if config.EarlyQuit.EarlyQuitTier2Threshold == nil || *config.EarlyQuit.EarlyQuitTier2Threshold != tier2 {
		t.Errorf("EarlyQuitTier2Threshold = %v, want %d", config.EarlyQuit.EarlyQuitTier2Threshold, tier2)
	}

	if config.SkillRating.TeamStatMultipliers == nil {
		t.Error("TeamStatMultipliers should be initialized")
	}
	if config.SkillRating.PlayerStatMultipliers == nil {
		t.Error("PlayerStatMultipliers should be initialized")
	}
}

func TestEVRMatchmakerConfig_Validation(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*EVRMatchmakerConfig)
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config",
			setup: func(c *EVRMatchmakerConfig) {
				c.SetDefaults()
			},
			wantErr: false,
		},
		{
			name: "negative matchmaking timeout",
			setup: func(c *EVRMatchmakerConfig) {
				c.SetDefaults()
				c.Timeout.MatchmakingTimeoutSecs = -1
			},
			wantErr: true,
			errMsg:  "matchmaking_timeout_secs must be >= 0",
		},
		{
			name: "negative failsafe timeout",
			setup: func(c *EVRMatchmakerConfig) {
				c.SetDefaults()
				c.Timeout.FailsafeTimeoutSecs = -1
			},
			wantErr: true,
			errMsg:  "failsafe_timeout_secs must be >= 0",
		},
		{
			name: "negative backfill max age",
			setup: func(c *EVRMatchmakerConfig) {
				c.SetDefaults()
				c.Backfill.ArenaBackfillMaxAgeSecs = -1
			},
			wantErr: true,
			errMsg:  "arena_backfill_max_age_secs must be >= 0",
		},
		{
			name: "negative rating range",
			setup: func(c *EVRMatchmakerConfig) {
				c.SetDefaults()
				c.SBMM.RatingRange = -1.0
			},
			wantErr: true,
			errMsg:  "rating_range must be >= 0",
		},
		{
			name: "party skill boost too high",
			setup: func(c *EVRMatchmakerConfig) {
				c.SetDefaults()
				c.SBMM.PartySkillBoostPercent = 1.5
			},
			wantErr: true,
			errMsg:  "party_skill_boost_percent must be between 0.0 and 1.0",
		},
		{
			name: "party skill boost too low",
			setup: func(c *EVRMatchmakerConfig) {
				c.SetDefaults()
				c.SBMM.PartySkillBoostPercent = -0.1
			},
			wantErr: true,
			errMsg:  "party_skill_boost_percent must be between 0.0 and 1.0",
		},
		{
			name: "positive early quit tier1 threshold",
			setup: func(c *EVRMatchmakerConfig) {
				c.SetDefaults()
				tier1 := int32(5)
				c.EarlyQuit.EarlyQuitTier1Threshold = &tier1
			},
			wantErr: true,
			errMsg:  "early_quit_tier1_threshold must be <= 0",
		},
		{
			name: "positive early quit tier2 threshold",
			setup: func(c *EVRMatchmakerConfig) {
				c.SetDefaults()
				tier2 := int32(5)
				c.EarlyQuit.EarlyQuitTier2Threshold = &tier2
			},
			wantErr: true,
			errMsg:  "early_quit_tier2_threshold must be <= 0",
		},
		{
			name: "early quit loss threshold too high",
			setup: func(c *EVRMatchmakerConfig) {
				c.SetDefaults()
				c.EarlyQuit.EarlyQuitLossThreshold = 1.5
			},
			wantErr: true,
			errMsg:  "early_quit_loss_threshold must be between 0.0 and 1.0",
		},
		{
			name: "negative max server rtt",
			setup: func(c *EVRMatchmakerConfig) {
				c.SetDefaults()
				c.ServerSelection.MaxServerRTT = -1
			},
			wantErr: true,
			errMsg:  "max_server_rtt must be >= 0",
		},
		{
			name: "zero skill rating Z",
			setup: func(c *EVRMatchmakerConfig) {
				c.SetDefaults()
				c.SkillRating.Defaults.Z = 0
			},
			wantErr: true,
			errMsg:  "skill_rating.defaults.z must be > 0",
		},
		{
			name: "zero skill rating Mu",
			setup: func(c *EVRMatchmakerConfig) {
				c.SetDefaults()
				c.SkillRating.Defaults.Mu = 0
			},
			wantErr: true,
			errMsg:  "skill_rating.defaults.mu must be > 0",
		},
		{
			name: "negative skill rating Tau",
			setup: func(c *EVRMatchmakerConfig) {
				c.SetDefaults()
				c.SkillRating.Defaults.Tau = -0.1
			},
			wantErr: true,
			errMsg:  "skill_rating.defaults.tau must be >= 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := NewEVRMatchmakerConfig()
			tt.setup(config)

			err := config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && err != nil && tt.errMsg != "" {
				if err.Error()[:len(tt.errMsg)] != tt.errMsg {
					t.Errorf("Validate() error = %v, want error starting with %v", err.Error(), tt.errMsg)
				}
			}
		})
	}
}

func TestEVRMatchmakerConfig_JSONSerialization(t *testing.T) {
	config := NewEVRMatchmakerConfig()
	config.SetDefaults()

	config.Timeout.MatchmakingTimeoutSecs = 500
	config.SBMM.EnableSBMM = true
	config.SBMM.MinPlayerCount = 32

	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var loaded EVRMatchmakerConfig
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	loaded.SetDefaults()

	if loaded.Timeout.MatchmakingTimeoutSecs != config.Timeout.MatchmakingTimeoutSecs {
		t.Errorf("Timeout.MatchmakingTimeoutSecs = %v, want %v", loaded.Timeout.MatchmakingTimeoutSecs, config.Timeout.MatchmakingTimeoutSecs)
	}
	if loaded.SBMM.EnableSBMM != config.SBMM.EnableSBMM {
		t.Errorf("SBMM.EnableSBMM = %v, want %v", loaded.SBMM.EnableSBMM, config.SBMM.EnableSBMM)
	}
	if loaded.SBMM.MinPlayerCount != config.SBMM.MinPlayerCount {
		t.Errorf("SBMM.MinPlayerCount = %v, want %v", loaded.SBMM.MinPlayerCount, config.SBMM.MinPlayerCount)
	}
}

func TestEVRMatchmakerConfig_AtomicAccess(t *testing.T) {
	config := NewEVRMatchmakerConfig()
	config.SetDefaults()

	EVRMatchmakerConfigSet(config)

	retrieved := EVRMatchmakerConfigGet()
	if retrieved == nil {
		t.Fatal("EVRMatchmakerConfigGet() returned nil")
	}

	if retrieved.Timeout.MatchmakingTimeoutSecs != config.Timeout.MatchmakingTimeoutSecs {
		t.Errorf("Retrieved config doesn't match set config")
	}
}

func BenchmarkEVRMatchmakerConfigGet(b *testing.B) {
	config := NewEVRMatchmakerConfig()
	config.SetDefaults()
	EVRMatchmakerConfigSet(config)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = EVRMatchmakerConfigGet()
	}
}

func BenchmarkEVRMatchmakerConfig_Validate(b *testing.B) {
	config := NewEVRMatchmakerConfig()
	config.SetDefaults()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = config.Validate()
	}
}
