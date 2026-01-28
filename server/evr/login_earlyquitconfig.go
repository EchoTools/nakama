package evr

import (
	"fmt"
	"time"
)

// EarlyQuitConfig is the player's current early quit penalty state (sent in profile)
type EarlyQuitConfig struct {
	SteadyPlayerLevel int32 `json:"steady_player_level,omitempty"`
	NumSteadyMatches  int32 `json:"num_steady_matches,omitempty"`
	PenaltyLevel      int32 `json:"penalty_level,omitempty"`
	PenaltyTimestamp  int64 `json:"penalty_ts,omitempty"`
}

func (c EarlyQuitConfig) PenaltyTimestampAsTime() time.Time {
	return time.Unix(c.PenaltyTimestamp, 0)
}

func (m EarlyQuitConfig) Token() string {
	return "SNSEarlyQuitConfig"
}

func (m *EarlyQuitConfig) Symbol() Symbol {
	return ToSymbol(m.Token())
}

// EarlyQuitPenaltyLevelConfig defines a single penalty tier configuration
type EarlyQuitPenaltyLevelConfig struct {
	PenaltyLevel     int `json:"penalty_level"`
	MinEarlyQuits    int `json:"min_earlyquits"`
	MaxEarlyQuits    int `json:"max_earlyquits"`
	MMLockoutSec     int `json:"mm_lockout_sec"`
	SpawnLock        int `json:"spawnlock"`
	AutoReport       int `json:"autoreport"`
	CNVPEReactivated int `json:"cnvpe_reactivated"`
}

// EarlyQuitSteadyPlayerLevelConfig defines a steady player tier configuration
type EarlyQuitSteadyPlayerLevelConfig struct {
	SteadyPlayerLevel int     `json:"steady_player_level"`
	MinNumMatches     int     `json:"min_num_matches"`
	MinSteadyRatio    float64 `json:"min_steady_ratio"`
}

// EarlyQuitServiceConfig is the config service response structure
type EarlyQuitServiceConfig struct {
	PenaltyLevels      []EarlyQuitPenaltyLevelConfig      `json:"penalty_levels"`
	SteadyPlayerLevels []EarlyQuitSteadyPlayerLevelConfig `json:"steady_player_levels"`
}

func (m EarlyQuitServiceConfig) Token() string {
	return "SNSEarlyQuitConfig"
}

func (m *EarlyQuitServiceConfig) Symbol() Symbol {
	return ToSymbol(m.Token())
}

func (m *EarlyQuitServiceConfig) String() string {
	return fmt.Sprintf("%s(penalty_levels=%d, steady_levels=%d)",
		m.Token(), len(m.PenaltyLevels), len(m.SteadyPlayerLevels))
}

func (m *EarlyQuitServiceConfig) Stream(s *EasyStream) error {
	return s.StreamJson(m, false, ZlibCompression)
}

// NewSNSEarlyQuitConfig creates a new SNS message for early quit config
func NewSNSEarlyQuitConfig(config *EarlyQuitServiceConfig) *EarlyQuitServiceConfig {
	if config == nil {
		config = DefaultEarlyQuitServiceConfig()
	}
	return config
}

// DefaultEarlyQuitServiceConfig returns the default early quit config with max penalty active
func DefaultEarlyQuitServiceConfig() *EarlyQuitServiceConfig {
	return &EarlyQuitServiceConfig{
		PenaltyLevels: []EarlyQuitPenaltyLevelConfig{
			{
				PenaltyLevel:     0,
				MinEarlyQuits:    0,
				MaxEarlyQuits:    2,
				MMLockoutSec:     0,
				SpawnLock:        0,
				AutoReport:       0,
				CNVPEReactivated: 0,
			},
			{
				PenaltyLevel:     1,
				MinEarlyQuits:    3,
				MaxEarlyQuits:    5,
				MMLockoutSec:     120,
				SpawnLock:        0,
				AutoReport:       0,
				CNVPEReactivated: 0,
			},
			{
				PenaltyLevel:     2,
				MinEarlyQuits:    6,
				MaxEarlyQuits:    10,
				MMLockoutSec:     300,
				SpawnLock:        1,
				AutoReport:       0,
				CNVPEReactivated: 0,
			},
			{
				PenaltyLevel:     3,
				MinEarlyQuits:    11,
				MaxEarlyQuits:    999,
				MMLockoutSec:     900,
				SpawnLock:        1,
				AutoReport:       1,
				CNVPEReactivated: 1,
			},
		},
		SteadyPlayerLevels: []EarlyQuitSteadyPlayerLevelConfig{
			{
				SteadyPlayerLevel: 0,
				MinNumMatches:     0,
				MinSteadyRatio:    0.0,
			},
			{
				SteadyPlayerLevel: 1,
				MinNumMatches:     10,
				MinSteadyRatio:    0.9,
			},
			{
				SteadyPlayerLevel: 2,
				MinNumMatches:     25,
				MinSteadyRatio:    0.95,
			},
		},
	}
}
