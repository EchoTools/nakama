package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

// gameServerSaveLoadoutRequest handles loadout save requests from the game server.
// This is triggered when a player updates their loadout at the character customization screen.
// The game server receives an internal broadcaster message (SR15NetSaveLoadoutRequest) and
// forwards it to nakama via the TCP broadcaster WebSocket connection.
func (p *EvrPipeline) gameServerSaveLoadoutRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, request *evr.GameServerSaveLoadoutRequest) error {
	logger = logger.With(
		zap.String("evr_id", request.EvrID.String()),
		zap.Int32("loadout_number", request.LoadoutNumber),
		zap.String("entrant_session_id", request.EntrantSessionID.String()),
	)

	// Parse the loadout data
	var loadoutPayload evr.SaveLoadoutPayload
	if err := json.Unmarshal(request.Loadout, &loadoutPayload); err != nil {
		logger.Warn("Failed to unmarshal loadout payload", zap.Error(err), zap.String("raw", string(request.Loadout)))
		// Try to continue with raw JSON if structured parse fails
	}

	logger.Info("Processing save loadout request",
		zap.Any("loadout", loadoutPayload))

	// Look up the user by their EvrID (used as device ID)
	userID, err := GetUserIDByDeviceID(ctx, p.db, request.EvrID.String())
	if err != nil {
		return fmt.Errorf("failed to get user ID by EvrID: %w", err)
	}

	if userID == "" {
		logger.Warn("No user found for EvrID", zap.String("evr_id", request.EvrID.String()))
		return nil
	}

	// Load the user's profile
	profile, err := EVRProfileLoad(ctx, p.nk, userID)
	if err != nil {
		return fmt.Errorf("failed to load EVR profile: %w", err)
	}

	// Update the cosmetic loadout in the profile
	if loadoutPayload.Slots != (evr.CosmeticLoadout{}) {
		profile.LoadoutCosmetics.Loadout = loadoutPayload.Slots // Updated to match profile structure
		logger.Debug("Updated loadout slots from structured payload")
	} else if len(request.Loadout) > 0 {
		// Try to unmarshal directly into the slots if the structured parse failed
		var slots evr.CosmeticLoadout
		if err := json.Unmarshal(request.Loadout, &slots); err == nil {
			profile.LoadoutCosmetics.Loadout = slots // Updated to match profile structure
			logger.Debug("Updated loadout slots from raw JSON")
		} else {
			logger.Warn("Failed to parse loadout data in any format", zap.Error(err))
		}
	}

	// Save the updated profile
	if err := EVRProfileUpdate(ctx, p.nk, userID, profile); err != nil {
		return fmt.Errorf("failed to store EVR profile: %w", err)
	}

	logger.Info("Successfully saved loadout update",
		zap.String("user_id", userID),
		zap.String("evr_id", request.EvrID.String()))

	return nil
}
