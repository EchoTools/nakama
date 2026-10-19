package server

import (
	"context"
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
func applyGameServerLoadout(profile *EVRProfile, equips []gameServerLoadoutEquip, jerseyNumber int, metricsCounterAdd metricsCounterAddFunc) (evr.CosmeticLoadout, error) {
	loadout := profile.LoadoutCosmetics.Loadout
	for _, e := range equips {
		setLoadoutSlot(&loadout, e.slot, e.equipped)
	}

	sanitized, err := sanitizeGameServerLoadout(loadout, profile, metricsCounterAdd)
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
		w, err := applyGameServerLoadout(profile, equips, jerseyNumber, nk.MetricsCounterAdd)
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
//
// metricsCounterAdd may be nil; see sanitizeLoadout.
func sanitizeGameServerLoadout(loadout evr.CosmeticLoadout, evrProfile *EVRProfile, metricsCounterAdd metricsCounterAddFunc) (evr.CosmeticLoadout, error) {
	owned, err := profileOwnedCosmetics(evrProfile)
	if err != nil {
		return loadout, err
	}
	// The strip report is discarded here. The owner's ruling covers the equip path; this
	// is a different entry point (the game server's own save-loadout request) and giving
	// it a log line is a separate decision, not this change's to make. Wiring it later is
	// one logCosmeticStrips call, because the report now exists.
	sanitized, _ := sanitizeLoadout(loadout, owned, metricsCounterAdd, sanitizePathGameServer)
	return sanitized, nil
}

// gameServerSaveLoadoutRequest handles loadout save requests from the game server.
// This is triggered when a player updates their loadout at the character customization screen.
// The game server receives an internal broadcaster message (SR15NetSaveLoadoutRequest) and
// forwards it to nakama via the TCP broadcaster WebSocket connection.
func (p *EvrPipeline) gameServerSaveLoadoutRequest(ctx context.Context, logger *zap.Logger, session Session, request *evr.GameServerSaveLoadoutRequest) error {
	return applyGameServerSaveLoadoutLegacy(ctx, logger, p.nk, p.sessionRegistry, session.ID(), request)
}
