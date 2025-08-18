package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

// TelemetryAPI handles telemetry-related HTTP endpoints
type TelemetryAPI struct {
	logger            runtime.Logger
	db                *sql.DB
	nk                runtime.NakamaModule
	telemetryManager  *LobbyTelemetryManager
	eventJournal      *EventJournal
	matchSummaryStore *MatchSummaryStore
}

// NewTelemetryAPI creates a new TelemetryAPI instance
func NewTelemetryAPI(logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, telemetryManager *LobbyTelemetryManager, eventJournal *EventJournal, matchSummaryStore *MatchSummaryStore) *TelemetryAPI {
	return &TelemetryAPI{
		logger:            logger,
		db:                db,
		nk:                nk,
		telemetryManager:  telemetryManager,
		eventJournal:      eventJournal,
		matchSummaryStore: matchSummaryStore,
	}
}

// TelemetrySubscribeRequest represents a subscription request
type TelemetrySubscribeRequest struct {
	LobbyID string `json:"lobby_id"`
}

// TelemetrySubscribeResponse represents a subscription response
type TelemetrySubscribeResponse struct {
	Success   bool   `json:"success"`
	Message   string `json:"message,omitempty"`
	LobbyID   string `json:"lobby_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// TelemetryUnsubscribeRequest represents an unsubscription request
type TelemetryUnsubscribeRequest struct {
	LobbyID string `json:"lobby_id"`
}

// TelemetryUnsubscribeResponse represents an unsubscription response
type TelemetryUnsubscribeResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// RegisterTelemetryEndpoints registers the telemetry API endpoints
func (api *TelemetryAPI) RegisterTelemetryEndpoints(initializer runtime.Initializer) error {
	// Register HTTP handlers for telemetry API
	if err := initializer.RegisterHttp("/telemetry/subscribe", http.HandlerFunc(api.handleTelemetrySubscribeHTTP), http.MethodPost); err != nil {
		return fmt.Errorf("failed to register telemetry subscribe endpoint: %w", err)
	}

	if err := initializer.RegisterHttp("/telemetry/unsubscribe", http.HandlerFunc(api.handleTelemetryUnsubscribeHTTP), http.MethodPost); err != nil {
		return fmt.Errorf("failed to register telemetry unsubscribe endpoint: %w", err)
	}

	if err := initializer.RegisterHttp("/telemetry/subscriptions", http.HandlerFunc(api.handleGetSubscriptionsHTTP), http.MethodGet); err != nil {
		return fmt.Errorf("failed to register telemetry subscriptions endpoint: %w", err)
	}

	return nil
}

// HTTP wrapper functions to match the expected signature
func (api *TelemetryAPI) handleTelemetrySubscribeHTTP(w http.ResponseWriter, r *http.Request) {
	api.handleTelemetrySubscribe(r.Context(), api.logger, api.db, api.nk, w, r)
}

func (api *TelemetryAPI) handleTelemetryUnsubscribeHTTP(w http.ResponseWriter, r *http.Request) {
	api.handleTelemetryUnsubscribe(r.Context(), api.logger, api.db, api.nk, w, r)
}

func (api *TelemetryAPI) handleGetSubscriptionsHTTP(w http.ResponseWriter, r *http.Request) {
	api.handleGetSubscriptions(r.Context(), api.logger, api.db, api.nk, w, r)
}

// handleTelemetrySubscribe handles POST /telemetry/subscribe
func (api *TelemetryAPI) handleTelemetrySubscribe(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, w http.ResponseWriter, r *http.Request) {
	// Get session information from context or headers
	sessionID, userID, err := api.getSessionInfo(r)
	if err != nil {
		api.writeErrorResponse(w, http.StatusUnauthorized, "Authentication required", err)
		return
	}

	// Parse request body
	var req TelemetrySubscribeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.writeErrorResponse(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	// Validate lobby ID
	lobbyID, err := uuid.FromString(req.LobbyID)
	if err != nil {
		api.writeErrorResponse(w, http.StatusBadRequest, "Invalid lobby ID", err)
		return
	}

	// Subscribe to telemetry
	if err := api.telemetryManager.Subscribe(ctx, sessionID, userID, lobbyID); err != nil {
		api.writeErrorResponse(w, http.StatusInternalServerError, "Failed to subscribe to telemetry", err)
		return
	}

	// Journal the subscription event
	if api.eventJournal != nil {
		subscriptionData := map[string]interface{}{
			"action":   "subscribe",
			"lobby_id": req.LobbyID,
		}
		api.eventJournal.JournalPlayerAction(ctx, userID.String(), sessionID.String(), "", "telemetry_subscription", subscriptionData)
	}

	// Send success response
	response := TelemetrySubscribeResponse{
		Success:   true,
		Message:   "Successfully subscribed to lobby telemetry",
		LobbyID:   req.LobbyID,
		SessionID: sessionID.String(),
	}

	api.writeJSONResponse(w, http.StatusOK, response)
}

// handleTelemetryUnsubscribe handles POST /telemetry/unsubscribe
func (api *TelemetryAPI) handleTelemetryUnsubscribe(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, w http.ResponseWriter, r *http.Request) {
	// Get session information
	sessionID, userID, err := api.getSessionInfo(r)
	if err != nil {
		api.writeErrorResponse(w, http.StatusUnauthorized, "Authentication required", err)
		return
	}

	// Parse request body
	var req TelemetryUnsubscribeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.writeErrorResponse(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	// Validate lobby ID
	lobbyID, err := uuid.FromString(req.LobbyID)
	if err != nil {
		api.writeErrorResponse(w, http.StatusBadRequest, "Invalid lobby ID", err)
		return
	}

	// Unsubscribe from telemetry
	if err := api.telemetryManager.Unsubscribe(ctx, sessionID, userID, lobbyID); err != nil {
		api.writeErrorResponse(w, http.StatusInternalServerError, "Failed to unsubscribe from telemetry", err)
		return
	}

	// Journal the unsubscription event
	if api.eventJournal != nil {
		subscriptionData := map[string]interface{}{
			"action":   "unsubscribe",
			"lobby_id": req.LobbyID,
		}
		api.eventJournal.JournalPlayerAction(ctx, userID.String(), sessionID.String(), "", "telemetry_subscription", subscriptionData)
	}

	// Send success response
	response := TelemetryUnsubscribeResponse{
		Success: true,
		Message: "Successfully unsubscribed from lobby telemetry",
	}

	api.writeJSONResponse(w, http.StatusOK, response)
}

// handleGetSubscriptions handles GET /telemetry/subscriptions
func (api *TelemetryAPI) handleGetSubscriptions(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, w http.ResponseWriter, r *http.Request) {
	// Get lobby ID from query parameters
	lobbyIDStr := r.URL.Query().Get("lobby_id")
	if lobbyIDStr == "" {
		api.writeErrorResponse(w, http.StatusBadRequest, "lobby_id parameter required", nil)
		return
	}

	lobbyID, err := uuid.FromString(lobbyIDStr)
	if err != nil {
		api.writeErrorResponse(w, http.StatusBadRequest, "Invalid lobby ID", err)
		return
	}

	// Get subscriptions
	subscriptions := api.telemetryManager.GetSubscriptions(lobbyID)

	// Send response
	response := map[string]interface{}{
		"lobby_id":      lobbyIDStr,
		"subscriptions": subscriptions,
		"count":         len(subscriptions),
	}

	api.writeJSONResponse(w, http.StatusOK, response)
}

// getSessionInfo extracts session and user information from request
func (api *TelemetryAPI) getSessionInfo(r *http.Request) (sessionID, userID uuid.UUID, err error) {
	// In a real implementation, this would extract session information from:
	// - Authorization header
	// - Session cookies
	// - JWT tokens
	// For this implementation, we'll use headers for simplicity

	sessionIDStr := r.Header.Get("X-Session-ID")
	userIDStr := r.Header.Get("X-User-ID")

	if sessionIDStr == "" || userIDStr == "" {
		return uuid.Nil, uuid.Nil, fmt.Errorf("missing session or user ID headers")
	}

	sessionID, err = uuid.FromString(sessionIDStr)
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("invalid session ID: %w", err)
	}

	userID, err = uuid.FromString(userIDStr)
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("invalid user ID: %w", err)
	}

	return sessionID, userID, nil
}

// writeJSONResponse writes a JSON response
func (api *TelemetryAPI) writeJSONResponse(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	if err := json.NewEncoder(w).Encode(data); err != nil {
		api.logger.Error("Failed to encode JSON response: %v", err)
	}
}

// writeErrorResponse writes an error response
func (api *TelemetryAPI) writeErrorResponse(w http.ResponseWriter, statusCode int, message string, err error) {
	errorResponse := map[string]interface{}{
		"success": false,
		"message": message,
	}

	if err != nil {
		api.logger.Error("%s: %v", message, err)
		errorResponse["error"] = err.Error()
	}

	api.writeJSONResponse(w, statusCode, errorResponse)
}
