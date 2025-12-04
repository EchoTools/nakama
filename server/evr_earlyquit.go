package server

import (
	"sync"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	StorageCollectionEarlyQuit = "EarlyQuit"
	StorageKeyEarlyQuit        = "statistics"
	MinEarlyQuitPenaltyLevel   = -1
	MaxEarlyQuitPenaltyLevel   = 3

	// Matchmaking Tier Constants
	// Tier 0 = Good standing (Tier 1)
	// Tier 1 = Penalty tier (Tier 2)
	// Tier 2+ = Reserved for future expansion (Tier 3+)
	MatchmakingTier1 = 1
	MatchmakingTier2 = 2
)

type EarlyQuitConfig struct {
	sync.Mutex
	EarlyQuitPenaltyLevel   int32     `json:"early_quit_penalty_level"`
	LastEarlyQuitTime       time.Time `json:"last_early_quit_time"`
	LastEarlyQuitMatchID    MatchID   `json:"last_early_quit_match_id"`
	TotalEarlyQuits         int32     `json:"total_early_quits"`
	TotalCompletedMatches   int32     `json:"total_completed_matches"`
	PlayerReliabilityRating float64   `json:"player_reliability_rating"`
	MatchmakingTier         int32     `json:"matchmaking_tier"`
	LastTierChange          time.Time `json:"last_tier_change"`

	version string
}

func CalculatePlayerReliabilityRating(earlyQuits, completedMatches int32) float64 {
	totalMatches := earlyQuits + completedMatches
	if totalMatches == 0 {
		return 1.0 // Default reliability rating when no matches played
	}
	return float64(completedMatches) / float64(totalMatches)
}

func NewEarlyQuitConfig() *EarlyQuitConfig {
	return &EarlyQuitConfig{}
}

func (s *EarlyQuitConfig) StorageMeta() StorableMetadata {
	s.Lock()
	defer s.Unlock()
	return StorableMetadata{
		Collection:      StorageCollectionEarlyQuit,
		Key:             StorageKeyEarlyQuit,
		PermissionRead:  runtime.STORAGE_PERMISSION_NO_READ,
		PermissionWrite: runtime.STORAGE_PERMISSION_NO_WRITE,
		Version:         s.version,
	}
}

func (s *EarlyQuitConfig) SetStorageMeta(meta StorableMetadata) {
	s.Lock()
	defer s.Unlock()
	s.version = meta.Version
}

func (s *EarlyQuitConfig) GetStorageVersion() string {
	s.Lock()
	defer s.Unlock()
	return s.version
}

func (s *EarlyQuitConfig) IncrementEarlyQuit() {
	s.Lock()
	defer s.Unlock()
	s.LastEarlyQuitTime = time.Now().UTC()
	s.EarlyQuitPenaltyLevel++
	if s.EarlyQuitPenaltyLevel > MaxEarlyQuitPenaltyLevel {
		s.EarlyQuitPenaltyLevel = MaxEarlyQuitPenaltyLevel // Max penalty level
	}
}

func (s *EarlyQuitConfig) IncrementCompletedMatches() {
	s.Lock()
	defer s.Unlock()
	s.LastEarlyQuitTime = time.Time{}
	s.EarlyQuitPenaltyLevel--
	if s.EarlyQuitPenaltyLevel < MinEarlyQuitPenaltyLevel {
		s.EarlyQuitPenaltyLevel = MinEarlyQuitPenaltyLevel // Reset penalty level
	}
}

func (s *EarlyQuitConfig) GetPenaltyLevel() int {
	s.Lock()
	defer s.Unlock()
	return int(s.EarlyQuitPenaltyLevel)
}

func (s *EarlyQuitConfig) GetTier() int32 {
	s.Lock()
	defer s.Unlock()
	return s.MatchmakingTier
}

// UpdateTier updates the matchmaking tier based on the penalty level and tier threshold.
// Returns (oldTier, newTier, changed) where changed indicates if the tier was modified.
//
// Current implementation (Tier 1 and Tier 2 only):
//   - penalty <= tier1Threshold: Tier 1 (good standing)
//   - penalty > tier1Threshold: Tier 2 (penalty tier)
//
// Future Tier 3+ implementation pattern:
//   - penalty > tier2Threshold: Tier 3+
//   - penalty > tier1Threshold: Tier 2
//   - penalty <= tier1Threshold: Tier 1
func (s *EarlyQuitConfig) UpdateTier(tier1Threshold int32) (oldTier, newTier int32, changed bool) {
	s.Lock()
	defer s.Unlock()

	oldTier = s.MatchmakingTier

	// Determine new tier based on penalty level
	if s.EarlyQuitPenaltyLevel <= tier1Threshold {
		newTier = MatchmakingTier1 // Good standing
	} else {
		newTier = MatchmakingTier2 // Penalty tier
	}

	// Check if tier changed
	if oldTier != newTier {
		s.MatchmakingTier = newTier
		s.LastTierChange = time.Now().UTC()
		changed = true
	}

	return oldTier, newTier, changed
}
