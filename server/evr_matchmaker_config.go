package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/atomic"
)

const (
	// EVRMatchmakerConfigCollection is the storage collection for matchmaker configuration
	EVRMatchmakerConfigCollection = "SystemConfig"
	// EVRMatchmakerConfigKey is the storage key for matchmaker configuration
	EVRMatchmakerConfigKey = "matchmaker"
)

var evrMatchmakerConfig = atomic.NewPointer((*EVRMatchmakerConfig)(nil))

func EVRMatchmakerConfigGet() *EVRMatchmakerConfig {
	return evrMatchmakerConfig.Load()
}

func EVRMatchmakerConfigSet(config *EVRMatchmakerConfig) {
	evrMatchmakerConfig.Store(config)
}

// EVRMatchmakerConfig is the system-owned configuration for matchmaking behavior.
// It replaces GlobalMatchmakingSettings and SkillRatingSettings from ServiceSettingsData.
// Storage: Collection="SystemConfig", Key="matchmaker", UserID=SystemUserID
// Permissions: Read=2 (public), Write=0 (system only)
type EVRMatchmakerConfig struct {
	meta StorableMetadata `json:"-"` // Storage metadata (not serialized)

	Timeout           MatchmakingTimeoutConfig    `json:"timeout"`            // Timeout configuration
	Backfill          MatchmakingBackfillConfig   `json:"backfill"`           // Backfill configuration
	ReducingPrecision ReducingPrecisionConfig     `json:"reducing_precision"` // Reducing precision configuration
	SBMM              SkillBasedMatchmakingConfig `json:"sbmm"`               // Skill-based matchmaking configuration
	Division          MatchmakingDivisionConfig   `json:"division"`           // Division configuration
	EarlyQuit         MatchmakingEarlyQuitConfig  `json:"early_quit"`         // Early quit penalty configuration
	ServerSelection   MatchmakingServerConfig     `json:"server_selection"`   // Server selection configuration
	QueryAddons       MatchmakingQueryAddons      `json:"query_addons"`       // Query addons configuration
	Debugging         MatchmakingDebugConfig      `json:"debugging"`          // Debugging configuration
	Priority          MatchmakingPriorityConfig   `json:"priority"`           // Priority configuration
	SkillRating       EVRSkillRatingConfig        `json:"skill_rating"`       // Skill rating configuration
}

// MatchmakingTimeoutConfig contains timeout-related matchmaking settings
type MatchmakingTimeoutConfig struct {
	MatchmakingTimeoutSecs int `json:"matchmaking_timeout_secs"` // The matchmaking timeout (default 360)
	FailsafeTimeoutSecs    int `json:"failsafe_timeout_secs"`    // The failsafe timeout (default MatchmakingTimeoutSecs - 60)
	FallbackTimeoutSecs    int `json:"fallback_timeout_secs"`    // The fallback timeout (default FailsafeTimeoutSecs / 2)
}

// MatchmakingBackfillConfig contains backfill-related matchmaking settings
type MatchmakingBackfillConfig struct {
	DisableArenaBackfill         bool `json:"disable_arena_backfill"`          // Disable backfilling for arena matches
	ArenaBackfillMaxAgeSecs      int  `json:"arena_backfill_max_age_secs"`     // Maximum age of arena matches to backfill (default 270s)
	BackfillMinTimeSecs          int  `json:"backfill_min_time_secs"`          // Minimum time in seconds before backfilling a player to a match
	EnablePostMatchmakerBackfill bool `json:"enable_post_matchmaker_backfill"` // Enable post-matchmaker backfill using matchmaker exports and match list
}

// ReducingPrecisionConfig contains configuration for relaxing matchmaking constraints over time
type ReducingPrecisionConfig struct {
	IntervalSecs int `json:"interval_secs"` // Interval in seconds after which constraints are relaxed for backfill (0 = disabled, default 30)
	MaxCycles    int `json:"max_cycles"`    // Maximum number of precision reduction cycles before fully relaxing constraints (default 5)
}

// SkillBasedMatchmakingConfig contains skill-based matchmaking settings
type SkillBasedMatchmakingConfig struct {
	EnableSBMM                    bool    `json:"enable_skill_based_mm"`             // Enable skill-based matchmaking
	MinPlayerCount                int     `json:"sbmm_min_player_count"`             // Minimum player count to enable skill-based matchmaking (default 24)
	EnableOrdinalRange            bool    `json:"enable_ordinal_range"`              // Enable ordinal range
	RatingRange                   float64 `json:"rating_range"`                      // The rating range
	RatingRangeExpansionPerMinute float64 `json:"rating_range_expansion_per_minute"` // How much to expand rating range per minute of wait time (default 0.5)
	MaxRatingRangeExpansion       float64 `json:"max_rating_range_expansion"`        // Maximum total rating range expansion allowed (default 5.0)
	MatchmakingTicketsUseMu       bool    `json:"sbmm_matchmaking_tickets_use_mu"`   // Use Mu instead of Ordinal for matchmaking tickets
	BackfillQueriesUseMu          bool    `json:"sbmm_backfill_queries_use_mu"`      // Use Mu instead of Ordinal for backfill queries
	MatchmakerUseMu               bool    `json:"sbmm_matchmaker_use_mu"`            // Use Mu instead of Ordinal for matchmaker player MMR values
	PartySkillBoostPercent        float64 `json:"party_skill_boost_percent"`         // Boost party effective skill by this percentage (e.g., 0.10 = 10%) to account for coordination advantage (default 0.10)
	EnableRosterVariants          bool    `json:"enable_roster_variants"`            // Generate multiple roster variants (balanced/stacked) for better match selection
	UseSnakeDraftTeamFormation    bool    `json:"use_snake_draft_team_formation"`    // Use snake draft instead of sequential filling for team formation
}

// MatchmakingDivisionConfig contains division-related matchmaking settings
type MatchmakingDivisionConfig struct {
	EnableDivisions                bool `json:"enable_divisions"`                    // Enable divisions
	GreenDivisionMaxAccountAgeDays int  `json:"green_division_max_account_age_days"` // The maximum account age to be in the green division
}

// MatchmakingEarlyQuitConfig contains early quit penalty settings
type MatchmakingEarlyQuitConfig struct {
	EnableEarlyQuitPenalty  bool    `json:"enable_early_quit_penalty"`  // Enable early quit penalty
	SilentEarlyQuitSystem   bool    `json:"silent_early_quit_system"`   // Disable Discord DM notifications for tier changes
	EarlyQuitTier1Threshold *int32  `json:"early_quit_tier1_threshold"` // Penalty level threshold for Tier 1 (good standing). Players with penalty <= threshold stay in Tier 1. Nil means not configured. (default -3)
	EarlyQuitTier2Threshold *int32  `json:"early_quit_tier2_threshold"` // Penalty level threshold for Tier 2 (reserved for future Tier 3+ implementation). Nil means not configured. (default -10)
	EarlyQuitLossThreshold  float64 `json:"early_quit_loss_threshold"`  // Threshold (0.0-1.0) for EarlyQuits/(Wins+Losses) ratio above which early quits count as losses in profile stats. 0.0 = disabled.
}

// MatchmakingServerConfig contains server selection settings
type MatchmakingServerConfig struct {
	Ratings      map[string]float64 `json:"ratings"`        // Server ratings by username or IP
	ExcludeList  []string           `json:"exclude_list"`   // Servers to exclude from selection
	RTTDelta     map[string]int     `json:"rtt_delta"`      // RTT adjustments by username or IP
	MaxServerRTT int                `json:"max_server_rtt"` // The maximum RTT to allow (default 180)
}

// MatchmakingQueryAddons contains additional query strings to add to matchmaking queries
type MatchmakingQueryAddons struct {
	Backfill     string `json:"lobby_backfill"`               // Additional query for backfill
	LobbyBuilder string `json:"matchmaker_server_allocation"` // Additional query for lobby builder
	Create       string `json:"lobby_create"`                 // Additional query for lobby creation
	Allocate     string `json:"allocate"`                     // Additional query for allocation
	Matchmaking  string `json:"matchmaking_ticket"`           // Additional query for matchmaking tickets
	RPCAllocate  string `json:"rpc_allocate"`                 // Additional query for RPC allocation
}

// MatchmakingDebugConfig contains debugging and development settings
type MatchmakingDebugConfig struct {
	EnableMatchmakerStateCapture bool   `json:"enable_matchmaker_state_capture"` // Enable capturing matchmaker state to files for debugging and replay (default false)
	MatchmakerStateCaptureDir    string `json:"matchmaker_state_capture_dir"`    // Directory to save matchmaker state files (default "/tmp/matchmaker_replay")
}

// MatchmakingPriorityConfig contains matchmaking priority settings
type MatchmakingPriorityConfig struct {
	WaitTimePriorityThresholdSecs int `json:"wait_time_priority_threshold_secs"` // Wait time threshold in seconds when wait time priority overrides match size priority (default 120)
	MaxMatchmakingTickets         int `json:"max_matchmaking_tickets"`           // Maximum number of matchmaking tickets allowed per interval (default 24)
}

// EVRSkillRatingConfig contains skill rating calculation settings
type EVRSkillRatingConfig struct {
	Defaults RatingDefaults `json:"defaults"` // Default rating values

	// TeamStatMultipliers are used for team-based rating calculations
	// Keys should match the JSON field names from evr.MatchTypeStats
	// Example: {"Points": 1.0, "Assists": 2.0, "Saves": 3.0, "Passes": 1.0, "ShotsOnGoal": -1.0}
	TeamStatMultipliers map[string]float64 `json:"team_stat_multipliers"`

	// PlayerStatMultipliers are used for individual player rating calculations
	// Uses a simpler formula focused on personal contribution
	// Example: {"Points": 1.0, "Assists": 1.0, "Saves": 2.0}
	PlayerStatMultipliers map[string]float64 `json:"player_stat_multipliers"`

	// WinningTeamBonus is added to winning team players' scores before rating calculation (default 4.0)
	WinningTeamBonus float64 `json:"winning_team_bonus"`
}

// StorageMeta returns the storage metadata for the matchmaker configuration
func (c *EVRMatchmakerConfig) StorageMeta() StorableMetadata {
	return c.meta
}

// SetStorageMeta sets the storage metadata for the matchmaker configuration
func (c *EVRMatchmakerConfig) SetStorageMeta(meta StorableMetadata) {
	c.meta = meta
}

// NewEVRMatchmakerConfig creates a new EVRMatchmakerConfig with default values and storage metadata
func NewEVRMatchmakerConfig() *EVRMatchmakerConfig {
	config := &EVRMatchmakerConfig{
		meta: StorableMetadata{
			UserID:          SystemUserID,
			Collection:      EVRMatchmakerConfigCollection,
			Key:             EVRMatchmakerConfigKey,
			PermissionRead:  2, // Public read
			PermissionWrite: 0, // System write only
			Version:         "*",
		},
	}
	config.SetDefaults()
	return config
}

// SetDefaults initializes all configuration values with their defaults
func (c *EVRMatchmakerConfig) SetDefaults() {
	// TimeoutConfig defaults
	if c.Timeout.MatchmakingTimeoutSecs == 0 {
		c.Timeout.MatchmakingTimeoutSecs = 360
	}
	if c.Timeout.FailsafeTimeoutSecs == 0 {
		c.Timeout.FailsafeTimeoutSecs = c.Timeout.MatchmakingTimeoutSecs - 60
	}
	if c.Timeout.FallbackTimeoutSecs == 0 {
		c.Timeout.FallbackTimeoutSecs = c.Timeout.FailsafeTimeoutSecs / 2
	}

	// BackfillConfig defaults
	if c.Backfill.ArenaBackfillMaxAgeSecs == 0 {
		c.Backfill.ArenaBackfillMaxAgeSecs = 270
	}

	// ReducingPrecisionConfig defaults
	if c.ReducingPrecision.IntervalSecs == 0 {
		c.ReducingPrecision.IntervalSecs = 30
	}
	if c.ReducingPrecision.MaxCycles == 0 {
		c.ReducingPrecision.MaxCycles = 5
	}

	// SBMMConfig defaults
	if c.SBMM.MinPlayerCount == 0 {
		c.SBMM.MinPlayerCount = 24
	}
	if c.SBMM.PartySkillBoostPercent == 0 {
		c.SBMM.PartySkillBoostPercent = 0.10
	}
	if c.SBMM.RatingRangeExpansionPerMinute == 0 {
		c.SBMM.RatingRangeExpansionPerMinute = 0.5
	}
	if c.SBMM.MaxRatingRangeExpansion == 0 {
		c.SBMM.MaxRatingRangeExpansion = 5.0
	}

	// EarlyQuitConfig defaults
	if c.EarlyQuit.EarlyQuitTier1Threshold == nil {
		tier1Threshold := int32(-3)
		c.EarlyQuit.EarlyQuitTier1Threshold = &tier1Threshold
	}
	if c.EarlyQuit.EarlyQuitTier2Threshold == nil {
		tier2Threshold := int32(-10)
		c.EarlyQuit.EarlyQuitTier2Threshold = &tier2Threshold
	}

	// ServerSelectionConfig defaults
	if c.ServerSelection.Ratings == nil {
		c.ServerSelection.Ratings = make(map[string]float64)
	}
	if c.ServerSelection.RTTDelta == nil {
		c.ServerSelection.RTTDelta = make(map[string]int)
	}
	if c.ServerSelection.MaxServerRTT == 0 {
		c.ServerSelection.MaxServerRTT = 180
	}

	// DebuggingConfig defaults
	if c.Debugging.MatchmakerStateCaptureDir == "" {
		c.Debugging.MatchmakerStateCaptureDir = "/tmp/matchmaker_replay"
	}

	// PriorityConfig defaults
	if c.Priority.WaitTimePriorityThresholdSecs == 0 {
		c.Priority.WaitTimePriorityThresholdSecs = 120
	}
	if c.Priority.MaxMatchmakingTickets == 0 {
		c.Priority.MaxMatchmakingTickets = 24
	}

	// SkillRatingConfig defaults
	if c.SkillRating.Defaults.Z == 0 {
		c.SkillRating.Defaults.Z = 3
	}
	if c.SkillRating.Defaults.Mu == 0 {
		c.SkillRating.Defaults.Mu = 10.0
	}
	if c.SkillRating.Defaults.Sigma == 0 {
		c.SkillRating.Defaults.Sigma = c.SkillRating.Defaults.Mu / float64(c.SkillRating.Defaults.Z)
	}
	if c.SkillRating.Defaults.Tau == 0 {
		c.SkillRating.Defaults.Tau = 0.3
	}

	// TeamStatMultipliers defaults
	if c.SkillRating.TeamStatMultipliers == nil {
		c.SkillRating.TeamStatMultipliers = map[string]float64{
			"Points":      1.0,
			"Assists":     2.0,
			"Saves":       3.0,
			"Passes":      1.0,
			"ShotsOnGoal": -1.0,
		}
	}

	// PlayerStatMultipliers defaults
	if c.SkillRating.PlayerStatMultipliers == nil {
		c.SkillRating.PlayerStatMultipliers = map[string]float64{
			"Points":  1.0,
			"Assists": 1.0,
			"Saves":   2.0,
		}
	}

	// WinningTeamBonus default
	if c.SkillRating.WinningTeamBonus == 0 {
		c.SkillRating.WinningTeamBonus = 4.0
	}
}

// Validate performs validation on the matchmaker configuration
// Returns an error if validation fails
func (c *EVRMatchmakerConfig) Validate() error {
	// Timeout validation
	if c.Timeout.MatchmakingTimeoutSecs < 0 {
		return fmt.Errorf("matchmaking_timeout_secs must be >= 0, got %d", c.Timeout.MatchmakingTimeoutSecs)
	}
	if c.Timeout.FailsafeTimeoutSecs < 0 {
		return fmt.Errorf("failsafe_timeout_secs must be >= 0, got %d", c.Timeout.FailsafeTimeoutSecs)
	}
	if c.Timeout.FallbackTimeoutSecs < 0 {
		return fmt.Errorf("fallback_timeout_secs must be >= 0, got %d", c.Timeout.FallbackTimeoutSecs)
	}

	// Backfill validation
	if c.Backfill.ArenaBackfillMaxAgeSecs < 0 {
		return fmt.Errorf("arena_backfill_max_age_secs must be >= 0, got %d", c.Backfill.ArenaBackfillMaxAgeSecs)
	}
	if c.Backfill.BackfillMinTimeSecs < 0 {
		return fmt.Errorf("backfill_min_time_secs must be >= 0, got %d", c.Backfill.BackfillMinTimeSecs)
	}

	// Reducing precision validation
	if c.ReducingPrecision.IntervalSecs < 0 {
		return fmt.Errorf("reducing_precision_interval_secs must be >= 0, got %d", c.ReducingPrecision.IntervalSecs)
	}
	if c.ReducingPrecision.MaxCycles < 0 {
		return fmt.Errorf("reducing_precision_max_cycles must be >= 0, got %d", c.ReducingPrecision.MaxCycles)
	}

	// SBMM validation
	if c.SBMM.MinPlayerCount < 0 {
		return fmt.Errorf("sbmm_min_player_count must be >= 0, got %d", c.SBMM.MinPlayerCount)
	}
	if c.SBMM.RatingRange < 0 {
		return fmt.Errorf("rating_range must be >= 0, got %f", c.SBMM.RatingRange)
	}
	if c.SBMM.RatingRangeExpansionPerMinute < 0 {
		return fmt.Errorf("rating_range_expansion_per_minute must be >= 0, got %f", c.SBMM.RatingRangeExpansionPerMinute)
	}
	if c.SBMM.MaxRatingRangeExpansion < 0 {
		return fmt.Errorf("max_rating_range_expansion must be >= 0, got %f", c.SBMM.MaxRatingRangeExpansion)
	}
	if c.SBMM.PartySkillBoostPercent < 0.0 || c.SBMM.PartySkillBoostPercent > 1.0 {
		return fmt.Errorf("party_skill_boost_percent must be between 0.0 and 1.0, got %f", c.SBMM.PartySkillBoostPercent)
	}

	// Division validation
	if c.Division.GreenDivisionMaxAccountAgeDays < 0 {
		return fmt.Errorf("green_division_max_account_age_days must be >= 0, got %d", c.Division.GreenDivisionMaxAccountAgeDays)
	}

	// Early quit validation
	if c.EarlyQuit.EarlyQuitTier1Threshold != nil && *c.EarlyQuit.EarlyQuitTier1Threshold > 0 {
		return fmt.Errorf("early_quit_tier1_threshold must be <= 0, got %d", *c.EarlyQuit.EarlyQuitTier1Threshold)
	}
	if c.EarlyQuit.EarlyQuitTier2Threshold != nil && *c.EarlyQuit.EarlyQuitTier2Threshold > 0 {
		return fmt.Errorf("early_quit_tier2_threshold must be <= 0, got %d", *c.EarlyQuit.EarlyQuitTier2Threshold)
	}
	if c.EarlyQuit.EarlyQuitLossThreshold < 0.0 || c.EarlyQuit.EarlyQuitLossThreshold > 1.0 {
		return fmt.Errorf("early_quit_loss_threshold must be between 0.0 and 1.0, got %f", c.EarlyQuit.EarlyQuitLossThreshold)
	}

	// Server selection validation
	if c.ServerSelection.MaxServerRTT < 0 {
		return fmt.Errorf("max_server_rtt must be >= 0, got %d", c.ServerSelection.MaxServerRTT)
	}

	// Priority validation
	if c.Priority.WaitTimePriorityThresholdSecs < 0 {
		return fmt.Errorf("wait_time_priority_threshold_secs must be >= 0, got %d", c.Priority.WaitTimePriorityThresholdSecs)
	}
	if c.Priority.MaxMatchmakingTickets < 0 {
		return fmt.Errorf("max_matchmaking_tickets must be >= 0, got %d", c.Priority.MaxMatchmakingTickets)
	}

	// Skill rating validation
	if c.SkillRating.Defaults.Z <= 0 {
		return fmt.Errorf("skill_rating.defaults.z must be > 0, got %d", c.SkillRating.Defaults.Z)
	}
	if c.SkillRating.Defaults.Mu <= 0 {
		return fmt.Errorf("skill_rating.defaults.mu must be > 0, got %f", c.SkillRating.Defaults.Mu)
	}
	if c.SkillRating.Defaults.Sigma <= 0 {
		return fmt.Errorf("skill_rating.defaults.sigma must be > 0, got %f", c.SkillRating.Defaults.Sigma)
	}
	if c.SkillRating.Defaults.Tau < 0 {
		return fmt.Errorf("skill_rating.defaults.tau must be >= 0, got %f", c.SkillRating.Defaults.Tau)
	}
	if c.SkillRating.WinningTeamBonus < 0 {
		return fmt.Errorf("skill_rating.winning_team_bonus must be >= 0, got %f", c.SkillRating.WinningTeamBonus)
	}

	return nil
}

// LoadEVRMatchmakerConfig loads the matchmaker configuration from storage
// If create is true, it will create a new configuration with defaults if it doesn't exist
func LoadEVRMatchmakerConfig(ctx context.Context, nk runtime.NakamaModule, create bool) (*EVRMatchmakerConfig, error) {
	config := NewEVRMatchmakerConfig()
	if err := StorableRead(ctx, nk, SystemUserID, config, create); err != nil {
		return nil, fmt.Errorf("failed to load matchmaker config: %w", err)
	}

	// Ensure defaults are set (in case config was loaded from storage)
	config.SetDefaults()

	// Validate the loaded configuration
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("matchmaker config validation failed: %w", err)
	}

	return config, nil
}

// SaveEVRMatchmakerConfig saves the matchmaker configuration to storage
func SaveEVRMatchmakerConfig(ctx context.Context, nk runtime.NakamaModule, config *EVRMatchmakerConfig) error {
	// Validate before saving
	if err := config.Validate(); err != nil {
		return fmt.Errorf("matchmaker config validation failed: %w", err)
	}

	if err := StorableWrite(ctx, nk, SystemUserID, config); err != nil {
		return fmt.Errorf("failed to save matchmaker config: %w", err)
	}

	return nil
}

// String returns a JSON string representation of the matchmaker configuration
func (c *EVRMatchmakerConfig) String() string {
	data, _ := json.Marshal(c)
	return string(data)
}
