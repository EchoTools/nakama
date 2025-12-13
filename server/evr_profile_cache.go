package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"maps"

	"github.com/gofrs/uuid/v5"
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

// ProfileCache is a registry of user evr profiles.
type ProfileCache struct {
	sync.RWMutex
	ctx         context.Context
	ctxCancelFn context.CancelFunc
	logger      runtime.Logger
	db          *sql.DB
	nk          runtime.NakamaModule

	metrics         Metrics
	sessionRegistry SessionRegistry

	profileByXPID    map[evr.EvrId]json.RawMessage
	sessionIDsByXPID map[evr.EvrId]map[uuid.UUID]struct{}
}

func NewProfileRegistry(nk runtime.NakamaModule, db *sql.DB, logger runtime.Logger, metrics Metrics, sessionRegistry SessionRegistry) *ProfileCache {

	ctx, cancelFn := context.WithCancel(context.Background())

	profileRegistry := &ProfileCache{
		ctx:         ctx,
		ctxCancelFn: cancelFn,
		logger:      logger,
		db:          db,
		nk:          nk,

		metrics:         metrics,
		sessionRegistry: sessionRegistry,

		profileByXPID:    make(map[evr.EvrId]json.RawMessage),
		sessionIDsByXPID: make(map[evr.EvrId]map[uuid.UUID]struct{}),
	}

	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-profileRegistry.ctx.Done():
				return
			case <-ticker.C:
				profileRegistry.Lock()
				for xpid, sessionIDs := range profileRegistry.sessionIDsByXPID {
					for sessionID := range sessionIDs {
						if profileRegistry.sessionRegistry.Get(sessionID) == nil {

							delete(profileRegistry.sessionIDsByXPID[xpid], sessionID)

							// If there are no more sessions for this xpid, delete the profile
							if len(profileRegistry.sessionIDsByXPID[xpid]) == 0 {
								delete(profileRegistry.profileByXPID, xpid)
							}
						}
					}
				}
				profileRegistry.Unlock()
			}
		}
	}()

	return profileRegistry
}

func (r *ProfileCache) Load(xpid evr.EvrId) (json.RawMessage, bool) {
	r.RLock()
	defer r.RUnlock()
	profiles, found := r.profileByXPID[xpid]
	return profiles, found
}

func (r *ProfileCache) Store(sessionID uuid.UUID, p evr.ServerProfile) (json.RawMessage, error) {
	r.Lock()
	defer r.Unlock()

	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal profile: %w", err)
	}

	r.profileByXPID[p.EvrID] = data

	if _, ok := r.sessionIDsByXPID[p.EvrID]; !ok {
		r.sessionIDsByXPID[p.EvrID] = make(map[uuid.UUID]struct{})
	}

	r.sessionIDsByXPID[p.EvrID][sessionID] = struct{}{}

	return data, nil
}

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

func UserServerProfileFromParameters(ctx context.Context, logger *zap.Logger, db *sql.DB, nk runtime.NakamaModule, params SessionParameters, groupID string, modes []evr.Symbol, dailyWeeklyMode evr.Symbol) (*evr.ServerProfile, error) {
	return NewUserServerProfile(ctx, logger, db, nk, params.profile, params.xpID, groupID, modes, dailyWeeklyMode, params.profile.GetGroupIGN(groupID))
}

func NewUserServerProfile(ctx context.Context, logger *zap.Logger, db *sql.DB, nk runtime.NakamaModule, evrProfile *EVRProfile, xpID evr.EvrId, groupID string, modes []evr.Symbol, dailyWeeklyMode evr.Symbol, displayName string) (*evr.ServerProfile, error) {

	var wallet map[string]int64
	if err := json.Unmarshal([]byte(evrProfile.Wallet()), &wallet); err != nil {
		return nil, fmt.Errorf("failed to unmarshal wallet: %w", err)
	}

	cosmetics := make(map[string]map[string]bool)
	for m, c := range cosmeticDefaults(evrProfile.Options.EnableAllCosmetics) {
		cosmetics[m] = make(map[string]bool, len(c))
		maps.Copy(cosmetics[m], c)
	}
	cosmetics = walletToCosmetics(wallet, cosmetics)

	cosmeticLoadout := evrProfile.LoadoutCosmetics.Loadout

	// If the player has "kissy lips" emote equipped, set their emote to default.
	if cosmeticLoadout.Emote == "emote_kissy_lips_a" {
		cosmeticLoadout.Emote = "emote_blink_smiley_a"
		cosmeticLoadout.SecondEmote = "emote_blink_smiley_a"
	}

	var developerFeatures *evr.DeveloperFeatures

	if evrProfile.Options.GoldDisplayNameActive {
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

	// Apply level override from player metadata if set
	if evrProfile.LevelOverride != nil {
		levelValue := &evr.StatisticValue{
			Count: 1,
			Value: float64(*evrProfile.LevelOverride),
		}
		for g, stats := range statsBySchedule {
			switch g.Mode {
			case evr.ModeArenaPublic:
				if arenaStats, ok := stats.(*evr.ArenaStatistics); ok {
					arenaStats.Level = levelValue
				}
			case evr.ModeCombatPublic:
				if combatStats, ok := stats.(*evr.CombatStatistics); ok {
					combatStats.Level = levelValue
				}
			}
		}
	}

	if evrProfile.Options.DisableAFKTimeout {
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

func (r *ProfileCache) ValidateArenaUnlockByName(i interface{}, itemName string) (bool, error) {
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

func (r *ProfileCache) retrieveStarterLoadout(ctx context.Context) (evr.CosmeticLoadout, error) {
	defaultLoadout := evr.DefaultCosmeticLoadout()
	// Retrieve a random starter loadout from storage
	objs, _, err := r.nk.StorageList(ctx, uuid.Nil.String(), uuid.Nil.String(), CosmeticLoadoutCollection, 100, "")
	if err != nil {
		return defaultLoadout, fmt.Errorf("failed to list objects: %w", err)
	}
	if len(objs) == 0 {
		return defaultLoadout, nil
	}

	var loadouts []StoredCosmeticLoadout
	if err = json.Unmarshal([]byte(objs[0].Value), &loadouts); err != nil {
		return defaultLoadout, fmt.Errorf("error unmarshalling client profile: %w", err)
	}

	// Pick a random id
	loadout := loadouts[rand.Intn(len(loadouts))]
	return loadout.Loadout, nil
}

type StoredCosmeticLoadout struct {
	LoadoutID string              `json:"loadout_id"`
	Loadout   evr.CosmeticLoadout `json:"loadout"`
	UserID    string              `json:"user_id"` // the creator
}
