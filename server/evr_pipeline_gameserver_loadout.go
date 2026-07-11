package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

// GameServerLoadoutPayload is the JSON format sent from the game server
// Format: {"slot":0,"number":8,"loadout_instances":[{"instance_name":"0x...","items":{"0x...":"0x..."}}]}
type GameServerLoadoutPayload struct {
	Slot             int                         `json:"slot"`
	Number           int                         `json:"number"` // Jersey number
	LoadoutInstances []GameServerLoadoutInstance `json:"loadout_instances"`
}

type GameServerLoadoutInstance struct {
	InstanceName string            `json:"instance_name"`
	Items        map[string]string `json:"items"` // slot_hash -> equipped_hash (both as hex strings)
}

// sanitizeGameServerLoadout strips any cosmetic the player does not own from a loadout
// parsed from a GameServerSaveLoadoutRequest, mirroring EquipAndSanitize's protection
// for the RemoteLogSet equip path (COSMETIC-1). It is a separate entry point because
// this handler builds a full loadout from a set of slot->item pairs rather than a single
// category/name equip.
func sanitizeGameServerLoadout(loadout evr.CosmeticLoadout, evrProfile *EVRProfile) (evr.CosmeticLoadout, error) {
	owned, err := profileOwnedCosmetics(evrProfile)
	if err != nil {
		return loadout, err
	}
	return sanitizeLoadout(loadout, owned), nil
}

// gameServerSaveLoadoutRequest handles loadout save requests from the game server.
// This is triggered when a player updates their loadout at the character customization screen.
// The game server receives an internal broadcaster message (SR15NetSaveLoadoutRequest) and
// forwards it to nakama via the TCP broadcaster WebSocket connection.
func (p *EvrPipeline) gameServerSaveLoadoutRequest(ctx context.Context, logger *zap.Logger, request *evr.GameServerSaveLoadoutRequest) error {
	logger = logger.With(
		zap.String("evr_id", request.EvrID.String()),
		zap.Int32("loadout_number", request.LoadoutNumber),
		zap.String("entrant_session_id", request.EntrantSessionID.String()),
	)

	logger.Info("Processing save loadout request", zap.String("raw_json", string(request.Loadout)))

	// Parse the new game server loadout format
	var payload GameServerLoadoutPayload
	if err := json.Unmarshal(request.Loadout, &payload); err != nil {
		logger.Warn("Failed to unmarshal loadout payload", zap.Error(err), zap.String("raw", string(request.Loadout)))
		return nil
	}

	logger.Info("Parsed loadout payload",
		zap.Int("slot", payload.Slot),
		zap.Int("jersey_number", payload.Number),
		zap.Int("instance_count", len(payload.LoadoutInstances)))

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

	// applyPayload maps the hash-based slot/item pairs onto profile.LoadoutCosmetics
	// and rejects any cosmetic the player doesn't own (COSMETIC-1)
	applyPayload := func() error {
		loadout := profile.LoadoutCosmetics.Loadout

		// Process each loadout instance
		for _, instance := range payload.LoadoutInstances {
			for slotHex, equippedHex := range instance.Items {
				slotSymbol := evr.ToSymbol(slotHex)
				if slotSymbol == 0 {
					logger.Warn("Failed to parse slot hash", zap.String("slot", slotHex))
					continue
				}
				equippedSymbol := evr.ToSymbol(equippedHex)
				if equippedSymbol == 0 {
					logger.Warn("Failed to parse equipped hash", zap.String("equipped", equippedHex))
					continue
				}

				// Convert symbols to their string names
				slotName := slotSymbol.String()
				equippedName := equippedSymbol.String()

				logger.Debug("Processing loadout item",
					zap.String("slot_hash", slotHex),
					zap.String("slot_name", slotName),
					zap.String("equipped_hash", equippedHex),
					zap.String("equipped_name", equippedName))

				// Map slot names to CosmeticLoadout fields
				switch slotName {
				case "banner":
					loadout.Banner = equippedName
				case "booster":
					loadout.Booster = equippedName
				case "bracer":
					loadout.Bracer = equippedName
				case "chassis":
					loadout.Chassis = equippedName
				case "decal":
					loadout.Decal = equippedName
				case "decal_body":
					loadout.DecalBody = equippedName
				case "decalborder":
					loadout.DecalBorder = equippedName
				case "decalback":
					loadout.DecalBack = equippedName
				case "emissive":
					loadout.Emissive = equippedName
				case "emote":
					loadout.Emote = equippedName
				case "goal_fx":
					loadout.GoalFX = equippedName
				case "medal":
					loadout.Medal = equippedName
				case "pattern":
					loadout.Pattern = equippedName
				case "pattern_body":
					loadout.PatternBody = equippedName
				case "pip":
					loadout.PIP = equippedName
				case "secondemote":
					loadout.SecondEmote = equippedName
				case "tag":
					loadout.Tag = equippedName
				case "tint":
					loadout.Tint = equippedName
				case "tint_alignment_a":
					loadout.TintAlignmentA = equippedName
				case "tint_alignment_b":
					loadout.TintAlignmentB = equippedName
				case "tint_body":
					loadout.TintBody = equippedName
				case "title":
					loadout.Title = equippedName
				default:
					logger.Debug("Unknown slot type", zap.String("slot", slotName))
				}
			}
		}

		sanitized, err := sanitizeGameServerLoadout(loadout, profile)
		if err != nil {
			return fmt.Errorf("failed to compute owned cosmetics: %w", err)
		}
		profile.LoadoutCosmetics.Loadout = sanitized

		// Update jersey number if present
		if payload.Number >= 0 {
			profile.LoadoutCosmetics.JerseyNumber = int64(payload.Number)
		}

		return nil
	}

	if err := applyPayload(); err != nil {
		return err
	}

	if err := EVRProfileUpdateWithRetry(ctx, p.nk, userID, profile, applyPayload); err != nil {
		return fmt.Errorf("failed to store EVR profile: %w", err)
	}

	logger.Info("Successfully saved loadout update",
		zap.String("user_id", userID),
		zap.String("evr_id", request.EvrID.String()),
		zap.Any("loadout", profile.LoadoutCosmetics.Loadout))

	return nil
}
