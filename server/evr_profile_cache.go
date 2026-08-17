package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"maps"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/samber/lo"
	"go.uber.org/zap"
)

var (
	ErrProfileNotFound = fmt.Errorf("profile not found")
)

var unlocksByItemName map[string]string

func init() {

	byItemName := make(map[string]string)
	types := []interface{}{evr.ArenaUnlocks{}, evr.CombatUnlocks{}}
	for _, t := range types {
		for i := 0; i < reflect.TypeOf(t).NumField(); i++ {
			field := reflect.TypeOf(t).Field(i)
			tag := field.Tag.Get("json")
			name := strings.SplitN(tag, ",", 2)[0]
			byItemName[field.Name] = name
		}
	}
	unlocksByItemName = byItemName

}

type StarterCosmeticLoadouts struct {
	Loadouts []*StoredCosmeticLoadout `json:"loadouts"`
}

// metricsCounterAddFunc is the one method of runtime.NakamaModule the sanitize path
// needs. Taking the method value rather than the module keeps sanitizeLoadout a pure
// function of its arguments, which is what lets a test hand it a recorder — the same
// seam warnVPNDegraded (evr_lobby_joinentrant.go) uses.
type metricsCounterAddFunc func(name string, tags map[string]string, delta int64)

// cosmeticStrippedCounter counts slots reset to their default because the player did
// not own what was equipped there. Tagged "slot" (one of the CosmeticLoadout json
// keys, a fixed set of 22) and "path" (loadoutSanitizePath, a fixed set of 3) — both
// bounded at compile time, so the tag cardinality cannot grow with traffic.
const cosmeticStrippedCounter = "profile_cosmetic_stripped"

// cosmeticStrip is one slot sanitizeLoadout reset because the player did not own what
// was equipped there.
//
// It carries what the counter deliberately cannot. cosmeticStrippedCounter's tags are
// bounded at compile time so the metrics backend cannot be taken down by traffic
// (TestSanitizeLoadout_StripCounterTagsAreBounded), which is exactly why it can answer
// "how often" and never "which player, which item". Those two questions want different
// instruments: the counter carries the RATE, this carries the IDENTITY, and splitting
// them means neither has to be at the wrong level to do the other's job.
type cosmeticStrip struct {
	Slot      string // CosmeticLoadout json key, e.g. "tag"
	ItemID    string // what the player had equipped and does not own
	DefaultID string // what the slot was reset to
}

// cosmeticRejectLogMsg is the whole message text, held as a constant so a test asserts
// against the same string production emits and an operator has one thing to grep for.
const cosmeticRejectLogMsg = "Cosmetic ownership reject: equipped item not owned, slot reset to default"

// logCosmeticStrips records an ownership reject: one line per stripped slot, naming the
// player and the item.
//
// Owner ruling 2026-08-16: "no, a reject does not tell the player. it just logs it."
// Nothing here reaches the session; the player is sent nothing on any branch.
//
// Fields are WHAT (the message) / WHERE (path) / IDENTIFIER (user_id, item_id, slot) /
// OUTCOME (default_id — reset to the default, and *which* default).
//
// DEBUG, not INFO. Level exists to discriminate expected from unexpected, and an
// ownership reject is the control working as designed — there is no unexpected
// counterpart on this event, so INFO would assert a signal that is not one. It stays
// retrievable because production runs at level: debug. The counter-argument is real and
// recorded rather than dismissed: every strip-eligible item measured is a restricted
// VRML/award tag, so a reject may be an exploit signal. That question is about volume
// over time, which is the counter's job and which the counter answers better than grep.
//
// One line per slot, not per event: an equip sanitizes the whole loadout, so a poisoned
// stored loadout can strip several slots at once, and a single line naming two items
// cannot be grepped for either one.
//
// Callers pass only the strips from a persisted write. Calling it on a report from an
// attempt that was rolled back or retried would record a reject that never happened.
func logCosmeticStrips(logger runtime.Logger, userID string, path loadoutSanitizePath, strips []cosmeticStrip) {
	for _, s := range strips {
		logger.WithFields(map[string]any{
			"user_id":    userID,
			"item_id":    s.ItemID,
			"slot":       s.Slot,
			"default_id": s.DefaultID,
			"path":       string(path),
		}).Debug(cosmeticRejectLogMsg)
	}
}

// loadoutSanitizePath names which of the three entry points into sanitizeLoadout
// reached a strip. Without it the counter answers "how often" but not "on write or on
// read", and those have different meanings: a strip on a write path is an equip the
// player just lost, while a strip on the serve path is a stored loadout that was
// already poisoned before the equip-time checks existed.
type loadoutSanitizePath string

const (
	// sanitizePathEquip is EquipAndSanitize, the in-game equip write path
	// (RemoteLogSet -> evr_runtime_event_remotelogset.go).
	sanitizePathEquip loadoutSanitizePath = "equip"
	// sanitizePathServe is equippedCosmeticsForProfile, the read/regeneration path
	// that runs on every login, lobby join and equip event.
	sanitizePathServe loadoutSanitizePath = "serve"
	// sanitizePathGameServer is sanitizeGameServerLoadout, the write path for
	// GameServerSaveLoadoutRequest from NativeSupport game servers.
	sanitizePathGameServer loadoutSanitizePath = "gameserver"
)

func walletToCosmetics(wallet map[string]int64, unlocks map[string]map[string]bool) map[string]map[string]bool {
	if unlocks == nil {
		unlocks = make(map[string]map[string]bool)
	}

	for k, v := range wallet {
		if v <= 0 {
			continue
		}

		// cosmetic:arena:rwd_tag_s1_vrml_s1
		if k, ok := strings.CutPrefix(k, "cosmetic:"); ok {
			if mode, item, ok := strings.Cut(k, ":"); ok {
				if _, ok := unlocks[mode]; !ok {
					unlocks[mode] = make(map[string]bool)
				}
				unlocks[mode][item] = true
			}
		}
	}
	return unlocks
}

// profileOwnedCosmetics returns the mode→item→owned set the player is entitled to:
// the default (or all, when EnableAllCosmetics) cosmetics merged with wallet-granted
// unlocks. This is the single source of truth for "does this player own this cosmetic",
// used both when building the broadcast server profile and when validating an equip.
func profileOwnedCosmetics(evrProfile *EVRProfile) (map[string]map[string]bool, error) {
	var wallet map[string]int64
	if err := json.Unmarshal([]byte(evrProfile.Wallet()), &wallet); err != nil {
		return nil, fmt.Errorf("failed to unmarshal wallet: %w", err)
	}

	cosmetics := make(map[string]map[string]bool)
	for m, c := range cosmeticDefaults(evrProfile.EnableAllCosmetics) {
		cosmetics[m] = make(map[string]bool, len(c))
		maps.Copy(cosmetics[m], c)
	}
	return walletToCosmetics(wallet, cosmetics), nil
}

// EquipAndSanitize applies a client-requested equip and then strips any resulting
// cosmetic the player does not own, returning a loadout safe to persist. This closes
// COSMETIC-1: the in-game equip path previously stored whatever the client claimed to
// equip (e.g. VRML finalist tags) with no ownership check. Sanitizing here mirrors the
// broadcast path (NewUserServerProfile), so the stored loadout can never hold an item
// the player is not entitled to, while default items and legitimately-owned cosmetics
// pass through unchanged.
//
// metricsCounterAdd may be nil; see sanitizeLoadout.
// It returns the strips it made so the equip path can record the reject. The report is
// empty on a successful owned equip, which is what makes "no line on the happy path" a
// property of the data rather than of a condition somebody remembered to write.
func EquipAndSanitize(loadout evr.CosmeticLoadout, category, name string, owned map[string]map[string]bool, metricsCounterAdd metricsCounterAddFunc) (evr.CosmeticLoadout, []cosmeticStrip, error) {
	equipped, err := LoadoutEquipItem(loadout, category, name)
	if err != nil {
		return loadout, nil, err
	}
	sanitized, strips := sanitizeLoadout(equipped, owned, metricsCounterAdd, sanitizePathEquip)
	return sanitized, strips, nil
}

// equippedCosmeticsForProfile computes the sanitized cosmetic loadout that is safe to
// serve — to other players (via ServerProfileStore -> otherUserProfileRequest) and to
// the broadcast game server — even when evrProfile.LoadoutCosmetics.Loadout already
// holds an unowned item (e.g. an account "poisoned" before the COSMETIC-1 equip-time
// fixes existed, such as db484900-...). This is the read-path counterpart to
// EquipAndSanitize / sanitizeGameServerLoadout: those guard what gets *written*: this
// guards what gets *served*, and it runs unconditionally every time NewUserServerProfile
// is called (login, lobby join, equip event) regardless of which path wrote the stored
// value. No backfill of already-poisoned accounts is required for the "other players
// see it" concern because of this: every regeneration re-derives ownership from the
// current wallet and strips anything not currently owned.
//
// metricsCounterAdd may be nil; see sanitizeLoadout.
func equippedCosmeticsForProfile(evrProfile *EVRProfile, metricsCounterAdd metricsCounterAddFunc) (evr.CosmeticLoadout, map[string]map[string]bool, error) {
	cosmetics, err := profileOwnedCosmetics(evrProfile)
	if err != nil {
		return evr.CosmeticLoadout{}, nil, err
	}

	// The strip report is deliberately discarded, and this function deliberately takes
	// no logger. This path runs on every profile regeneration -- login, lobby join, and
	// every equip event -- against the stored loadout rather than a player action, so an
	// already-poisoned account strips here on every single regeneration. A line per
	// regeneration would be a flood that buries the ~3/day the equip path produces. The
	// rate on this path is the counter's job; it is already tagged path="serve".
	cosmeticLoadout, _ := sanitizeLoadout(evrProfile.LoadoutCosmetics.Loadout, cosmetics, metricsCounterAdd, sanitizePathServe)

	// If the player has "kissy lips" emote equipped, set their emote to default.
	if cosmeticLoadout.Emote == "emote_kissy_lips_a" {
		cosmeticLoadout.Emote = "emote_blink_smiley_a"
		cosmeticLoadout.SecondEmote = "emote_blink_smiley_a"
	}

	return cosmeticLoadout, cosmetics, nil
}

func UserServerProfileFromParameters(ctx context.Context, logger *zap.Logger, db *sql.DB, nk runtime.NakamaModule, params *SessionParameters, groupID string, modes []evr.Symbol, dailyWeeklyMode evr.Symbol) (*evr.ServerProfile, error) {
	return NewUserServerProfile(ctx, logger, db, nk, params.profile, params.xpID, groupID, modes, dailyWeeklyMode, params.profile.GetGroupIGN(groupID))
}

func NewUserServerProfile(ctx context.Context, logger *zap.Logger, db *sql.DB, nk runtime.NakamaModule, evrProfile *EVRProfile, xpID evr.EvrId, groupID string, modes []evr.Symbol, dailyWeeklyMode evr.Symbol, displayName string) (*evr.ServerProfile, error) {

	cosmeticLoadout, cosmetics, err := equippedCosmeticsForProfile(evrProfile, nk.MetricsCounterAdd)
	if err != nil {
		return nil, err
	}

	var developerFeatures *evr.DeveloperFeatures

	if evrProfile.GoldDisplayNameActive {
		developerFeatures = &evr.DeveloperFeatures{}
	}

	// Default to their main group if they are not a member of the group
	if _, ok := evrProfile.GetGroupDisplayName(groupID); !ok || groupID == "" {
		groupID = evrProfile.GetActiveGroupID().String()

	}

	if slices.Equal(modes, []evr.Symbol{0}) {
		modes = []evr.Symbol{evr.ModeArenaPublic, evr.ModeCombatPublic}
	}

	statsBySchedule, _, err := PlayerStatisticsGetID(ctx, db, nk, evrProfile.ID(), groupID, modes, dailyWeeklyMode)
	if err != nil {
		return nil, fmt.Errorf("failed to get user tablet statistics: %w", err)
	}

	// Override win percentages with rolling last-100-games values from MongoDB.
	if mc := globalMongoClient.Load(); mc != nil {
		arenaGroup := evr.StatisticsGroup{Mode: evr.ModeArenaPublic, ResetSchedule: evr.ResetScheduleAllTime}
		if arenaStats, ok := statsBySchedule[arenaGroup]; ok {
			if arena, ok := arenaStats.(*evr.ArenaStatistics); ok {
				if winRate, count, err := GetRecentWinRate(ctx, mc, evrProfile.ID(), evr.ModeArenaPublic.String(), 100); err != nil {
					logger.Warn("Failed to get recent arena win rate", zap.Error(err))
				} else if count > 0 {
					arena.RecentWinPercentage = &winRate
				}
			}
		}
		combatGroup := evr.StatisticsGroup{Mode: evr.ModeCombatPublic, ResetSchedule: evr.ResetScheduleAllTime}
		if combatStats, ok := statsBySchedule[combatGroup]; ok {
			if combat, ok := combatStats.(*evr.CombatStatistics); ok {
				if winRate, count, err := GetRecentWinRate(ctx, mc, evrProfile.ID(), evr.ModeCombatPublic.String(), 100); err != nil {
					logger.Warn("Failed to get recent combat win rate", zap.Error(err))
				} else if count > 0 {
					combat.RecentWinPercentage = &winRate
				}
			}
		}
	}

	// Consume lifetime XP into level + remainder for client display.
	// Leaderboard records are not modified — only the profile sent to clients.
	consumeXPIntoLevel(statsBySchedule)

	if evrProfile.DisableAFKTimeout {
		developerFeatures = &evr.DeveloperFeatures{
			DisableAfkTimeout: true,
		}
	}

	return &evr.ServerProfile{
		DisplayName:       displayName,
		EvrID:             xpID,
		SchemaVersion:     4,
		PublisherLock:     "echovrce",
		LobbyVersion:      1680630467,
		PurchasedCombat:   1,
		Statistics:        statsBySchedule,
		UnlockedCosmetics: cosmetics,
		EquippedCosmetics: evr.EquippedCosmetics{
			Number:     int(evrProfile.LoadoutCosmetics.JerseyNumber),
			NumberBody: int(evrProfile.LoadoutCosmetics.JerseyNumber),
			Instances: evr.CosmeticInstances{
				Unified: evr.UnifiedCosmeticInstance{
					Slots: cosmeticLoadout,
				},
			},
		},

		Social: evr.ServerSocial{
			Channel: evr.GUID(evrProfile.GetActiveGroupID()),
		},
		DeveloperFeatures: developerFeatures,
	}, nil
}

// sanitizeLoadout replaces any equipped cosmetic the player does not own
// (per the wallet-derived cosmetics map) with the safe default for that slot.
// Empty fields are left untouched.
//
// The strip is deliberate and is not changing: an item nobody owns must not be
// served or stored. What is new here is that it is no longer silent to US. Every
// reset increments cosmeticStrippedCounter tagged with the slot and the path that
// reached it, because until now a player equipped an item, was told the save
// succeeded, wore the default, and the server recorded nothing at all — no log, no
// error, no metric. The counter does not tell the PLAYER anything; it only makes the
// rate visible to operators, and it exists to be reconciled against the
// independently measured strip-eligible rate (0.326%-0.3498% of equips). Two
// instruments built from different data that disagree mean one of them is wrong.
//
// metricsCounterAdd may be nil, which disables counting: tests that exercise the
// sanitize logic itself pass nil rather than growing a metrics dependency.
//
// It returns the strips it made alongside the sanitized loadout. Returning only the
// loadout made "changed" and "no-op" indistinguishable to every caller — the function
// mutated and reported nothing, so a caller wanting to react to a reject had no signal
// short of diffing the loadout itself. The report is a query over what the modifier
// did; callers act on it being non-empty, not on the absence of an error. Callers that
// do not care (the serve path) ignore it and cost nothing.
func sanitizeLoadout(loadout evr.CosmeticLoadout, cosmetics map[string]map[string]bool, metricsCounterAdd metricsCounterAddFunc, path loadoutSanitizePath) (evr.CosmeticLoadout, []cosmeticStrip) {
	var strips []cosmeticStrip
	defaults := evr.DefaultCosmeticLoadout()
	defaultMap := defaults.ToMap() // json_key → default item ID
	result := loadout
	v := reflect.ValueOf(&result).Elem()
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		jsonTag := strings.SplitN(t.Field(i).Tag.Get("json"), ",", 2)[0]
		itemID := v.Field(i).String()
		if itemID == "" {
			continue // omitempty slot, leave it
		}
		unlocked := false
		for _, modeUnlocks := range cosmetics {
			if modeUnlocks[itemID] {
				unlocked = true
				break
			}
		}
		if !unlocked {
			if def, ok := defaultMap[jsonTag]; ok {
				v.Field(i).SetString(def)
				strips = append(strips, cosmeticStrip{Slot: jsonTag, ItemID: itemID, DefaultID: def})
				if metricsCounterAdd != nil {
					metricsCounterAdd(cosmeticStrippedCounter, map[string]string{
						"slot": jsonTag,
						"path": string(path),
					}, 1)
				}
			}
		}
	}
	return result, strips
}

func NewClientProfile(ctx context.Context, evrProfile *EVRProfile, serverProfile *evr.ServerProfile) *evr.ClientProfile {
	// Load friends to get blocked (ghosted) players
	muted := make([]evr.EvrId, 0)
	ghosted := make([]evr.EvrId, 0)
	if m := evrProfile.GetMuted(); len(m) > 0 {
		muted = append(muted, m...)
	}
	if g := evrProfile.GetGhosted(); len(g) > 0 {
		ghosted = append(ghosted, g...)
	}
	if evrProfile.NewUnlocks == nil {
		evrProfile.NewUnlocks = []int64{}
	}

	// Copy the slice to avoid mutating the original backing array.
	newUnlocks := make([]int64, len(evrProfile.NewUnlocks))
	copy(newUnlocks, evrProfile.NewUnlocks)

	// Remove newunlocks for cosmetics that the user does not have unlocked
	for i := 0; i < len(newUnlocks); i++ {
		sym := evr.ToSymbol(newUnlocks[i])
		name := sym.String()
		if !serverProfile.IsUnlocked(name) {
			newUnlocks = slices.Delete(newUnlocks, i, i+1)
			i--
		}
	}

	// Remove kissy lips from new unlocks
	if i := slices.Index(newUnlocks, -6079176325296842000); i != -1 {
		newUnlocks = slices.Delete(newUnlocks, i, i+1)
	}

	customizationPOIs := evrProfile.CustomizationPOIs
	if customizationPOIs == nil {
		customizationPOIs = &evr.Customization{
			BattlePassSeasonPoiVersion: 3246,
			NewUnlocksPoiVersion:       1,
			StoreEntryPoiVersion:       1,
			ClearNewUnlocksVersion:     1,
		}
	}

	return &evr.ClientProfile{
		ModifyTime:         time.Now().UTC().Unix(),
		DisplayName:        serverProfile.DisplayName,
		EvrID:              serverProfile.EvrID,
		TeamName:           evrProfile.TeamName,
		CombatWeapon:       evrProfile.CombatLoadout.CombatWeapon,
		CombatGrenade:      evrProfile.CombatLoadout.CombatGrenade,
		CombatDominantHand: evrProfile.CombatLoadout.CombatDominantHand,
		CombatAbility:      evrProfile.CombatLoadout.CombatAbility,
		MutedPlayers: evr.Players{
			Players: muted,
		},
		GhostedPlayers: evr.Players{
			Players: ghosted,
		},
		LegalConsents: evrProfile.LegalConsents,
		NewPlayerProgress: evr.NewPlayerProgress{
			Lobby: evr.NpeMilestone{Completed: true},

			FirstMatch:        evr.NpeMilestone{Completed: true},
			Movement:          evr.NpeMilestone{Completed: true},
			ArenaBasics:       evr.NpeMilestone{Completed: true},
			SocialTabSeen:     evr.Versioned{Version: 1},
			Pointer:           evr.Versioned{Version: 1},
			BlueTintTabSeen:   evr.Versioned{Version: 1},
			HeraldryTabSeen:   evr.Versioned{Version: 1},
			OrangeTintTabSeen: evr.Versioned{Version: 1},
		},
		Customization: customizationPOIs,
		Social: evr.ClientSocial{
			CommunityValuesVersion: 1,
			SetupVersion:           1,
			Channel:                serverProfile.Social.Channel,
		},
		NewUnlocks: newUnlocks, // This could pull from the wallet ledger
	}
}

func GetFieldByJSONProperty(i interface{}, fieldName string) (bool, error) {
	// Lookup the field name by it's item name (json key)

	// Lookup the field value by it's field name
	value := reflect.ValueOf(i)
	typ := value.Type()
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.Name == fieldName {
			return value.FieldByName(fieldName).Bool(), nil
		}
	}

	return false, fmt.Errorf("unknown field name: %s", fieldName)
}

func LoadoutEquipItem(loadout evr.CosmeticLoadout, category string, name string) (evr.CosmeticLoadout, error) {
	newLoadout := loadout

	alignmentTints := map[string][]string{
		"tint_alignment_a": {
			"tint_blue_a_default",
			"tint_blue_b_default",
			"tint_blue_c_default",
			"tint_blue_d_default",
			"tint_blue_e_default",
			"tint_blue_f_default",
			"tint_blue_g_default",
			"tint_blue_h_default",
			"tint_blue_i_default",
			"tint_blue_j_default",
			"tint_blue_k_default",
			"tint_neutral_summer_a_default",
			"rwd_tint_s3_tint_e",
		},
		"tint_alignment_b": {
			"tint_orange_a_default",
			"tint_orange_b_default",
			"tint_orange_c_default",
			"tint_orange_i_default",
			"tint_neutral_spooky_a_default",
			"tint_neutral_spooky_d_default",
			"tint_neutral_xmas_c_default",
			"rwd_tint_s3_tint_b",
			"tint_orange_j_default",
			"tint_orange_d_default",
			"tint_orange_e_default",
			"tint_orange_f_default",
			"tint_orange_g_default",
			"tint_orange_h_default",
			"tint_orange_k_default",
		},
	}

	newLoadout.DecalBody = "rwd_decalback_default"

	// Exact mappings
	exactmap := map[string]*string{
		"emissive_default":      &newLoadout.Emissive,
		"rwd_decalback_default": &newLoadout.PIP,
	}
	if val, ok := exactmap[name]; ok {
		*val = name
	} else {

		switch category {
		case "emote":
			newLoadout.Emote = name
			newLoadout.SecondEmote = name
		case "decal":
			newLoadout.Decal = name
			newLoadout.DecalBody = name
		case "tint":
			// Assigning a tint to the alignment will also assign it to the body
			if lo.Contains(alignmentTints["tint_alignment_a"], name) {
				newLoadout.TintAlignmentA = name
			} else if lo.Contains(alignmentTints["tint_alignment_b"], name) {
				newLoadout.TintAlignmentB = name
			}
			if name != "tint_chassis_default" {
				// Equipping "tint_chassis_default" to heraldry tint causes every heraldry equipped to be pitch black.
				// It seems that the tint being pulled from doesn't exist on heraldry equippables.
				newLoadout.Tint = name
			}
			newLoadout.TintBody = name

		case "pattern":
			newLoadout.Pattern = name
			newLoadout.PatternBody = name
		case "chassis":
			newLoadout.Chassis = name
		case "bracer":
			newLoadout.Bracer = name
		case "booster":
			newLoadout.Booster = name
		case "title":
			newLoadout.Title = name
		case "tag", "heraldry":
			newLoadout.Tag = name
		case "banner":
			newLoadout.Banner = name
		case "medal":
			newLoadout.Medal = name
		case "goal":
			newLoadout.GoalFX = name
		case "emissive":
			newLoadout.Emissive = name
		//case "decalback":
		//	fallthrough
		case "pip":
			newLoadout.PIP = name
		default:
			return newLoadout, fmt.Errorf("unknown category: %s", category)
		}
	}
	// Update the timestamp
	return newLoadout, nil
}

var defaultCosmetics = func() map[string]map[string]bool {
	cosmetics := make(map[string]map[string]bool)
	structs := map[string]interface{}{
		"arena":  evr.ArenaUnlocks{},
		"combat": evr.CombatUnlocks{},
	}
	for m, t := range structs {
		v := reflect.ValueOf(t)
		if v.Kind() == reflect.Ptr {
			v = v.Elem()
		}

		cosmetics[m] = make(map[string]bool)

		for i := 0; i < v.NumField(); i++ {
			tag := v.Type().Field(i).Tag.Get("validate")
			j := v.Type().Field(i).Tag.Get("json")
			j = strings.SplitN(j, ",", 2)[0]
			if j == "" {
				continue
			}
			cosmetics[m][j] = !strings.Contains(tag, "restricted") && !strings.Contains(tag, "blocked")
		}
	}
	return cosmetics
}()

var allCosmetics = func() map[string]map[string]bool {

	all := make(map[string]map[string]bool, len(defaultCosmetics))
	for m, t := range defaultCosmetics {
		all[m] = make(map[string]bool, len(t))
		for k := range t {
			all[m][k] = true
		}
	}
	return all
}()

// cosmeticDefaults returns a fresh deep copy of the process-lifetime defaultCosmetics
// (or allCosmetics) singleton. This MUST copy rather than return the shared map
// directly: every known caller feeds the result straight into walletToCosmetics, which
// mutates its "unlocks" argument in place (unlocks[mode][item] = true). Before this
// function copied, that meant a single call — e.g. UserUnlockedCosmetics
// (evr_discord_loadout.go), invoked on every /loadout autocomplete keystroke and on
// /loadout set — would permanently merge one player's wallet-granted cosmetic (VRML
// tag, medal, etc.) into the shared defaultCosmetics/allCosmetics map for the lifetime
// of the running process, silently granting that restricted item to every other player
// from then on. This is a distinct, likely-live contributor to COSMETIC-1 reports beyond
// the unvalidated-equip write paths, and a test-isolation hazard (a test calling
// cosmeticDefaults(false) followed by walletToCosmetics would poison every later test in
// the same process, exactly as observed by
// TestEquippedCosmeticsForProfile_PoisonedAccountNeverServesUnownedItem failing only when
// run after TestEquipAndSanitize_AllowsOwnedVRMLTag in the same binary).
func cosmeticDefaults(enableAll bool) map[string]map[string]bool {
	src := defaultCosmetics
	if enableAll {
		src = allCosmetics
	}
	out := make(map[string]map[string]bool, len(src))
	for m, c := range src {
		out[m] = make(map[string]bool, len(c))
		maps.Copy(out[m], c)
	}
	return out
}

type StoredCosmeticLoadout struct {
	LoadoutID string              `json:"loadout_id"`
	Loadout   evr.CosmeticLoadout `json:"loadout"`
	UserID    string              `json:"user_id"` // the creator
}
