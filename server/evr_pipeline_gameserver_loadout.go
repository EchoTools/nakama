package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/echotools/nevr-common/v4/gen/go/rtapi"
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

	// Convert the hash-based items to CosmeticLoadout fields
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

	// Update profile with new loadout
	profile.LoadoutCosmetics.Loadout = loadout

	// Update jersey number if present
	if payload.Number >= 0 {
		profile.LoadoutCosmetics.JerseyNumber = int64(payload.Number)
		logger.Info("Updated jersey number", zap.Int("number", payload.Number))
	}

	// Save the updated profile
	if err := EVRProfileUpdate(ctx, p.nk, userID, profile); err != nil {
		return fmt.Errorf("failed to store EVR profile: %w", err)
	}

	logger.Info("Successfully saved loadout update",
		zap.String("user_id", userID),
		zap.String("evr_id", request.EvrID.String()),
		zap.Any("loadout", loadout))

	return nil
}

// gameServerSaveLoadoutProtobuf handles loadout save requests via protobuf from the game server.
// This is the new protobuf-based handler that replaces the legacy EVR binary message path.
func (p *EvrPipeline) gameServerSaveLoadoutProtobuf(logger *zap.Logger, session *sessionWS, in *rtapi.Envelope) error {
	msg := in.GetGameServerSaveLoadout()
	if msg == nil {
		return fmt.Errorf("invalid GameServerSaveLoadout message")
	}

	logger = logger.With(
		zap.String("lobby_session_id", msg.LobbySessionId),
		zap.String("entrant_id", msg.EntrantId),
		zap.Int32("loadout_slot", msg.LoadoutSlot),
		zap.Int32("jersey_number", msg.JerseyNumber),
	)

	logger.Info("Processing protobuf save loadout request")

	ctx := session.Context()

	// Look up the user by their entrant_id (account ID as string)
	// The entrant_id from the game server is the account ID
	userID, err := p.lookupUserByEntrantID(ctx, msg.EntrantId)
	if err != nil {
		return fmt.Errorf("failed to get user by entrant ID: %w", err)
	}

	if userID == "" {
		logger.Warn("No user found for entrant ID", zap.String("entrant_id", msg.EntrantId))
		return nil
	}

	// Load the user's profile
	profile, err := EVRProfileLoad(ctx, p.nk, userID)
	if err != nil {
		return fmt.Errorf("failed to load EVR profile: %w", err)
	}

	// Convert the protobuf loadout instances to CosmeticLoadout fields
	loadout := profile.LoadoutCosmetics.Loadout

	// Process each loadout instance
	for _, instance := range msg.LoadoutInstances {
		for _, item := range instance.Items {
			// Convert fixed64 symbol IDs to evr.Symbol
			slotSymbol := evr.Symbol(item.SlotType)
			equippedSymbol := evr.Symbol(item.EquippedItem)

			// Convert symbols to their string names
			slotName := slotSymbol.String()
			equippedName := equippedSymbol.String()

			logger.Debug("Processing loadout item",
				zap.Uint64("slot_type", item.SlotType),
				zap.String("slot_name", slotName),
				zap.Uint64("equipped_item", item.EquippedItem),
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

	// Update profile with new loadout
	profile.LoadoutCosmetics.Loadout = loadout

	// Update jersey number if present
	if msg.JerseyNumber >= 0 {
		profile.LoadoutCosmetics.JerseyNumber = int64(msg.JerseyNumber)
		logger.Info("Updated jersey number", zap.Int32("number", msg.JerseyNumber))
	}

	// Save the updated profile
	if err := EVRProfileUpdate(ctx, p.nk, userID, profile); err != nil {
		return fmt.Errorf("failed to store EVR profile: %w", err)
	}

	logger.Info("Successfully saved loadout update via protobuf",
		zap.String("user_id", userID),
		zap.Any("loadout", loadout))

	return nil
}

// lookupUserByEntrantID looks up a Nakama user ID by the entrant's account ID
func (p *EvrPipeline) lookupUserByEntrantID(ctx context.Context, entrantID string) (string, error) {
	// The entrant ID is the account ID (uint64 as string)
	// We need to find the user that has this account linked

	// First, try to find by device ID (EvrID format)
	// If entrantID is numeric, it might be a platform account ID
	if _, err := strconv.ParseUint(entrantID, 10, 64); err == nil {
		// It's a numeric account ID - search by linked device
		// For now, try looking up by EvrID pattern
		// This might need adjustment based on how IDs are actually stored
	}

	// Try device ID lookup with common prefixes
	prefixes := []string{"STM-", "OVR-", "DMO-", ""}
	for _, prefix := range prefixes {
		deviceID := prefix + entrantID
		userID, err := GetUserIDByDeviceID(ctx, p.db, deviceID)
		if err == nil && userID != "" {
			return userID, nil
		}
	}

	return "", fmt.Errorf("user not found for entrant ID: %s", entrantID)
}
