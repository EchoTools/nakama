package evr

import (
	"fmt"
)

// SNSEarlyQuitFeatureFlags represents the feature flags message for early quit system
// Server → Client: Controls which early quit features are enabled/disabled
type SNSEarlyQuitFeatureFlags struct {
	Enabled             bool     `json:"enabled"`                        // Master enable/disable
	EnableMMLockout     bool     `json:"enable_mm_lockout"`              // Matchmaking lockout active
	EnableSpawnLock     bool     `json:"enable_spawn_lock"`              // Spawn lock penalties active
	EnableAutoReport    bool     `json:"enable_auto_report"`             // Automatic reporting active
	EnableUICountdown   bool     `json:"enable_ui_countdown"`            // Show countdown timer in UI
	EnableQueueBlocking bool     `json:"enable_queue_blocking"`          // Block queue during penalty
	SupportedRegions    []string `json:"supported_regions"`              // Regions where system applies
	SupportedGameModes  []string `json:"supported_game_modes"`           // Game modes where system applies
	MaxPenaltyLevel     int32    `json:"max_penalty_level"`              // Max penalty tier
	PenaltyDecayDays    int32    `json:"penalty_decay_days"`             // Days before penalties reset
	TierDownGracePeriod int32    `json:"tier_down_grace_period_seconds"` // Grace period for tier restoration
}

func (m SNSEarlyQuitFeatureFlags) Token() string {
	return "SNSEarlyQuitFeatureFlags"
}

func (m *SNSEarlyQuitFeatureFlags) Symbol() Symbol {
	return ToSymbol(m.Token())
}

// DefaultEarlyQuitFeatureFlags returns the default enabled feature flags
func DefaultEarlyQuitFeatureFlags() *SNSEarlyQuitFeatureFlags {
	return &SNSEarlyQuitFeatureFlags{
		Enabled:             true,
		EnableMMLockout:     true,
		EnableSpawnLock:     true,
		EnableAutoReport:    true,
		EnableUICountdown:   true,
		EnableQueueBlocking: true,
		SupportedRegions: []string{
			"us-east",
			"us-west",
			"eu-west",
			"ap-southeast",
		},
		SupportedGameModes: []string{
			"echo_arena",
		},
		MaxPenaltyLevel:     3,
		PenaltyDecayDays:    30,
		TierDownGracePeriod: 86400, // 1 day in seconds
	}
}

func (m *SNSEarlyQuitFeatureFlags) String() string {
	return fmt.Sprintf("%s(enabled=%v, regions=%v, modes=%v)",
		m.Token(), m.Enabled, m.SupportedRegions, m.SupportedGameModes)
}

func (m *SNSEarlyQuitFeatureFlags) Stream(s *EasyStream) error {
	return s.StreamJson(m, false, ZlibCompression)
}
