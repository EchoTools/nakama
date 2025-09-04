package service

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/samber/lo"
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

func CosmeticsFromWallet(wallet map[string]int64, unlocks map[string]map[string]bool) map[string]map[string]bool {
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

func NewClientProfile(ctx context.Context, evrProfile *EVRProfile, serverProfile *evr.ServerProfile) *evr.ClientProfile {
	// Load friends to get blocked (ghosted) players
	muted := make([]evr.XPID, 0)
	ghosted := make([]evr.XPID, 0)
	if m := evrProfile.GetMuted(); len(m) > 0 {
		muted = append(muted, m...)
	}
	if g := evrProfile.GetGhosted(); len(g) > 0 {
		ghosted = append(ghosted, g...)
	}
	if evrProfile.NewUnlocks == nil {
		evrProfile.NewUnlocks = []int64{}
	}
	// Remove newunlocks for cosmetics that the user does not have unlocked
	for i := 0; i < len(evrProfile.NewUnlocks); i++ {
		sym := evr.ToSymbol(evrProfile.NewUnlocks[i])
		name := sym.String()
		if !serverProfile.IsUnlocked(name) {
			evrProfile.NewUnlocks = slices.Delete(evrProfile.NewUnlocks, i, i+1)
			i--
		}
	}

	// Remove kissy lips from new unlocks
	if i := slices.Index(evrProfile.NewUnlocks, -6079176325296842000); i != -1 {
		evrProfile.NewUnlocks = slices.Delete(evrProfile.NewUnlocks, i, i+1)
	}

	var customizationPOIs *evr.Customization
	if evrProfile.CustomizationPOIs != nil {
		customizationPOIs = evrProfile.CustomizationPOIs
	} else {
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
		EvrID:              serverProfile.XPID,
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
		NewUnlocks: evrProfile.NewUnlocks, // This could pull from the wallet ledger
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

func cosmeticDefaults(enableAll bool) map[string]map[string]bool {
	if enableAll {
		return allCosmetics
	}
	return defaultCosmetics
}

func ValidateArenaUnlockByName(i interface{}, itemName string) (bool, error) {
	// Lookup the field name by it's item name (json key)
	fieldName, found := unlocksByItemName[itemName]
	if !found {
		return false, fmt.Errorf("unknown item name: %s", itemName)
	}

	// Lookup the field value by it's field name
	value := reflect.ValueOf(i)
	typ := value.Type()
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.Name == fieldName {
			return value.FieldByName(fieldName).Bool(), nil
		}
	}

	return false, fmt.Errorf("unknown unlock field name: %s", fieldName)
}
