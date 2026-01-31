package evr

import (
	"fmt"
	"sort"
	"time"
)

const (
	StorageCollectionEarlyQuitConfig = "EarlyQuit"
	StorageKeyEarlyQuitConfig        = "config"
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
	version            string
	userID             string
}

func NewEarlyQuitServiceConfig() *EarlyQuitServiceConfig {
	config := DefaultEarlyQuitServiceConfig()
	config.version = "*"
	return config
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

// StorageVersion returns the storage version
func (m EarlyQuitServiceConfig) StorageVersion() string {
	return m.version
}

// SetStorageVersion updates the storage version
func (m *EarlyQuitServiceConfig) SetStorageVersion(version string) {
	m.version = version
}

// NewSNSEarlyQuitConfig creates a new SNS message for early quit config
func NewSNSEarlyQuitConfig(config *EarlyQuitServiceConfig) *EarlyQuitServiceConfig {
	if config == nil {
		config = DefaultEarlyQuitServiceConfig()
	}
	config.Validate()
	return config
}

// Validate ensures the config is sequentially valid and fixes any invalid values.
// It:
// - Sorts penalty levels by penalty level
// - Ensures min/max early quits are compatible and non-overlapping
// - Ensures steady player levels are sorted
// - Ensures steady player ratios are non-decreasing
func (m *EarlyQuitServiceConfig) Validate() {
	if m == nil {
		return
	}

	// Validate and sort penalty levels
	m.validatePenaltyLevels()

	// Validate and sort steady player levels
	m.validateSteadyPlayerLevels()
}

// validatePenaltyLevels ensures penalty levels are properly sequenced
func (m *EarlyQuitServiceConfig) validatePenaltyLevels() {
	if len(m.PenaltyLevels) == 0 {
		m.PenaltyLevels = DefaultEarlyQuitServiceConfig().PenaltyLevels
		return
	}

	// Sort by penalty level
	sort.Slice(m.PenaltyLevels, func(i, j int) bool {
		return m.PenaltyLevels[i].PenaltyLevel < m.PenaltyLevels[j].PenaltyLevel
	})

	// Validate and fix each level
	for i := range m.PenaltyLevels {
		level := &m.PenaltyLevels[i]

		// Ensure min <= max
		if level.MinEarlyQuits > level.MaxEarlyQuits {
			level.MinEarlyQuits, level.MaxEarlyQuits = level.MaxEarlyQuits, level.MinEarlyQuits
		}

		// Ensure non-negative values
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

		// Ensure no overlap with previous level
		if i > 0 {
			prevLevel := &m.PenaltyLevels[i-1]
			// Current level's min should be at least previous level's max + 1
			if level.MinEarlyQuits <= prevLevel.MaxEarlyQuits {
				level.MinEarlyQuits = prevLevel.MaxEarlyQuits + 1
			}
			// Ensure max is still >= min after adjustment
			if level.MaxEarlyQuits < level.MinEarlyQuits {
				level.MaxEarlyQuits = level.MinEarlyQuits + 10 // Give some reasonable range
			}
		}
	}

	// Regenerate penalty levels if they're invalid
	if len(m.PenaltyLevels) == 0 {
		m.PenaltyLevels = DefaultEarlyQuitServiceConfig().PenaltyLevels
	}
}

// validateSteadyPlayerLevels ensures steady player levels are properly sequenced
func (m *EarlyQuitServiceConfig) validateSteadyPlayerLevels() {
	if len(m.SteadyPlayerLevels) == 0 {
		m.SteadyPlayerLevels = DefaultEarlyQuitServiceConfig().SteadyPlayerLevels
		return
	}

	// Sort by steady player level
	sort.Slice(m.SteadyPlayerLevels, func(i, j int) bool {
		return m.SteadyPlayerLevels[i].SteadyPlayerLevel < m.SteadyPlayerLevels[j].SteadyPlayerLevel
	})

	// Validate and fix each level
	for i := range m.SteadyPlayerLevels {
		level := &m.SteadyPlayerLevels[i]

		// Ensure non-negative values
		if level.MinNumMatches < 0 {
			level.MinNumMatches = 0
		}
		if level.MinSteadyRatio < 0 {
			level.MinSteadyRatio = 0
		}
		if level.MinSteadyRatio > 1.0 {
			level.MinSteadyRatio = 1.0
		}

		// Ensure each level requires more matches than the previous
		if i > 0 {
			prevLevel := &m.SteadyPlayerLevels[i-1]
			if level.MinNumMatches <= prevLevel.MinNumMatches {
				level.MinNumMatches = prevLevel.MinNumMatches + 1
			}
			// Optionally ensure ratio is non-decreasing
			if level.MinSteadyRatio < prevLevel.MinSteadyRatio {
				level.MinSteadyRatio = prevLevel.MinSteadyRatio
			}
		}
	}

	// Regenerate steady player levels if they're invalid
	if len(m.SteadyPlayerLevels) == 0 {
		m.SteadyPlayerLevels = DefaultEarlyQuitServiceConfig().SteadyPlayerLevels
	}
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
