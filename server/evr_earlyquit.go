package server

import (
	"context"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	StorageCollectionEarlyQuit = "EarlyQuit"
	StorageKeyEarlyQuit        = "statistics"
	MinEarlyQuitPenaltyLevel   = -1
	MaxEarlyQuitPenaltyLevel   = 3

	// Matchmaking Tier Constants
	// Tier 1 (internal value 1) = Good standing
	// Tier 2 (internal value 2) = Penalty tier
	// Tier 3+ (internal value 3+) = Reserved for future expansion
	MatchmakingTier1 = 1
	MatchmakingTier2 = 2

	// Discord DM messages for tier changes
	TierDegradedMessage = "Matchmaking Status Update: Account flagged for early quitting.\nYou have been moved to the Tier 2 priority queue. All incoming Tier 1 requests\nare now cutting in front of you. You will remain in a holding pattern until no\nother players are available to match.\n\nComplete full matches to restore Tier 1 status."
	TierRestoredMessage = "Matchmaking Priority Restored: You have returned to Tier 1 status. Complete full matches to maintain your standing."
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
	return &EarlyQuitConfig{
		MatchmakingTier: MatchmakingTier1,
	}
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
	s.TotalEarlyQuits++
	s.PlayerReliabilityRating = CalculatePlayerReliabilityRating(s.TotalEarlyQuits, s.TotalCompletedMatches)
}

func (s *EarlyQuitConfig) IncrementCompletedMatches() {
	s.Lock()
	defer s.Unlock()
	s.LastEarlyQuitTime = time.Time{}
	s.EarlyQuitPenaltyLevel--
	if s.EarlyQuitPenaltyLevel < MinEarlyQuitPenaltyLevel {
		s.EarlyQuitPenaltyLevel = MinEarlyQuitPenaltyLevel // Reset penalty level
	}
	s.TotalCompletedMatches++
	s.PlayerReliabilityRating = CalculatePlayerReliabilityRating(s.TotalEarlyQuits, s.TotalCompletedMatches)
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
// If tier1Threshold is nil, defaults to 0 (players with penalty 0 or less are in good standing).
//
// Current implementation (Tier 1 and Tier 2 only):
//   - penalty <= tier1Threshold: Tier 1 (good standing)
//   - penalty > tier1Threshold: Tier 2 (penalty tier)
//
// Future Tier 3+ implementation pattern:
//   - penalty > tier2Threshold: Tier 3+
//   - penalty > tier1Threshold: Tier 2
//   - penalty <= tier1Threshold: Tier 1
func (s *EarlyQuitConfig) UpdateTier(tier1Threshold *int32) (oldTier, newTier int32, changed bool) {
	s.Lock()
	defer s.Unlock()

	oldTier = s.MatchmakingTier

	// Use default threshold if nil
	threshold := int32(0)
	if tier1Threshold != nil {
		threshold = *tier1Threshold
	}

	// Determine new tier based on penalty level
	if s.EarlyQuitPenaltyLevel <= threshold {
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

// CheckAndStrikeEarlyQuitIfLoggedOut is a goroutine function that checks if a player has logged out
// and removes the early quit penalty from their record if they have.
// This prevents penalizing players who quit the entire game rather than just that match.
//
// Parameters:
//   - ctx: Context for logging and operations
//   - logger: Logger for debug/error messages
//   - nk: Nakama runtime module for storage operations
//   - sessionRegistry: Registry to check for active sessions
//   - userID: User ID to check
//   - sessionID: Session ID that disconnected
//   - checkInterval: Duration to wait before checking if player logged out (grace period)
func CheckAndStrikeEarlyQuitIfLoggedOut(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, sessionRegistry SessionRegistry, userID, sessionID string, checkInterval time.Duration) {
	// Wait for the grace period before checking
	select {
	case <-time.After(checkInterval):
		// Grace period elapsed, proceed with check
	case <-ctx.Done():
		// Context cancelled, exit early
		return
	}

	// Check if the player is still logged in
	sessionUUID, err := uuid.FromString(sessionID)
	if err != nil {
		logger.WithField("error", err).Warn("Invalid session ID format for early quit check")
		return
	}

	// If session still exists, player is still logged in to this session
	// No action needed as they're still using the system
	if sessionRegistry.Get(sessionUUID) != nil {
		logger.WithFields(map[string]any{
			"uid":        userID,
			"session_id": sessionID,
		}).Debug("Player still has active session, early quit penalty remains")
		return
	}

	// Session doesn't exist, but we need to check if user has ANY active session
	// For now, we'll assume if this session is gone, we should remove the early quit
	// since it indicates they've fully logged out
	userUUID, err := uuid.FromString(userID)
	if err != nil {
		logger.WithField("error", err).Warn("Invalid user ID format for early quit check")
		return
	}

	// Load the player's early quit config
	eqconfig := NewEarlyQuitConfig()
	if err := StorableRead(ctx, nk, userUUID.String(), eqconfig, false); err != nil {
		logger.WithFields(map[string]any{
			"uid":   userID,
			"error": err,
		}).Warn("Failed to load early quit config for logout check")
		return
	}

	// Remove the early quit penalty by decrementing
	// This is equivalent to completing a match in terms of penalty reduction
	eqconfig.IncrementCompletedMatches()

	// Write the updated config back to storage
	if err := StorableWrite(ctx, nk, userUUID.String(), eqconfig); err != nil {
		logger.WithFields(map[string]any{
			"uid":   userID,
			"error": err,
		}).Warn("Failed to write early quit config after logout check")
		return
	}

	logger.WithFields(map[string]any{
		"uid":               userID,
		"new_penalty_level": eqconfig.GetPenaltyLevel(),
	}).Info("Removed early quit penalty: player logged out after early quit")
}
