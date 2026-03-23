package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
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

// EarlyQuitLockoutDurations maps penalty levels to their lockout durations
var EarlyQuitLockoutDurations = map[int]time.Duration{
	0: 0 * time.Second,   // No lockout
	1: 120 * time.Second, // 2 minutes
	2: 300 * time.Second, // 5 minutes
	3: 900 * time.Second, // 15 minutes
}

// EarlyQuitGuildOverride stores per-guild enforcer overrides for a player's
// early quit behavior. These are set via the earlyquit/modify RPC.
type EarlyQuitGuildOverride struct {
	Exempt         bool   `json:"exempt"`                    // Exempt from early quit tracking in this guild
	PenaltyLevel   *int32 `json:"penalty_level,omitempty"`   // Override penalty level (nil = use global)
	ModeratorNotes string `json:"moderator_notes,omitempty"` // Internal enforcer notes
}

type EarlyQuitPlayerState struct {
	sync.Mutex `json:"-"`

	// Binary-aligned fields (match earlyquit| profile keys)
	PenaltyTimestamp    int64 `json:"penalty_ts"`              // Absolute expiry timestamp (unix seconds), -1 = none
	NumEarlyQuits       int32 `json:"num_early_quits"`         // Accumulating early quit count
	NumSteadyMatches    int32 `json:"num_steady_matches"`      // Matches counted toward steady player status
	NumSteadyEarlyQuits int32 `json:"num_steady_early_quits"`  // Quits counted toward steady player status
	PenaltyLevel        int32 `json:"penalty_level"`           // Resolved from config (clamped uint8)
	SteadyPlayerLevel   int32 `json:"steady_player_level"`     // Resolved from config (clamped uint8)

	// Nakama extensions (kept for matchmaker/UI compat)
	TotalCompletedMatches      int32     `json:"total_completed_matches"`
	MatchmakingTier            int32     `json:"matchmaking_tier"`
	LastEarlyQuitMatchID       MatchID   `json:"last_early_quit_match_id"`
	LastTierChange             time.Time `json:"last_tier_change"`
	LastExpiryNotificationSent time.Time `json:"last_expiry_notification_sent"`

	// Guild-scoped overrides (per guild)
	GuildOverrides map[string]*EarlyQuitGuildOverride `json:"guild_overrides,omitempty"`

	version string
}

// earlyQuitPlayerStateLegacy is used to detect and migrate old storage format.
type earlyQuitPlayerStateLegacy struct {
	EarlyQuitPenaltyLevel int32     `json:"early_quit_penalty_level"`
	LastEarlyQuitTime     time.Time `json:"last_early_quit_time"`
	TotalEarlyQuits       int32     `json:"total_early_quits"`
}

// UnmarshalJSON handles backward-compatible deserialization from old storage format.
func (s *EarlyQuitPlayerState) UnmarshalJSON(data []byte) error {
	// First try the new format (aliased struct to avoid recursion)
	type Alias EarlyQuitPlayerState
	aux := &struct {
		*Alias

		// Old field names for migration
		OldPenaltyLevel int32     `json:"early_quit_penalty_level"`
		OldLastQuitTime time.Time `json:"last_early_quit_time"`
		OldTotalQuits   int32     `json:"total_early_quits"`
		OldReliability  float64   `json:"player_reliability_rating"`
	}{
		Alias: (*Alias)(s),
	}

	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	// Migrate from old format if new fields are zero but old fields are present
	if s.NumEarlyQuits == 0 && aux.OldTotalQuits > 0 {
		s.NumEarlyQuits = aux.OldTotalQuits
	}
	if s.PenaltyLevel == 0 && aux.OldPenaltyLevel != 0 {
		s.PenaltyLevel = aux.OldPenaltyLevel
	}
	if s.PenaltyTimestamp == 0 && !aux.OldLastQuitTime.IsZero() {
		// Convert old relative lockout to absolute timestamp
		lockout := GetLockoutDuration(int(s.PenaltyLevel))
		s.PenaltyTimestamp = aux.OldLastQuitTime.Add(lockout).Unix()
	}

	return nil
}

// ResolvePenaltyLevel determines the penalty level and lockout duration from the
// config's penalty_levels array, matching the binary's algorithm: find the first
// level where numQuits >= min AND numQuits <= max.
func ResolvePenaltyLevel(numQuits int32, cfg *evr.SNSEarlyQuitConfig) (level int32, lockoutSec int32) {
	if cfg == nil {
		return 0, 0
	}
	for _, pl := range cfg.PenaltyLevels {
		if int(numQuits) >= pl.MinEarlyQuits && int(numQuits) <= pl.MaxEarlyQuits {
			return int32(pl.PenaltyLevel), int32(pl.MMLockoutSec)
		}
	}
	// If no level matches (quit count above all ranges), use the highest level
	if len(cfg.PenaltyLevels) > 0 {
		last := cfg.PenaltyLevels[len(cfg.PenaltyLevels)-1]
		return int32(last.PenaltyLevel), int32(last.MMLockoutSec)
	}
	return 0, 0
}

// ResolveSteadyPlayerLevel determines the steady player level from the config.
// Binary algorithm: ratio = (matches - quits) / matches; find level where
// matches >= min_num_matches AND ratio >= min_steady_ratio.
func ResolveSteadyPlayerLevel(steadyMatches, steadyEarlyQuits int32, cfg *evr.SNSEarlyQuitConfig) int32 {
	if cfg == nil || steadyMatches <= 0 {
		return 0
	}
	ratio := float64(steadyMatches-steadyEarlyQuits) / float64(steadyMatches)
	var best int32
	for _, sl := range cfg.SteadyPlayerLevels {
		if int(steadyMatches) >= sl.MinNumMatches && ratio >= sl.MinSteadyRatio {
			if int32(sl.SteadyPlayerLevel) > best {
				best = int32(sl.SteadyPlayerLevel)
			}
		}
	}
	return best
}

// GetLockoutDuration returns the lockout duration for a given penalty level.
// Uses the hardcoded fallback map (deprecated — callers should use ResolvePenaltyLevel with config).
func GetLockoutDuration(penaltyLevel int) time.Duration {
	duration, ok := EarlyQuitLockoutDurations[penaltyLevel]
	if !ok {
		return 0
	}
	return duration
}

// GetLockoutDurationSeconds returns the lockout duration in seconds for a given penalty level.
func GetLockoutDurationSeconds(penaltyLevel int) int32 {
	return int32(GetLockoutDuration(penaltyLevel).Seconds())
}

func NewEarlyQuitPlayerState() *EarlyQuitPlayerState {
	return &EarlyQuitPlayerState{
		MatchmakingTier: MatchmakingTier1,
	}
}

func (s *EarlyQuitPlayerState) StorageMeta() StorableMetadata {
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

func (s *EarlyQuitPlayerState) SetStorageMeta(meta StorableMetadata) {
	s.Lock()
	defer s.Unlock()
	s.version = meta.Version
}

func (s *EarlyQuitPlayerState) GetStorageVersion() string {
	s.Lock()
	defer s.Unlock()
	return s.version
}

// IncrementEarlyQuit records an early quit. Increments counters and re-resolves
// penalty level from the config. The binary model only increments — no decrement.
func (s *EarlyQuitPlayerState) IncrementEarlyQuit() {
	s.Lock()
	defer s.Unlock()
	s.NumEarlyQuits++
	s.NumSteadyEarlyQuits++
	// PenaltyLevel and PenaltyTimestamp are set by the caller after resolving from config.
}

// IncrementCompletedMatches records a completed match.
// In the binary model, completed matches increment steady counters but do NOT
// decrement the penalty level — penalty is always resolved from quit count.
func (s *EarlyQuitPlayerState) IncrementCompletedMatches() {
	s.Lock()
	defer s.Unlock()
	s.TotalCompletedMatches++
	s.NumSteadyMatches++
	// PenaltyLevel is re-resolved by the caller from config.
}

// ForgiveLastQuit decrements NumEarlyQuits (e.g., player logged out entirely).
// The binary has no explicit forgiveness mechanism, but this is a nakama extension
// to handle genuine crashes/network issues.
func (s *EarlyQuitPlayerState) ForgiveLastQuit() {
	s.Lock()
	defer s.Unlock()
	if s.NumEarlyQuits > 0 {
		s.NumEarlyQuits--
	}
	if s.NumSteadyEarlyQuits > 0 {
		s.NumSteadyEarlyQuits--
	}
	// PenaltyLevel and PenaltyTimestamp are re-resolved by the caller.
}

// IsPenaltyActive returns true if the penalty lockout has not yet expired.
func (s *EarlyQuitPlayerState) IsPenaltyActive() bool {
	s.Lock()
	defer s.Unlock()
	return s.PenaltyTimestamp > 0 && time.Now().Unix() < s.PenaltyTimestamp
}

func (s *EarlyQuitPlayerState) GetPenaltyLevel() int {
	s.Lock()
	defer s.Unlock()
	return int(s.PenaltyLevel)
}

func (s *EarlyQuitPlayerState) GetTier() int32 {
	s.Lock()
	defer s.Unlock()
	return s.MatchmakingTier
}

func (s *EarlyQuitPlayerState) GetEarlyQuitCount() int32 {
	s.Lock()
	defer s.Unlock()
	return s.NumEarlyQuits
}

// GetEffectivePenaltyLevel returns the penalty level for a specific guild,
// checking guild overrides first, falling back to global.
func (s *EarlyQuitPlayerState) GetEffectivePenaltyLevel(groupID string) int32 {
	s.Lock()
	defer s.Unlock()
	if s.GuildOverrides != nil {
		if override, ok := s.GuildOverrides[groupID]; ok && override.PenaltyLevel != nil {
			return *override.PenaltyLevel
		}
	}
	return s.PenaltyLevel
}

// IsExempt returns true if the player is exempt from early quit tracking in the given guild.
func (s *EarlyQuitPlayerState) IsExempt(groupID string) bool {
	s.Lock()
	defer s.Unlock()
	if s.GuildOverrides != nil {
		if override, ok := s.GuildOverrides[groupID]; ok {
			return override.Exempt
		}
	}
	return false
}

// UpdateTier updates the matchmaking tier based on the penalty level and tier threshold.
// Returns (oldTier, newTier, changed) where changed indicates if the tier was modified.
func (s *EarlyQuitPlayerState) UpdateTier(tier1Threshold *int32) (oldTier, newTier int32, changed bool) {
	s.Lock()
	defer s.Unlock()

	oldTier = s.MatchmakingTier

	threshold := int32(0)
	if tier1Threshold != nil {
		threshold = *tier1Threshold
	}

	if s.PenaltyLevel <= threshold {
		newTier = MatchmakingTier1
	} else {
		newTier = MatchmakingTier2
	}

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
//   - ctx: Context for logging and operations (should be a background context, not match context)
//   - logger: Logger for debug/error messages
//   - nk: Nakama runtime module for storage operations
//   - db: Database connection for Discord ID lookup
//   - sessionRegistry: Registry to check for active sessions
//   - userID: User ID to check
//   - sessionID: Session ID that disconnected
//   - checkInterval: Duration to wait before checking if player logged out (grace period)
func CheckAndStrikeEarlyQuitIfLoggedOut(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, db *sql.DB, sessionRegistry SessionRegistry, userID, sessionID string, checkInterval time.Duration) {
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
	eqconfig := NewEarlyQuitPlayerState()
	if err := StorableRead(ctx, nk, userUUID.String(), eqconfig, false); err != nil {
		logger.WithFields(map[string]any{
			"uid":   userID,
			"error": err,
		}).Warn("Failed to load early quit config for logout check")
		return
	}

	// Load and update the detailed quit history to mark the last quit as forgiven
	history := NewEarlyQuitHistory(userID)
	if err := StorableRead(ctx, nk, userID, history, false); err != nil {
		logger.WithFields(map[string]any{
			"uid":   userID,
			"error": err,
		}).Debug("Failed to load early quit history for forgiveness")
	} else {
		// Forgive the most recent quit for the last match they quit from
		if !eqconfig.LastEarlyQuitMatchID.IsNil() && history.ForgiveQuit(eqconfig.LastEarlyQuitMatchID) {
			if err := StorableWrite(ctx, nk, userID, history); err != nil {
				logger.WithFields(map[string]any{
					"uid":   userID,
					"error": err,
				}).Warn("Failed to write forgiven early quit history")
			} else {
				logger.WithFields(map[string]any{
					"uid":      userID,
					"match_id": eqconfig.LastEarlyQuitMatchID.String(),
				}).Debug("Marked early quit as forgiven in detailed history")
			}
		}
	}

	// Reduce early quit penalty by one level without inflating match completion statistics.
	// This forgives the early quit since the player logged out entirely rather than
	// just leaving the match to join another.
	eqconfig.ForgiveLastQuit()

	// Check if tier should be updated after penalty reduction
	serviceSettings := ServiceSettings()
	oldTier, newTier, tierChanged := eqconfig.UpdateTier(serviceSettings.Matchmaking.EarlyQuitTier1Threshold)

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
		"old_penalty_level": eqconfig.GetPenaltyLevel() + 1, // +1 because we just decremented it
		"new_penalty_level": eqconfig.GetPenaltyLevel(),
		"old_tier":          oldTier,
		"new_tier":          newTier,
		"tier_changed":      tierChanged,
	}).Info("Reduced early quit penalty: player logged out after early quit")

	// Send notifications if tier changed (unless penalty system is disabled or silent mode is enabled)
	if tierChanged && serviceSettings.Matchmaking.EnableEarlyQuitPenalty && !serviceSettings.Matchmaking.SilentEarlyQuitSystem {
		if messageTrigger := globalEarlyQuitMessageTrigger.Load(); messageTrigger != nil {
			messageTrigger.SendEarlyQuitUpdateNotification(ctx, userID, eqconfig)
		}

		discordID, err := GetDiscordIDByUserID(ctx, db, userID)
		if err != nil {
			logger.WithFields(map[string]any{
				"uid":   userID,
				"error": err,
			}).Warn("Failed to get Discord ID for tier notification after logout")
		} else if appBot := globalAppBot.Load(); appBot != nil && appBot.dg != nil {
			var message string
			if newTier < oldTier {
				// Recovered to Tier 1
				message = TierRestoredMessage
			} else {
				// This shouldn't happen when decrementing penalty, but handle it
				message = TierDegradedMessage
			}
			if _, err := SendUserMessage(ctx, appBot.dg, discordID, message); err != nil {
				logger.WithFields(map[string]any{
					"uid":        userID,
					"discord_id": discordID,
					"error":      err,
				}).Warn("Failed to send tier change notification after logout")
			}
		}
	}
}

// CheckAndApplyEarlyQuitIfStillOnline is the inverse of CheckAndStrikeEarlyQuitIfLoggedOut.
// For disconnects where penalty was deferred: if the player is still online after the grace
// period, they likely force-closed intentionally, so apply the penalty. If they're offline,
// they genuinely crashed/had a network issue, so forgive it.
func CheckAndApplyEarlyQuitIfStillOnline(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, db *sql.DB, sessionRegistry SessionRegistry, userID, sessionID string, matchID MatchID, checkInterval time.Duration) {
	select {
	case <-time.After(checkInterval):
	case <-ctx.Done():
		return
	}

	sessionUUID, err := uuid.FromString(sessionID)
	if err != nil {
		logger.WithField("error", err).Warn("Invalid session ID for disconnect penalty check")
		return
	}

	// If session is gone, player logged out — genuine crash/disconnect, no penalty.
	if sessionRegistry.Get(sessionUUID) == nil {
		logger.WithFields(map[string]any{
			"uid":        userID,
			"session_id": sessionID,
		}).Info("Disconnect forgiven: player logged out (likely crash/network issue)")
		return
	}

	// Player is still online — this was likely an intentional force-close. Apply penalty.
	logger.WithFields(map[string]any{
		"uid":        userID,
		"session_id": sessionID,
	}).Info("Applying deferred penalty: player still online after disconnect")

	eqconfig := NewEarlyQuitPlayerState()
	if err := StorableRead(ctx, nk, userID, eqconfig, true); err != nil {
		logger.WithField("error", err).Warn("Failed to load early quit config for deferred penalty")
		return
	}

	eqconfig.IncrementEarlyQuit()
	eqconfig.LastEarlyQuitMatchID = matchID

	serviceSettings := ServiceSettings()
	oldTier, newTier, tierChanged := eqconfig.UpdateTier(serviceSettings.Matchmaking.EarlyQuitTier1Threshold)

	if err := StorableWrite(ctx, nk, userID, eqconfig); err != nil {
		logger.WithField("error", err).Warn("Failed to write deferred early quit penalty")
		return
	}

	// Update session cache
	if s := sessionRegistry.Get(sessionUUID); s != nil {
		if params, ok := LoadParams(s.Context()); ok {
			params.earlyQuitConfig.Store(eqconfig)
		}
	}

	// Send notifications (unless penalty system is disabled or silent mode is enabled)
	if serviceSettings.Matchmaking.EnableEarlyQuitPenalty && !serviceSettings.Matchmaking.SilentEarlyQuitSystem {
		if messageTrigger := globalEarlyQuitMessageTrigger.Load(); messageTrigger != nil {
			messageTrigger.SendEarlyQuitUpdateNotification(ctx, userID, eqconfig)
		}

		if tierChanged {
			discordID, err := GetDiscordIDByUserID(ctx, db, userID)
			if err == nil {
				if appBot := globalAppBot.Load(); appBot != nil && appBot.dg != nil {
					var message string
					if newTier > oldTier {
						message = TierDegradedMessage
					} else {
						message = TierRestoredMessage
					}
					if _, err := SendUserMessage(ctx, appBot.dg, discordID, message); err != nil {
						logger.WithField("error", err).Warn("Failed to send tier change DM for deferred penalty")
					}
				}
			}
		}
	}
}
