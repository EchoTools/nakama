package server

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

const (
	SpamActionThreshold = 3
	SpamActionWindow    = 30 * time.Second
	GhostSuspendDuration = 1 * time.Hour
)

// SpamActionType distinguishes ghost vs mute spam.
type SpamActionType string

const (
	SpamActionGhost SpamActionType = "ghost"
	SpamActionMute  SpamActionType = "mute"
)

type spamAction struct {
	timestamp  time.Time
	actionType SpamActionType
}

// SpamTracker detects when an enforcer repeatedly ghosts or mutes the same
// player within a short window and takes automated action.
type SpamTracker struct {
	sync.Mutex
	actions map[string][]spamAction // key: "actionType:operatorID:targetEvrID"
	logger  *zap.Logger
	nk      runtime.NakamaModule
	done    chan struct{}
}

func NewSpamTracker(ctx context.Context, logger *zap.Logger, nk runtime.NakamaModule) *SpamTracker {
	tracker := &SpamTracker{
		actions: make(map[string][]spamAction),
		logger:  logger,
		nk:      nk,
		done:    make(chan struct{}),
	}

	go tracker.cleanupLoop()

	return tracker
}

func (t *SpamTracker) Stop() {
	close(t.done)
}

func (t *SpamTracker) cleanupLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			t.cleanup()
		case <-t.done:
			return
		}
	}
}

func (t *SpamTracker) cleanup() {
	t.Lock()
	defer t.Unlock()

	now := time.Now()
	for key, actions := range t.actions {
		filtered := actions[:0]
		for _, a := range actions {
			if now.Sub(a.timestamp) < SpamActionWindow {
				filtered = append(filtered, a)
			}
		}
		if len(filtered) == 0 {
			delete(t.actions, key)
		} else {
			t.actions[key] = filtered
		}
	}
}

// TrackAction records a ghost or mute action and returns true if the
// threshold has been reached for the given operator+target+actionType.
func (t *SpamTracker) TrackAction(operatorID string, targetPlayer evr.EvrId, actionType SpamActionType) bool {
	t.Lock()
	defer t.Unlock()

	key := fmt.Sprintf("%s:%s:%s", actionType, operatorID, targetPlayer.String())
	now := time.Now()

	t.actions[key] = append(t.actions[key], spamAction{
		timestamp:  now,
		actionType: actionType,
	})

	// Prune old entries for this key.
	recent := t.actions[key][:0]
	for _, a := range t.actions[key] {
		if now.Sub(a.timestamp) < SpamActionWindow {
			recent = append(recent, a)
		}
	}
	t.actions[key] = recent

	if len(recent) >= SpamActionThreshold {
		t.logger.Info("Spam threshold reached",
			zap.String("action_type", string(actionType)),
			zap.String("operator_id", operatorID),
			zap.String("target_player", targetPlayer.String()),
			zap.Int("count", len(recent)))
		delete(t.actions, key)
		return true
	}

	return false
}

// FindUserIDByEvrID resolves an EvrId to a Nakama user ID via the Profiles storage index.
func (t *SpamTracker) FindUserIDByEvrID(ctx context.Context, evrID evr.EvrId) (string, error) {
	storageObjects, _, err := t.nk.StorageIndexList(ctx, "", "Profiles", fmt.Sprintf("+value.server.xplatformid:%s", Query.EscapeIndexValue(evrID.String())), 1, nil, "")
	if err != nil || storageObjects == nil || len(storageObjects.GetObjects()) == 0 {
		return "", fmt.Errorf("failed to find user by EVR ID %s: %w", evrID.String(), err)
	}
	return storageObjects.GetObjects()[0].GetUserId(), nil
}

// BuildMatchContext gathers the operator's current match ID and the other
// players in that match, for inclusion in enforcement notes.
func (t *SpamTracker) BuildMatchContext(ctx context.Context, operatorEvrID evr.EvrId) (matchID string, otherPlayers []string) {
	matchIDs, err := MatchIDsByEvrID(ctx, t.nk, operatorEvrID)
	if err != nil || len(matchIDs) == 0 {
		return "", nil
	}

	mid := matchIDs[0]
	matchID = mid.String()

	label, err := MatchLabelByID(ctx, t.nk, mid)
	if err != nil || label == nil {
		return matchID, nil
	}

	for _, player := range label.Players {
		otherPlayers = append(otherPlayers, fmt.Sprintf("%s (%s)", player.DisplayName, player.EvrID.Token()))
	}
	return matchID, otherPlayers
}

// FormatNotes creates the enforcement/audit note with match context.
func FormatSpamNote(actionType SpamActionType, targetEvrID evr.EvrId, matchID string, otherPlayers []string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Auto-%s spam: %s targeted %d times in %v",
		actionType, targetEvrID.Token(), SpamActionThreshold, SpamActionWindow))

	if matchID != "" {
		sb.WriteString(fmt.Sprintf("\nMatch: %s", matchID))
	}
	if len(otherPlayers) > 0 {
		sb.WriteString(fmt.Sprintf("\nOther players in match: %s", strings.Join(otherPlayers, ", ")))
	}
	return sb.String()
}
