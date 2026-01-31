package server

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SNSEarlyQuitMessageTrigger manages sending early quit SNS messages to connected players
type SNSEarlyQuitMessageTrigger struct {
	sync.Mutex
	pipeline *EvrPipeline
	logger   *zap.Logger
	nk       runtime.NakamaModule
	db       *sql.DB
	stopCh   chan struct{}
	stopped  bool
}

// EarlyQuitServiceConfigStorable wraps the config for storage operations
type EarlyQuitServiceConfigStorable struct {
	*evr.EarlyQuitServiceConfig
	meta StorableMetadata
}

func (c *EarlyQuitServiceConfigStorable) StorageMeta() StorableMetadata {
	return StorableMetadata{
		Collection:      evr.StorageCollectionEarlyQuitConfig,
		Key:             evr.StorageKeyEarlyQuitConfig,
		PermissionRead:  runtime.STORAGE_PERMISSION_NO_READ,
		PermissionWrite: runtime.STORAGE_PERMISSION_NO_WRITE,
		Version:         c.EarlyQuitServiceConfig.StorageVersion(),
		UserID:          c.meta.UserID,
	}
}

func (c *EarlyQuitServiceConfigStorable) SetStorageMeta(meta StorableMetadata) {
	c.meta = meta
	c.EarlyQuitServiceConfig.SetStorageVersion(meta.Version)
}

// LoadEarlyQuitServiceConfig loads the early quit service config from storage,
// or creates it with defaults if not found or blank. The config is validated and fixed if invalid.
func LoadEarlyQuitServiceConfig(ctx context.Context, nk runtime.NakamaModule, logger *zap.Logger) *evr.EarlyQuitServiceConfig {
	configStorable := &EarlyQuitServiceConfigStorable{
		EarlyQuitServiceConfig: evr.NewEarlyQuitServiceConfig(),
	}

	// Try to load from storage (create=false to handle NotFound explicitly)
	err := StorableRead(ctx, nk, SystemUserID, configStorable, false)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			// Config doesn't exist, create it with defaults
			config := evr.DefaultEarlyQuitServiceConfig()
			config.SetStorageVersion("*")

			// Validate before writing to ensure defaults are correct
			config.Validate()
			configStorable.EarlyQuitServiceConfig = config
			if writeErr := StorableWrite(ctx, nk, SystemUserID, configStorable); writeErr != nil {
				if logger != nil {
					logger.Warn("Failed to write early quit config to storage",
						zap.Error(writeErr))
				}
			}
			return config
		}

		// Some other error occurred; suppress logs if the context was canceled.
		if ctx.Err() == nil {
			if logger != nil {
				logger.Warn("Failed to load early quit config from storage, using defaults",
					zap.Error(err))
			}
		}
		// Use defaults but don't write
		config := evr.DefaultEarlyQuitServiceConfig()
		config.SetStorageVersion("*")
		return config
	}

	config := configStorable.EarlyQuitServiceConfig

	// Check if the loaded config is blank and populate with defaults if needed
	if len(config.PenaltyLevels) == 0 || len(config.SteadyPlayerLevels) == 0 {
		defaults := evr.DefaultEarlyQuitServiceConfig()
		if len(config.PenaltyLevels) == 0 {
			config.PenaltyLevels = defaults.PenaltyLevels
		}
		if len(config.SteadyPlayerLevels) == 0 {
			config.SteadyPlayerLevels = defaults.SteadyPlayerLevels
		}

		// Write the populated config back to storage
		config.SetStorageVersion("*")
		configStorable.EarlyQuitServiceConfig = config
		if writeErr := StorableWrite(ctx, nk, SystemUserID, configStorable); writeErr != nil {
			if logger != nil {
				logger.Warn("Failed to write populated early quit config to storage",
					zap.Error(writeErr))
			}
		}
	}

	// Validate and fix any invalid values
	// Track if config was mutated to determine if we need to write it back
	// We use a simple heuristic: if validation changes the slice lengths or values, write back
	originalPenaltyCount := len(config.PenaltyLevels)
	originalSteadyCount := len(config.SteadyPlayerLevels)

	// Create a snapshot of first penalty level if exists (to detect mutations)
	var firstPenaltySnapshot *evr.EarlyQuitPenaltyLevelConfig
	if len(config.PenaltyLevels) > 0 {
		snapshot := config.PenaltyLevels[0]
		firstPenaltySnapshot = &snapshot
	}

	config.Validate()

	// Check if validation mutated the config
	configMutated := originalPenaltyCount != len(config.PenaltyLevels) ||
		originalSteadyCount != len(config.SteadyPlayerLevels) ||
		(firstPenaltySnapshot != nil && len(config.PenaltyLevels) > 0 &&
			config.PenaltyLevels[0] != *firstPenaltySnapshot)

	// Write back to storage if validation mutated the config
	if configMutated {
		config.SetStorageVersion("*")
		configStorable.EarlyQuitServiceConfig = config
		if writeErr := StorableWrite(ctx, nk, SystemUserID, configStorable); writeErr != nil {
			if logger != nil {
				logger.Warn("Failed to write validated early quit config to storage",
					zap.Error(writeErr))
			}
		}
	}

	return config
}

// NewSNSEarlyQuitMessageTrigger creates a new message trigger
func NewSNSEarlyQuitMessageTrigger(pipeline *EvrPipeline, logger *zap.Logger, nk runtime.NakamaModule, db *sql.DB) *SNSEarlyQuitMessageTrigger {
	return &SNSEarlyQuitMessageTrigger{
		pipeline: pipeline,
		logger:   logger,
		nk:       nk,
		db:       db,
		stopCh:   make(chan struct{}),
	}
}

// getEvrSessions retrieves all EVR sessions for a given user
func (t *SNSEarlyQuitMessageTrigger) getEvrSessions(userID string) []*sessionWS {
	var sessions []*sessionWS
	parsedUserID, err := uuid.FromString(userID)
	if err != nil {
		t.logger.Warn("Invalid user ID", zap.String("user_id", userID), zap.Error(err))
		return sessions
	}

	t.pipeline.sessionRegistry.Range(func(session Session) bool {
		if session.UserID() == parsedUserID && session.Format() == SessionFormatEVR {
			if evrSession, ok := session.(*sessionWS); ok {
				sessions = append(sessions, evrSession)
			}
		}
		return true
	})

	return sessions
}

// SendEvrMessage sends an EVR message directly to all EVR sessions of a user
func (t *SNSEarlyQuitMessageTrigger) SendEvrMessage(userID string, message evr.Message) bool {
	sessions := t.getEvrSessions(userID)
	if len(sessions) == 0 {
		t.logger.Debug("No EVR sessions found for user",
			zap.String("user_id", userID))
		return false
	}

	for _, session := range sessions {
		if err := session.SendEvr(message); err != nil {
			t.logger.Warn("Failed to send message to session",
				zap.String("user_id", userID),
				zap.String("session_id", session.ID().String()),
				zap.Error(err))
		}
	}
	return true
}

// SendEarlyQuitConfigOnLogin sends the SNSEarlyQuitConfig message when a player logs in
// This provides the client with the current penalty tier configuration
func (t *SNSEarlyQuitMessageTrigger) SendEarlyQuitConfigOnLogin(ctx context.Context, session *sessionWS) error {
	if session == nil {
		return nil
	}

	// Load the configuration from storage (or defaults)
	config := LoadEarlyQuitServiceConfig(ctx, t.nk, t.logger)

	// Create the SNS message wrapper
	message := evr.NewSNSEarlyQuitConfig(config)

	// Send to the player
	if err := session.SendEvr(message); err != nil {
		t.logger.Warn("Failed to send early quit config on login",
			zap.String("user_id", session.userID.String()),
			zap.Error(err))
		return err
	}

	t.logger.Debug("Sent SNSEarlyQuitConfig on login",
		zap.String("user_id", session.userID.String()))

	return nil
}

// SendFeatureFlagsOnLogin sends the SNSEarlyQuitFeatureFlags message when a player logs in
// This controls which early quit features are enabled/disabled
func (t *SNSEarlyQuitMessageTrigger) SendFeatureFlagsOnLogin(ctx context.Context, session *sessionWS) error {
	if session == nil {
		return nil
	}

	// Get the default feature flags
	flags := evr.DefaultEarlyQuitFeatureFlags()

	// Send to the player
	if err := session.SendEvr(flags); err != nil {
		t.logger.Warn("Failed to send early quit feature flags on login",
			zap.String("user_id", session.userID.String()),
			zap.Error(err))
		return err
	}

	t.logger.Debug("Sent SNSEarlyQuitFeatureFlags on login",
		zap.String("user_id", session.userID.String()))

	return nil
}

// SendPenaltyAppliedNotification sends a notification when a player receives a new penalty
// This is triggered when an early quit is recorded
func (t *SNSEarlyQuitMessageTrigger) SendPenaltyAppliedNotification(ctx context.Context, userID string, penaltyLevel int32, durationSeconds int32, reason string) error {
	// Create the notification
	notification := evr.NewPenaltyAppliedNotification(penaltyLevel, durationSeconds, reason)

	// Send to all sessions for this user
	if found := t.SendEvrMessage(userID, notification); !found {
		t.logger.Debug("Player not connected for penalty notification",
			zap.String("user_id", userID))
	}

	t.logger.Debug("Sent penalty applied notification",
		zap.String("user_id", userID),
		zap.Int32("penalty_level", penaltyLevel),
		zap.Int32("duration_seconds", durationSeconds))

	return nil
}

// SendPenaltyExpiredNotification sends a notification when a player's penalty expires
// This is triggered after the lockout duration has passed
func (t *SNSEarlyQuitMessageTrigger) SendPenaltyExpiredNotification(ctx context.Context, userID string) error {
	// Create the notification
	notification := evr.NewPenaltyExpiredNotification()

	// Send to all sessions for this user
	if found := t.SendEvrMessage(userID, notification); !found {
		t.logger.Debug("Player not connected for penalty expired notification",
			zap.String("user_id", userID))
	}

	t.logger.Debug("Sent penalty expired notification",
		zap.String("user_id", userID))

	return nil
}

// SendTierChangeNotification sends a notification when a player's matchmaking tier changes
// This is triggered when penalty causes tier degradation or completion causes tier restoration
func (t *SNSEarlyQuitMessageTrigger) SendTierChangeNotification(ctx context.Context, userID string, oldTier, newTier int32, isDegradation bool) error {
	var notification *evr.SNSEarlyQuitUpdateNotification
	if isDegradation {
		notification = evr.NewTierDegradedNotification(oldTier, newTier, "Your matchmaking tier has been downgraded due to early quitting.")
	} else {
		notification = evr.NewTierRestoredNotification(oldTier, newTier, "Your matchmaking tier has been restored.")
	}

	// Send to all sessions for this user
	if found := t.SendEvrMessage(userID, notification); !found {
		t.logger.Debug("Player not connected for tier change notification",
			zap.String("user_id", userID))
	}

	eventType := "tier_degraded"
	if !isDegradation {
		eventType = "tier_restored"
	}

	t.logger.Debug("Sent tier change notification",
		zap.String("user_id", userID),
		zap.String("event_type", eventType),
		zap.Int32("old_tier", oldTier),
		zap.Int32("new_tier", newTier))

	return nil
}

// SendLockoutNotification sends a notification when matchmaking lockout becomes active or clears
func (t *SNSEarlyQuitMessageTrigger) SendLockoutNotification(ctx context.Context, userID string, penaltyLevel int32, durationSeconds int32, isActive bool) error {
	var notification *evr.SNSEarlyQuitUpdateNotification
	if isActive {
		notification = evr.NewLockoutActiveNotification(penaltyLevel, durationSeconds)
	} else {
		notification = evr.NewLockoutClearedNotification()
	}

	// Send to all sessions for this user
	if found := t.SendEvrMessage(userID, notification); !found {
		t.logger.Debug("Player not connected for lockout notification",
			zap.String("user_id", userID))
	}

	eventType := "lockout_active"
	if !isActive {
		eventType = "lockout_cleared"
	}

	t.logger.Debug("Sent lockout notification",
		zap.String("user_id", userID),
		zap.String("event_type", eventType),
		zap.Int32("penalty_level", penaltyLevel),
		zap.Int32("duration_seconds", durationSeconds))

	return nil
}

// BroadcastFeatureFlagsUpdate sends updated feature flags to all connected players
// This is used when feature flags change server-side
func (t *SNSEarlyQuitMessageTrigger) BroadcastFeatureFlagsUpdate(ctx context.Context, flags *evr.SNSEarlyQuitFeatureFlags) error {
	if flags == nil {
		flags = evr.DefaultEarlyQuitFeatureFlags()
	}

	count := 0
	// Broadcast to all connected EVR sessions
	t.pipeline.sessionRegistry.Range(func(session Session) bool {
		if session.Format() == SessionFormatEVR {
			if evrSession, ok := session.(*sessionWS); ok {
				if err := evrSession.SendEvr(flags); err != nil {
					t.logger.Warn("Failed to broadcast feature flags update",
						zap.String("user_id", session.UserID().String()),
						zap.Error(err))
				} else {
					count++
				}
			}
		}
		return true
	})

	t.logger.Info("Broadcasted feature flags update to players", zap.Int("count", count))
	return nil
}

// BroadcastConfigUpdate sends updated penalty configuration to all connected players
// This is used when penalty tiers change server-side
func (t *SNSEarlyQuitMessageTrigger) BroadcastConfigUpdate(ctx context.Context, config *evr.EarlyQuitServiceConfig) error {
	if config == nil {
		config = evr.DefaultEarlyQuitServiceConfig()
	}

	message := evr.NewSNSEarlyQuitConfig(config)

	count := 0
	// Broadcast to all connected EVR sessions
	t.pipeline.sessionRegistry.Range(func(session Session) bool {
		if session.Format() == SessionFormatEVR {
			if evrSession, ok := session.(*sessionWS); ok {
				if err := evrSession.SendEvr(message); err != nil {
					t.logger.Warn("Failed to broadcast config update",
						zap.String("user_id", session.UserID().String()),
						zap.Error(err))
				} else {
					count++
				}
			}
		}
		return true
	})

	t.logger.Info("Broadcasted config update to players", zap.Int("count", count))
	return nil
}

// StartLockoutExpiryScheduler starts a background goroutine that monitors and sends penalty expiry notifications
// It checks for expired penalties periodically and sends notifications to connected players
// Call Stop() to gracefully shutdown the scheduler
func (t *SNSEarlyQuitMessageTrigger) StartLockoutExpiryScheduler(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(30 * time.Second) // Check every 30 seconds
		defer ticker.Stop()

		for {
			select {
			case <-t.stopCh:
				t.logger.Info("Penalty expiry scheduler stopped")
				return
			case <-ticker.C:
				t.checkAndNotifyExpiredPenalties(ctx)
			}
		}
	}()

	t.logger.Info("Started early quit penalty expiry scheduler")
}

// Stop gracefully shuts down the penalty expiry scheduler
func (t *SNSEarlyQuitMessageTrigger) Stop() {
	t.Lock()
	defer t.Unlock()

	if !t.stopped {
		t.stopped = true
		close(t.stopCh)
	}
}

// checkAndNotifyExpiredPenalties checks all connected players' penalties and sends expiry notifications
// Only notifies players who are currently connected
func (t *SNSEarlyQuitMessageTrigger) checkAndNotifyExpiredPenalties(ctx context.Context) {
	// Check all connected EVR sessions
	t.pipeline.sessionRegistry.Range(func(session Session) bool {
		if session.Format() != SessionFormatEVR {
			return true
		}

		_, ok := session.(*sessionWS)
		if !ok {
			return true
		}

		// Get player's early quit config from storage
		userID := session.UserID().String()
		eqConfig := NewEarlyQuitConfig()
		if err := StorableRead(ctx, t.nk, userID, eqConfig, true); err != nil {
			t.logger.Debug("Failed to load early quit config",
				zap.String("user_id", userID),
				zap.Error(err))
			return true
		}

		// Check if penalty has expired
		penaltyLevel := eqConfig.EarlyQuitPenaltyLevel
		if penaltyLevel <= 0 {
			return true // No penalty
		}

		// Get lockout duration for current penalty level
		lockoutDuration := GetLockoutDurationSeconds(int(penaltyLevel))

		if lockoutDuration == 0 {
			return true // No lockout for penalty level 0
		}

		// Calculate time since last early quit
		now := time.Now()
		timeSinceLastQuit := now.Sub(eqConfig.LastEarlyQuitTime).Seconds()

		// If enough time has passed, check if we already notified
		if timeSinceLastQuit >= float64(lockoutDuration) {
			// Skip if we already sent notification for this penalty
			if !eqConfig.LastExpiryNotificationSent.IsZero() && eqConfig.LastExpiryNotificationSent.After(eqConfig.LastEarlyQuitTime) {
				return true // Already notified for this penalty
			}

			// Send expiry notification
			if err := t.SendPenaltyExpiredNotification(ctx, userID); err != nil {
				t.logger.Warn("Failed to send penalty expired notification",
					zap.String("user_id", userID),
					zap.Error(err))
			} else {
				eqConfig.LastExpiryNotificationSent = now
				if err := StorableWrite(ctx, t.nk, userID, eqConfig); err != nil {
					t.logger.Warn("Failed to save expiry notification timestamp",
						zap.String("user_id", userID),
						zap.Error(err))
				}
			}
		}

		return true
	})
}
