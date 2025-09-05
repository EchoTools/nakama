package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server"
)

const (
	StreamModeLobbySessionTelemetry uint8 = 0x25
)

// TelemetrySubscription represents a session's subscription to lobby telemetry
type TelemetrySubscription struct {
	SessionID uuid.UUID `json:"session_id"`
	UserID    uuid.UUID `json:"user_id"`
	LobbyID   uuid.UUID `json:"lobby_id"`
	Active    bool      `json:"active"`
}

// LobbyTelemetryManager manages telemetry subscriptions and streaming
type LobbyTelemetryManager struct {
	mu            sync.RWMutex
	subscriptions map[uuid.UUID]map[uuid.UUID]*TelemetrySubscription // lobbyID -> sessionID -> subscription
	nk            runtime.NakamaModule
	logger        runtime.Logger
	eventJournal  *EventJournal
}

// NewLobbyTelemetryManager creates a new LobbyTelemetryManager
func NewLobbyTelemetryManager(nk runtime.NakamaModule, logger runtime.Logger, eventJournal *EventJournal) *LobbyTelemetryManager {
	return &LobbyTelemetryManager{
		subscriptions: make(map[uuid.UUID]map[uuid.UUID]*TelemetrySubscription),
		nk:            nk,
		logger:        logger,
		eventJournal:  eventJournal,
	}
}

// Subscribe adds a session to a lobby's telemetry stream
func (ltm *LobbyTelemetryManager) Subscribe(ctx context.Context, sessionID, userID, lobbyID uuid.UUID) error {
	ltm.mu.Lock()
	defer ltm.mu.Unlock()

	// Initialize lobby subscriptions map if not exists
	if ltm.subscriptions[lobbyID] == nil {
		ltm.subscriptions[lobbyID] = make(map[uuid.UUID]*TelemetrySubscription)
	}

	// Create subscription
	subscription := &TelemetrySubscription{
		SessionID: sessionID,
		UserID:    userID,
		LobbyID:   lobbyID,
		Active:    true,
	}

	ltm.subscriptions[lobbyID][sessionID] = subscription

	// Join the session to the lobby telemetry stream
	stream := server.PresenceStream{
		Mode:    StreamModeLobbySessionTelemetry,
		Subject: lobbyID,
		Label:   "telemetry",
	}

	// Get the session status for the presence
	status, err := json.Marshal(subscription)
	if err != nil {
		return fmt.Errorf("failed to marshal subscription status: %w", err)
	}

	// Add session to the telemetry stream
	success, err := ltm.nk.StreamUserJoin(stream.Mode, stream.Subject.String(), "", stream.Label, userID.String(), sessionID.String(), false, false, string(status))
	if err != nil || !success {
		delete(ltm.subscriptions[lobbyID], sessionID)
		return fmt.Errorf("failed to join telemetry stream: %w", err)
	}

	ltm.logger.Info("Session subscribed to lobby telemetry: session_id=%s, user_id=%s, lobby_id=%s", sessionID.String(), userID.String(), lobbyID.String())

	return nil
}

// Unsubscribe removes a session from a lobby's telemetry stream
func (ltm *LobbyTelemetryManager) Unsubscribe(ctx context.Context, sessionID, userID, lobbyID uuid.UUID) error {
	ltm.mu.Lock()
	defer ltm.mu.Unlock()

	// Remove subscription
	if ltm.subscriptions[lobbyID] != nil {
		delete(ltm.subscriptions[lobbyID], sessionID)

		// Clean up empty lobby subscription map
		if len(ltm.subscriptions[lobbyID]) == 0 {
			delete(ltm.subscriptions, lobbyID)
		}
	}

	// Leave the telemetry stream
	stream := server.PresenceStream{
		Mode:    StreamModeLobbySessionTelemetry,
		Subject: lobbyID,
		Label:   "telemetry",
	}

	if err := ltm.nk.StreamUserLeave(stream.Mode, stream.Subject.String(), "", stream.Label, userID.String(), sessionID.String()); err != nil {
		ltm.logger.Warn("Failed to leave telemetry stream: session_id=%s, error=%v", sessionID.String(), err)
		// Don't return error as subscription is already removed from memory
	}

	ltm.logger.Info("Session unsubscribed from lobby telemetry: session_id=%s, user_id=%s, lobby_id=%s", sessionID.String(), userID.String(), lobbyID.String())

	return nil
}

// BroadcastTelemetry sends telemetry data to all subscribed sessions for a lobby
func (ltm *LobbyTelemetryManager) BroadcastTelemetry(ctx context.Context, lobbyID uuid.UUID, telemetryData map[string]interface{}) error {
	ltm.mu.RLock()
	subscriptions := ltm.subscriptions[lobbyID]
	ltm.mu.RUnlock()

	if len(subscriptions) == 0 {
		// No subscribers, but still journal the event
		if ltm.eventJournal != nil {
			return ltm.eventJournal.JournalTelemetry(ctx, "", "", lobbyID.String(), telemetryData)
		}
		return nil
	}

	// Create the telemetry message
	telemetryMessage := map[string]interface{}{
		"type":      "lobby_telemetry",
		"lobby_id":  lobbyID.String(),
		"data":      telemetryData,
		"timestamp": time.Now().Format(time.RFC3339),
	}

	messageBytes, err := json.Marshal(telemetryMessage)
	if err != nil {
		return fmt.Errorf("failed to marshal telemetry message: %w", err)
	}

	// Send to each subscribed session's individual stream
	for sessionID, subscription := range subscriptions {
		if !subscription.Active {
			continue
		}

		// Create individual session stream for telemetry delivery
		sessionStream := server.PresenceStream{
			Mode:    StreamModeLobbySessionTelemetry,
			Subject: sessionID, // Use sessionID as subject for individual delivery
			Label:   fmt.Sprintf("lobby_%s", lobbyID.String()),
		}

		// Send the telemetry data to the session's stream
		if err := ltm.nk.StreamUserUpdate(sessionStream.Mode, sessionStream.Subject.String(), "", sessionStream.Label,
			subscription.UserID.String(), subscription.SessionID.String(), false, false, string(messageBytes)); err != nil {
			ltm.logger.Warn("Failed to send telemetry to session: session_id=%s, lobby_id=%s, error=%v", sessionID.String(), lobbyID.String(), err)
			continue
		}

		// Journal the telemetry for this specific session
		if ltm.eventJournal != nil {
			ltm.eventJournal.JournalTelemetry(ctx, subscription.UserID.String(), subscription.SessionID.String(), lobbyID.String(), telemetryData)
		}
	}

	ltm.logger.Debug("Telemetry broadcasted to subscribers: lobby_id=%s, subscriber_count=%d", lobbyID.String(), len(subscriptions))

	return nil
}

// GetSubscriptions returns active subscriptions for a lobby
func (ltm *LobbyTelemetryManager) GetSubscriptions(lobbyID uuid.UUID) []*TelemetrySubscription {
	ltm.mu.RLock()
	defer ltm.mu.RUnlock()

	subscriptions := ltm.subscriptions[lobbyID]
	if subscriptions == nil {
		return nil
	}

	result := make([]*TelemetrySubscription, 0, len(subscriptions))
	for _, subscription := range subscriptions {
		if subscription.Active {
			result = append(result, subscription)
		}
	}

	return result
}

// CleanupSession removes all subscriptions for a session (called when session disconnects)
func (ltm *LobbyTelemetryManager) CleanupSession(ctx context.Context, sessionID, userID uuid.UUID) {
	ltm.mu.Lock()
	defer ltm.mu.Unlock()

	// Find and remove all subscriptions for this session
	for lobbyID, subscriptions := range ltm.subscriptions {
		if subscription, exists := subscriptions[sessionID]; exists {
			delete(subscriptions, sessionID)

			// Clean up empty lobby subscription map
			if len(subscriptions) == 0 {
				delete(ltm.subscriptions, lobbyID)
			}

			// Leave the telemetry stream
			stream := server.PresenceStream{
				Mode:    StreamModeLobbySessionTelemetry,
				Subject: lobbyID,
				Label:   "telemetry",
			}

			if err := ltm.nk.StreamUserLeave(stream.Mode, stream.Subject.String(), "", stream.Label, userID.String(), sessionID.String()); err != nil {
				ltm.logger.Warn("Failed to leave telemetry stream during cleanup: session_id=%s, lobby_id=%s, error=%v", sessionID.String(), subscription.LobbyID.String(), err)
			}
		}
	}

	ltm.logger.Debug("Cleaned up telemetry subscriptions for session: session_id=%s, user_id=%s", sessionID.String(), userID.String())
}
