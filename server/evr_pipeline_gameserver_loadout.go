package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/heroiclabs/nakama-common/runtime"
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

// gameServerLoadoutEquip is one slot assignment named by a
// GameServerSaveLoadoutRequest, already decoded from its symbol hashes.
type gameServerLoadoutEquip struct{ slot, equipped string }

// applyGameServerLoadout composes equips onto the CURRENT loadout of profile,
// strips any cosmetic the player does not own, and stores the result on profile.
// A jerseyNumber below zero leaves the stored jersey number untouched. It returns
// the loadout it stored.
//
// Every input other than equips is read from profile, which is what makes this
// safe to call more than once: on a retry after a version conflict, profile is a
// freshly read object, so only the slots this request actually named change and
// a concurrent writer's other slots survive. Deriving the base loadout from a
// profile captured BEFORE the conflict would instead stamp a stale full loadout
// back over that writer's work.
func applyGameServerLoadout(profile *EVRProfile, equips []gameServerLoadoutEquip, jerseyNumber int) (evr.CosmeticLoadout, error) {
	loadout := profile.LoadoutCosmetics.Loadout
	for _, e := range equips {
		setLoadoutSlot(&loadout, e.slot, e.equipped)
	}

	sanitized, err := sanitizeGameServerLoadout(loadout, profile)
	if err != nil {
		return evr.CosmeticLoadout{}, fmt.Errorf("failed to compute owned cosmetics: %w", err)
	}
	profile.LoadoutCosmetics.Loadout = sanitized

	// Update jersey number if present
	if jerseyNumber >= 0 {
		profile.LoadoutCosmetics.JerseyNumber = int64(jerseyNumber)
	}
	return sanitized, nil
}

// setLoadoutSlot assigns equippedName to the named slot of l, and reports whether
// slotName is a slot this server recognises. Splitting the slot-name switch out of
// the request handler is what lets the equips be replayed onto a freshly read
// profile when a write hits a version conflict.
func setLoadoutSlot(l *evr.CosmeticLoadout, slotName, equippedName string) bool {
	switch slotName {
	case "banner":
		l.Banner = equippedName
	case "booster":
		l.Booster = equippedName
	case "bracer":
		l.Bracer = equippedName
	case "chassis":
		l.Chassis = equippedName
	case "decal":
		l.Decal = equippedName
	case "decal_body":
		l.DecalBody = equippedName
	case "decalborder":
		l.DecalBorder = equippedName
	case "decalback":
		l.DecalBack = equippedName
	case "emissive":
		l.Emissive = equippedName
	case "emote":
		l.Emote = equippedName
	case "goal_fx":
		l.GoalFX = equippedName
	case "medal":
		l.Medal = equippedName
	case "pattern":
		l.Pattern = equippedName
	case "pattern_body":
		l.PatternBody = equippedName
	case "pip":
		l.PIP = equippedName
	case "secondemote":
		l.SecondEmote = equippedName
	case "tag":
		l.Tag = equippedName
	case "tint":
		l.Tint = equippedName
	case "tint_alignment_a":
		l.TintAlignmentA = equippedName
	case "tint_alignment_b":
		l.TintAlignmentB = equippedName
	case "tint_body":
		l.TintBody = equippedName
	case "title":
		l.Title = equippedName
	default:
		return false
	}
	return true
}

// knownLoadoutSlot reports whether slotName is a slot this server recognises.
//
// The handler uses it while PARSING, so an unrecognised slot is rejected and
// logged exactly once per request rather than re-discovered on every write
// attempt of a retry.
func knownLoadoutSlot(slotName string) bool {
	var probe evr.CosmeticLoadout
	return setLoadoutSlot(&probe, slotName, "")
}

// persistGameServerLoadout applies equips to profile and writes it, retrying on a
// storage version conflict.
//
// The retry is the entire reason this is a function rather than a few lines inline
// in the handler. evrProfileUpdateWithRetry RE-READS the profile after a conflict
// and hands that fresh object to the callback, so the callback must derive
// everything except equips from the profile it is HANDED. A callback that instead
// closed over the caller's pre-conflict profile would stamp that stale 22-slot
// loadout back over whatever the concurrent writer committed — precisely the
// clobber the retry exists to prevent, and a silent one: no error, no log.
//
// That mistake is a one-character edit away (dropping the parameter, or renaming
// it so the outer variable is no longer shadowed) and the type checker cannot see
// it. Keeping the callback here, next to a test that drives a real version
// conflict through this function, is what makes the mistake fail loudly.
//
// It returns the loadout actually stored: after a retry that is the one composed
// onto the FRESH base, so the caller's success log matches storage rather than the
// attempt that lost.
func persistGameServerLoadout(ctx context.Context, nk runtime.NakamaModule, userID string, profile *EVRProfile, equips []gameServerLoadoutEquip, jerseyNumber int) (evr.CosmeticLoadout, error) {
	var written evr.CosmeticLoadout

	apply := func(profile *EVRProfile) error {
		w, err := applyGameServerLoadout(profile, equips, jerseyNumber)
		if err != nil {
			return err
		}
		written = w
		return nil
	}

	if err := apply(profile); err != nil {
		return evr.CosmeticLoadout{}, err
	}
	if _, err := evrProfileUpdateWithRetry(ctx, nk, userID, profile, apply); err != nil {
		return evr.CosmeticLoadout{}, fmt.Errorf("failed to store EVR profile: %w", err)
	}
	return written, nil
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

	// Convert the hash-based items to (slot, equipped) name pairs.
	//
	// Parsing is deliberately independent of the profile. A retry after a version
	// conflict must compose these equips onto the loadout of the FRESHLY read
	// profile; capturing a base loadout here instead would stamp this request's
	// pre-conflict copy back over whatever the concurrent writer committed —
	// exactly the clobber evrProfileUpdateWithRetry exists to prevent.
	equips := make([]gameServerLoadoutEquip, 0, len(payload.LoadoutInstances))

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

			if !knownLoadoutSlot(slotName) {
				logger.Debug("Unknown slot type", zap.String("slot", slotName))
				continue
			}
			equips = append(equips, gameServerLoadoutEquip{slot: slotName, equipped: equippedName})
		}
	}

	// Update profile with new loadout, rejecting any cosmetic the player does not own
	// (COSMETIC-1). This handler is the *sole* persistence path for game servers with
	// NativeSupport: evr_runtime_event_remotelogset.go's equip handler explicitly skips
	// itself for NativeSupport matches ("NEVR (non-legacy) servers already persist
	// loadout equips themselves"), so the ownership fix applied there does not run for
	// those servers at all. Without this check, a NativeSupport-hosted character
	// customization equip would persist an unowned cosmetic (e.g. a VRML finalist tag)
	// exactly like the original remotelogset bug.
	// persistGameServerLoadout applies the equips and saves the profile, retrying
	// on a version conflict with a fresh read: a concurrent writer on the same key
	// (the Discord sync path and the login path both write it) must not cost the
	// player this equip. The retry composes the equips onto the RE-READ profile, so
	// only the slots this request named change; see its doc comment.
	writtenLoadout, err := persistGameServerLoadout(ctx, p.nk, userID, profile, equips, payload.Number)
	if err != nil {
		return err
	}

	// Logged only after the write commits. Emitting it before meant a failed write
	// still reported a jersey number that never persisted.
	if payload.Number >= 0 {
		logger.Info("Updated jersey number", zap.Int("number", payload.Number))
	}

	logger.Info("Successfully saved loadout update",
		zap.String("user_id", userID),
		zap.String("evr_id", request.EvrID.String()),
		zap.Any("loadout", writtenLoadout))

	return nil
}
