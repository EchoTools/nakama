package evr

import (
	"fmt"
	"sort"
)

const (
	StorageCollectionEarlyQuitConfig = "EarlyQuit"
	StorageKeyEarlyQuitConfig        = "config"
)

// SNSEarlyQuitConfig is the on-wire message for early quit penalty configuration.
//
// Wire layout (variable zstd payload):
//
//	+0x00  uint32  size (decompressed JSON size, LE)
//	+0x04  []byte  zstd-compressed JSON (penalty_levels + steady_player_levels)
type SNSEarlyQuitConfig struct {
	PenaltyLevels      []EarlyQuitPenaltyLevelConfig      `json:"penalty_levels"`
	SteadyPlayerLevels []EarlyQuitSteadyPlayerLevelConfig `json:"steady_player_levels"`

	version string
}

func (m SNSEarlyQuitConfig) Token() string {
	return "SNSEarlyQuitConfig"
}

func (m *SNSEarlyQuitConfig) Symbol() Symbol {
	return ToSymbol(m.Token())
}

func (m *SNSEarlyQuitConfig) String() string {
	return fmt.Sprintf("%s(penalty_levels=%d, steady_levels=%d)",
		m.Token(), len(m.PenaltyLevels), len(m.SteadyPlayerLevels))
}

func (m *SNSEarlyQuitConfig) Stream(s *EasyStream) error {
	// Wire format: u32 decompressed JSON size (LE) + zstd-compressed JSON body.
	return s.StreamJson(m, false, ZstdCompression)
}

// StorageVersion returns the storage version.
func (m SNSEarlyQuitConfig) StorageVersion() string {
	return m.version
}

// SetStorageVersion updates the storage version.
func (m *SNSEarlyQuitConfig) SetStorageVersion(version string) {
	m.version = version
}

// EarlyQuitPenaltyLevelConfig defines a single penalty tier configuration.
type EarlyQuitPenaltyLevelConfig struct {
	PenaltyLevel     int `json:"penalty_level"`
	MinEarlyQuits    int `json:"min_earlyquits"`
	MaxEarlyQuits    int `json:"max_earlyquits"`
	MMLockoutSec     int `json:"mm_lockout_sec"`
	SpawnLock        int `json:"spawnlock"`
	AutoReport       int `json:"autoreport"`
	CNVPEReactivated int `json:"cnvpe_reactivated"`
}

// EarlyQuitSteadyPlayerLevelConfig defines a steady player tier configuration.
type EarlyQuitSteadyPlayerLevelConfig struct {
	SteadyPlayerLevel int     `json:"steady_player_level"`
	MinNumMatches     int     `json:"min_num_matches"`
	MinSteadyRatio    float64 `json:"min_steady_ratio"`
}

// EarlyQuitConfig is the player's current early quit penalty state (sent in profile).
type EarlyQuitConfig struct {
	SteadyPlayerLevel int32 `json:"steady_player_level,omitempty"`
	NumSteadyMatches  int32 `json:"num_steady_matches,omitempty"`
	PenaltyLevel      int32 `json:"penalty_level,omitempty"`
	PenaltyTimestamp  int64 `json:"penalty_ts,omitempty"`
}

// NewSNSEarlyQuitConfig creates a new SNS message for early quit config.
func NewSNSEarlyQuitConfig(penaltyLevels []EarlyQuitPenaltyLevelConfig, steadyLevels []EarlyQuitSteadyPlayerLevelConfig) *SNSEarlyQuitConfig {
	msg := &SNSEarlyQuitConfig{
		PenaltyLevels:      penaltyLevels,
		SteadyPlayerLevels: steadyLevels,
		version:            "*",
	}
	msg.Validate()
	return msg
}

// NewDefaultSNSEarlyQuitConfig creates a config with default penalty/steady levels.
func NewDefaultSNSEarlyQuitConfig() *SNSEarlyQuitConfig {
	d := DefaultEarlyQuitLevels()
	return NewSNSEarlyQuitConfig(d.PenaltyLevels, d.SteadyPlayerLevels)
}

// defaultLevels holds the default penalty and steady player level configs.
type defaultLevels struct {
	PenaltyLevels      []EarlyQuitPenaltyLevelConfig
	SteadyPlayerLevels []EarlyQuitSteadyPlayerLevelConfig
}

// DefaultEarlyQuitLevels returns the default penalty and steady player levels.
func DefaultEarlyQuitLevels() defaultLevels {
	return defaultLevels{
		PenaltyLevels: []EarlyQuitPenaltyLevelConfig{
			{PenaltyLevel: 0, MinEarlyQuits: 0, MaxEarlyQuits: 2, MMLockoutSec: 0, SpawnLock: 0, AutoReport: 0, CNVPEReactivated: 0},
			{PenaltyLevel: 1, MinEarlyQuits: 3, MaxEarlyQuits: 5, MMLockoutSec: 120, SpawnLock: 0, AutoReport: 0, CNVPEReactivated: 0},
			{PenaltyLevel: 2, MinEarlyQuits: 6, MaxEarlyQuits: 10, MMLockoutSec: 300, SpawnLock: 1, AutoReport: 0, CNVPEReactivated: 0},
			{PenaltyLevel: 3, MinEarlyQuits: 11, MaxEarlyQuits: 999, MMLockoutSec: 900, SpawnLock: 1, AutoReport: 1, CNVPEReactivated: 1},
		},
		SteadyPlayerLevels: []EarlyQuitSteadyPlayerLevelConfig{
			{SteadyPlayerLevel: 0, MinNumMatches: 0, MinSteadyRatio: 0.0},
			{SteadyPlayerLevel: 1, MinNumMatches: 10, MinSteadyRatio: 0.9},
			{SteadyPlayerLevel: 2, MinNumMatches: 25, MinSteadyRatio: 0.95},
		},
	}
}

// Validate ensures the config is sequentially valid and fixes any invalid values.
func (m *SNSEarlyQuitConfig) Validate() {
	if m == nil {
		return
	}
	m.validatePenaltyLevels()
	m.validateSteadyPlayerLevels()
}

func (m *SNSEarlyQuitConfig) validatePenaltyLevels() {
	defaults := DefaultEarlyQuitLevels()
	if len(m.PenaltyLevels) == 0 {
		m.PenaltyLevels = defaults.PenaltyLevels
		return
	}

	sort.Slice(m.PenaltyLevels, func(i, j int) bool {
		return m.PenaltyLevels[i].PenaltyLevel < m.PenaltyLevels[j].PenaltyLevel
	})

	for i := range m.PenaltyLevels {
		level := &m.PenaltyLevels[i]

		if level.MinEarlyQuits > level.MaxEarlyQuits {
			level.MinEarlyQuits, level.MaxEarlyQuits = level.MaxEarlyQuits, level.MinEarlyQuits
		}
		if level.MinEarlyQuits < 0 {
			level.MinEarlyQuits = 0
		}
		if level.MaxEarlyQuits < 0 {
			level.MaxEarlyQuits = 0
		}
		if level.MMLockoutSec < 0 {
			level.MMLockoutSec = 0
		}
		if level.SpawnLock < 0 {
			level.SpawnLock = 0
		}
		if level.AutoReport < 0 {
			level.AutoReport = 0
		}
		if level.CNVPEReactivated < 0 {
			level.CNVPEReactivated = 0
		}

		if i > 0 {
			prev := &m.PenaltyLevels[i-1]
			if level.MinEarlyQuits <= prev.MaxEarlyQuits {
				level.MinEarlyQuits = prev.MaxEarlyQuits + 1
			}
			if level.MaxEarlyQuits < level.MinEarlyQuits {
				level.MaxEarlyQuits = level.MinEarlyQuits
			}
		}
	}
}

func (m *SNSEarlyQuitConfig) validateSteadyPlayerLevels() {
	defaults := DefaultEarlyQuitLevels()
	if len(m.SteadyPlayerLevels) == 0 {
		m.SteadyPlayerLevels = defaults.SteadyPlayerLevels
		return
	}

	sort.Slice(m.SteadyPlayerLevels, func(i, j int) bool {
		return m.SteadyPlayerLevels[i].SteadyPlayerLevel < m.SteadyPlayerLevels[j].SteadyPlayerLevel
	})

	for i := range m.SteadyPlayerLevels {
		level := &m.SteadyPlayerLevels[i]

		if level.MinNumMatches < 0 {
			level.MinNumMatches = 0
		}
		if level.MinSteadyRatio < 0 {
			level.MinSteadyRatio = 0
		}
		if level.MinSteadyRatio > 1.0 {
			level.MinSteadyRatio = 1.0
		}

		if i > 0 {
			prev := &m.SteadyPlayerLevels[i-1]
			if level.MinNumMatches <= prev.MinNumMatches {
				level.MinNumMatches = prev.MinNumMatches + 1
			}
			if level.MinSteadyRatio < prev.MinSteadyRatio {
				level.MinSteadyRatio = prev.MinSteadyRatio
			}
		}
	}
}
