package server

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

const (
	GhostSpamThreshold = 3
	GhostSpamWindow    = 30 * time.Second
)

type ghostAction struct {
	timestamp    time.Time
	operatorID   string
	targetPlayer evr.EvrId
}

type GhostSpamTracker struct {
	sync.RWMutex
	actions map[string][]ghostAction
	logger  *zap.Logger
	nk      runtime.NakamaModule
}

func NewGhostSpamTracker(logger *zap.Logger, nk runtime.NakamaModule) *GhostSpamTracker {
	tracker := &GhostSpamTracker{
		actions: make(map[string][]ghostAction),
		logger:  logger,
		nk:      nk,
	}

	go tracker.cleanupLoop()

	return tracker
}

func (t *GhostSpamTracker) cleanupLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		t.cleanup()
	}
}

func (t *GhostSpamTracker) cleanup() {
	t.Lock()
	defer t.Unlock()

	now := time.Now()
	for key, actions := range t.actions {
		filtered := make([]ghostAction, 0)
		for _, action := range actions {
			if now.Sub(action.timestamp) < GhostSpamWindow {
				filtered = append(filtered, action)
			}
		}
		if len(filtered) == 0 {
			delete(t.actions, key)
		} else {
			t.actions[key] = filtered
		}
	}
}

func (t *GhostSpamTracker) TrackGhostAction(ctx context.Context, operatorID string, targetPlayer evr.EvrId, isGlobalOperator bool) (shouldKick bool, kickReason string) {
	if !isGlobalOperator {
		return false, ""
	}

	t.Lock()
	defer t.Unlock()

	key := fmt.Sprintf("%s:%s", operatorID, targetPlayer.String())

	now := time.Now()
	action := ghostAction{
		timestamp:    now,
		operatorID:   operatorID,
		targetPlayer: targetPlayer,
	}

	t.actions[key] = append(t.actions[key], action)

	recent := make([]ghostAction, 0)
	for _, a := range t.actions[key] {
		if now.Sub(a.timestamp) < GhostSpamWindow {
			recent = append(recent, a)
		}
	}
	t.actions[key] = recent

	if len(recent) >= GhostSpamThreshold {
		kickReason = fmt.Sprintf("Auto-kicked: Global operator ghosted player %d times in %v", GhostSpamThreshold, GhostSpamWindow)
		t.logger.Info("Ghost spam threshold reached",
			zap.String("operator_id", operatorID),
			zap.String("target_player", targetPlayer.String()),
			zap.Int("ghost_count", len(recent)))

		delete(t.actions, key)
		return true, kickReason
	}

	return false, ""
}

func (t *GhostSpamTracker) KickPlayer(ctx context.Context, targetUserID string, reason string) error {
	count, err := DisconnectUserID(ctx, t.nk, targetUserID, true, true, false)
	if err != nil {
		return fmt.Errorf("failed to disconnect user: %w", err)
	}

	t.logger.Info("Player kicked due to ghost spam",
		zap.String("target_user_id", targetUserID),
		zap.Int("sessions_disconnected", count),
		zap.String("reason", reason))

	return nil
}

func (t *GhostSpamTracker) FindUserIDByEvrID(ctx context.Context, evrID evr.EvrId) (string, error) {
	storageObjects, _, err := t.nk.StorageIndexList(ctx, "", "Profiles", fmt.Sprintf("+value.server.xplatformid:%s", evrID.String()), 1, nil, "")
	if err != nil || storageObjects == nil || len(storageObjects.GetObjects()) == 0 {
		return "", fmt.Errorf("failed to find user by EVR ID %s: %w", evrID.String(), err)
	}
	return storageObjects.GetObjects()[0].GetUserId(), nil
}
