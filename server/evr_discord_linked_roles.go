package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

// DiscordLinkedRolesMetadata represents the metadata Discord uses to determine role eligibility
type DiscordLinkedRolesMetadata struct {
	PlatformName     string `json:"platform_name"`
	PlatformUsername string `json:"platform_username,omitempty"`
	HasHeadset       bool   `json:"has_headset"`
	HasPlayedEcho    bool   `json:"has_played_echo"`
}

// DiscordLinkedRolesHandler handles Discord Linked Roles metadata requests
type DiscordLinkedRolesHandler struct {
	ctx    context.Context
	logger runtime.Logger
	db     *sql.DB
	nk     runtime.NakamaModule
}

// NewDiscordLinkedRolesHandler creates a new Discord Linked Roles handler
func NewDiscordLinkedRolesHandler(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) *DiscordLinkedRolesHandler {
	return &DiscordLinkedRolesHandler{
		ctx:    ctx,
		logger: logger,
		db:     db,
		nk:     nk,
	}
}

// ServeHTTP handles the Discord Linked Roles metadata request
func (h *DiscordLinkedRolesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Set CORS headers for all requests
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")

	// Handle OPTIONS preflight request
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Only allow GET requests
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get the access token from the Authorization header
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		http.Error(w, "Missing Authorization header", http.StatusUnauthorized)
		return
	}

	// Extract Bearer token
	const bearerPrefix = "Bearer "
	if !strings.HasPrefix(authHeader, bearerPrefix) {
		http.Error(w, "Invalid Authorization header format", http.StatusUnauthorized)
		return
	}

	accessToken := strings.TrimPrefix(authHeader, bearerPrefix)
	if accessToken == "" {
		http.Error(w, "Missing access token", http.StatusUnauthorized)
		return
	}

	// Get Discord user info from the access token
	discordUserID, err := h.getDiscordUserIDFromToken(accessToken)
	if err != nil {
		h.logger.Error("Failed to get Discord user ID from token", zap.Error(err))
		http.Error(w, "Invalid access token", http.StatusUnauthorized)
		return
	}

	// Find the corresponding Nakama user ID
	userID, err := h.findUserIDByDiscordID(discordUserID)
	if err != nil {
		h.logger.Error("Failed to find user by Discord ID", zap.String("discord_id", discordUserID), zap.Error(err))
		// Return empty metadata for users not found in our system
		metadata := DiscordLinkedRolesMetadata{
			PlatformName:  "Echo VR",
			HasHeadset:    false,
			HasPlayedEcho: false,
		}
		h.sendJSONResponse(w, metadata)
		return
	}

	// Get user metadata
	metadata, err := h.getUserMetadata(userID)
	if err != nil {
		h.logger.Error("Failed to get user metadata", zap.String("user_id", userID), zap.Error(err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Send the metadata response
	h.sendJSONResponse(w, metadata)
}

// getDiscordUserIDFromToken retrieves the Discord user ID from an access token
func (h *DiscordLinkedRolesHandler) getDiscordUserIDFromToken(accessToken string) (string, error) {
	// Make a request to Discord's /users/@me endpoint
	req, err := http.NewRequest("GET", "https://discord.com/api/users/@me", nil)
	if err != nil {
		return "", fmt.Errorf("failed to create Discord API request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", "EchoVR-Nakama-Bot/1.0")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to make Discord API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Discord API returned status %d", resp.StatusCode)
	}

	var user struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return "", fmt.Errorf("failed to decode Discord user response: %w", err)
	}

	return user.ID, nil
}

// findUserIDByDiscordID finds a Nakama user ID by Discord ID
func (h *DiscordLinkedRolesHandler) findUserIDByDiscordID(discordID string) (string, error) {
	// Query the database to find the user by Discord ID
	// Discord ID is typically stored in custom_id or as part of user metadata
	query := `
		SELECT id FROM users 
		WHERE custom_id = $1 
		OR metadata LIKE '%"discord_id":"' || $1 || '"%'
		LIMIT 1
	`

	var userID string
	err := h.db.QueryRow(query, discordID).Scan(&userID)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("user not found for Discord ID: %s", discordID)
		}
		return "", fmt.Errorf("database error: %w", err)
	}

	return userID, nil
}

// getUserMetadata gets the metadata for a user to determine role eligibility
func (h *DiscordLinkedRolesHandler) getUserMetadata(userID string) (DiscordLinkedRolesMetadata, error) {
	metadata := DiscordLinkedRolesMetadata{
		PlatformName: "Echo VR",
	}

	// Check if the user has a headset linked (using existing IsLinked function)
	profile, err := EVRProfileLoad(h.ctx, h.nk, userID)
	if err != nil {
		// If profile doesn't exist, user hasn't linked a headset
		h.logger.Debug("Failed to load EVR profile for user", zap.String("user_id", userID), zap.Error(err))
		metadata.HasHeadset = false
	} else {
		metadata.HasHeadset = profile.IsLinked()
		// Use the user's display name or username as platform username
		if displayName := profile.DisplayName(); displayName != "" {
			metadata.PlatformUsername = displayName
		} else if username := profile.Username(); username != "" {
			metadata.PlatformUsername = username
		}
	}

	// Check if the user has played Echo (using existing HasLoggedIntoEcho function)
	hasPlayed, err := HasLoggedIntoEcho(h.ctx, h.nk, userID)
	if err != nil {
		h.logger.Debug("Failed to check if user has played Echo", zap.String("user_id", userID), zap.Error(err))
		metadata.HasPlayedEcho = false
	} else {
		metadata.HasPlayedEcho = hasPlayed
	}

	return metadata, nil
}

// sendJSONResponse sends a JSON response
func (h *DiscordLinkedRolesHandler) sendJSONResponse(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(data); err != nil {
		h.logger.Error("Failed to encode JSON response", zap.Error(err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// RegisterDiscordLinkedRolesHandler registers the Discord Linked Roles HTTP handler
func RegisterDiscordLinkedRolesHandler(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) error {
	handler := NewDiscordLinkedRolesHandler(ctx, logger, db, nk)

	// Register the handler for the Discord Linked Roles endpoint
	return initializer.RegisterHttp("/discord/linked-roles/metadata", handler.ServeHTTP, http.MethodGet, http.MethodOptions)
}
