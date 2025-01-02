package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/samber/lo"
)

var unlocksByItemName map[string]string

func init() {

	unlocks := make(map[string]string)
	types := []interface{}{evr.ArenaUnlocks{}, evr.CombatUnlocks{}}
	for _, t := range types {
		for i := 0; i < reflect.TypeOf(t).NumField(); i++ {
			field := reflect.TypeOf(t).Field(i)
			tag := field.Tag.Get("json")
			name := strings.SplitN(tag, ",", 2)[0]
			unlocks[name] = field.Name
		}
	}
	unlocksByItemName = unlocks

}

// ProfileRegistry is a registry of user evr profiles.
type ProfileRegistry struct {
	ctx         context.Context
	ctxCancelFn context.CancelFunc
	logger      runtime.Logger
	db          *sql.DB
	nk          runtime.NakamaModule

	tracker Tracker
	metrics Metrics

	// Unlocks by item name

	cacheMu *sync.RWMutex
	cache   map[evr.EvrId]json.RawMessage
}

func NewProfileRegistry(nk runtime.NakamaModule, db *sql.DB, logger runtime.Logger, tracker Tracker, metrics Metrics) *ProfileRegistry {

	profileRegistry := &ProfileRegistry{
		logger:  logger,
		db:      db,
		nk:      nk,
		tracker: tracker,
		metrics: metrics,

		cacheMu: &sync.RWMutex{},
		cache:   make(map[evr.EvrId]json.RawMessage),
	}

	return profileRegistry
}

func (r *ProfileRegistry) GetCached(ctx context.Context, xpid evr.EvrId) (json.RawMessage, error) {
	r.cacheMu.RLock()
	defer r.cacheMu.RUnlock()
	if p, ok := r.cache[xpid]; ok {
		return p, nil
	}
	return nil, nil
}

func (r *ProfileRegistry) CacheProfile(ctx context.Context, p evr.ServerProfile) error {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	data, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("failed to marshal profile: %w", err)
	}

	r.cache[p.EvrID] = json.RawMessage(data)
	return nil
}

func (r *ProfileRegistry) PurgeProfile(xpid evr.EvrId) {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	delete(r.cache, xpid)
}

func NewUserServerProfile(ctx context.Context, db *sql.DB, params *SessionParameters) (*evr.ServerProfile, error) {

	cosmetics := make(map[string]map[string]bool)
	for m, c := range cosmeticDefaults(params.accountMetadata.EnableAllCosmetics) {
		cosmetics[m] = make(map[string]bool, len(c))
		for k := range c {
			cosmetics[m][k] = true
		}
	}

	var wallet map[string]int64
	if err := json.Unmarshal([]byte(params.accountMetadata.account.Wallet), &wallet); err != nil {
		return nil, fmt.Errorf("failed to unmarshal wallet: %w", err)
	}

	// Convert wallet items into cosmetics
	for k, v := range wallet {
		if v <= 0 {
			continue
		}
		if k, ok := strings.CutPrefix("cosmetics:", k); ok {
			if k, v, ok := strings.Cut(k, ":"); ok {
				if _, ok := cosmetics[k]; !ok {
					cosmetics[k] = make(map[string]bool)
				}
				cosmetics[k][v] = true
			}
		}
	}

	stats, err := LeaderboardsUserTabletStatisticsGet(ctx, db, params.accountMetadata.account.User.Id, []evr.Symbol{evr.ModeArenaPublic, evr.ModeCombatPublic}, "alltime")
	if err != nil {
		return nil, fmt.Errorf("failed to get user tablet statistics: %w", err)
	}

	var developerFeatures *evr.DeveloperFeatures
	if params.accountMetadata.DisableAFKTimeout {
		developerFeatures = &evr.DeveloperFeatures{
			DisableAfkTimeout: true,
		}
	}

	loadout := params.accountMetadata.GetEquippedCosmetics()

	return &evr.ServerProfile{
		DisplayName:       params.accountMetadata.GetActiveGroupDisplayName(),
		EvrID:             params.xpID,
		SchemaVersion:     4,
		PublisherLock:     params.publisherLock,
		LobbyVersion:      params.lobbyVersion,
		PurchasedCombat:   1,
		LoginTime:         time.Now().UTC().Unix(),
		UpdateTime:        params.accountMetadata.account.User.UpdateTime.AsTime().UTC().Unix(),
		CreateTime:        params.accountMetadata.account.User.CreateTime.AsTime().UTC().Unix(),
		Statistics:        stats,
		UnlockedCosmetics: cosmetics,
		EquippedCosmetics: evr.EquippedCosmetics{
			Number:     int(loadout.JerseyNumber),
			NumberBody: int(loadout.JerseyNumber),
			Instances: evr.CosmeticInstances{
				Unified: evr.UnifiedCosmeticInstance{
					Slots: loadout.Loadout,
				},
			},
		},
		Social: evr.ServerSocial{
			Channel: evr.GUID(params.accountMetadata.GetActiveGroupID()),
		},
		DeveloperFeatures: developerFeatures,
	}, nil
}

func NewClientProfile(ctx context.Context, db *sql.DB, nk runtime.NakamaModule, params *SessionParameters) (*evr.ClientProfile, error) {
	// Load friends to get blocked (ghosted) players
	muted := make([]evr.EvrId, 0)
	ghosted := make([]evr.EvrId, 0)
	if m := params.accountMetadata.GetMuted(); len(m) > 0 {
		muted = append(muted, m...)
	}
	if g := params.accountMetadata.GetGhosted(); len(g) > 0 {
		ghosted = append(ghosted, g...)
	}
	combatLoadout := params.accountMetadata.GetCombatLoadout()
	return &evr.ClientProfile{
		ModifyTime:         time.Now().UTC().Unix(),
		DisplayName:        params.accountMetadata.GetActiveGroupDisplayName(),
		EvrID:              params.xpID,
		TeamName:           params.accountMetadata.GetTeamName(),
		CombatWeapon:       combatLoadout.CombatWeapon,
		CombatGrenade:      combatLoadout.CombatGrenade,
		CombatDominantHand: combatLoadout.CombatDominantHand,
		CombatAbility:      combatLoadout.CombatAbility,
		MutedPlayers: evr.Players{
			Players: muted,
		},
		GhostedPlayers: evr.Players{
			Players: ghosted,
		},
		LegalConsents: evr.LegalConsents{
			PointsPolicyVersion: 1,
			EulaVersion:         1,
			GameAdminVersion:    1,
			SplashScreenVersion: 2,
		},
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
		Customization: evr.Customization{
			BattlePassSeasonPoiVersion: 1,
			NewUnlocksPoiVersion:       1,
			StoreEntryPoiVersion:       1,
			ClearNewUnlocksVersion:     1,
		},
		Social: evr.ClientSocial{
			CommunityValuesVersion: 1,
			SetupVersion:           1,
			Channel:                evr.GUID(params.accountMetadata.GetActiveGroupID()),
		},
		NewUnlocks: []int64{}, // This could pull from the wallet ledger
	}, nil
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
	newLoadout.DecalBack = "rwd_decalback_default"

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
		case "decal", "decalback":
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

func cosmeticDefaults(enableAll bool) map[string]map[string]struct{} {

	// Return all but the restricted and blocked cosmetics
	cosmetics := make(map[string]map[string]struct{})

	structs := map[string]interface{}{
		"arena":  evr.ArenaUnlocks{},
		"combat": evr.CombatUnlocks{},
	}
	for m, t := range structs {

		v := reflect.ValueOf(t)
		if v.Kind() == reflect.Ptr {
			v = v.Elem()
		}

		cosmetics[m] = make(map[string]struct{})

		for i := 0; i < v.NumField(); i++ {

			tag := v.Type().Field(i).Tag.Get("validate")
			disabled := strings.Contains(tag, "restricted") || strings.Contains(tag, "blocked")

			if !disabled || enableAll {
				k := v.Type().Field(i).Name
				cosmetics[m][k] = struct{}{}
			}

		}
	}
	return cosmetics
}

func (r *ProfileRegistry) ValidateArenaUnlockByName(i interface{}, itemName string) (bool, error) {
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

func (r *ProfileRegistry) retrieveStarterLoadout(ctx context.Context) (evr.CosmeticLoadout, error) {
	defaultLoadout := evr.DefaultCosmeticLoadout()
	// Retrieve a random starter loadout from storage
	ids, _, err := r.nk.StorageList(ctx, uuid.Nil.String(), uuid.Nil.String(), CosmeticLoadoutCollection, 100, "")
	if err != nil {
		return defaultLoadout, fmt.Errorf("failed to list objects: %w", err)
	}
	if len(ids) == 0 {
		return defaultLoadout, nil
	}
	// Pick a random id
	obj := ids[rand.Intn(len(ids))]
	loadout := StoredCosmeticLoadout{}
	if err = json.Unmarshal([]byte(obj.Value), &loadout); err != nil {
		return defaultLoadout, fmt.Errorf("error unmarshalling client profile: %w", err)
	}
	return loadout.Loadout, nil
}

type StoredCosmeticLoadout struct {
	LoadoutID string              `json:"loadout_id"`
	Loadout   evr.CosmeticLoadout `json:"loadout"`
	UserID    string              `json:"user_id"` // the creator
}

func LeaderboardsUserTabletStatisticsGet(ctx context.Context, db *sql.DB, userID string, modes []evr.Symbol, resetSchedule string) (map[string]map[string]evr.MatchStatistic, error) {
	if len(modes) == 0 {
		return nil, nil
	}
	query := `
	SELECT leaderboard_id, operator, score, subscore
 	FROM leaderboard_record lr, leaderboard l
 		WHERE lr.leaderboard_id = l.id
   		AND owner_id = $1
		AND (%s)
	`
	params := make([]interface{}, 0, len(modes)+1)

	params = append(params, userID)

	likes := make([]string, 0, len(modes))
	for i, mode := range modes {
		likes = append(likes, fmt.Sprintf(" l.id LIKE $%d", i+2))
		params = append(params, fmt.Sprintf("%s:%%:%s", mode.String(), resetSchedule))
	}

	query = fmt.Sprintf(query, strings.Join(likes, " OR "))

	rows, err := db.QueryContext(ctx, query, params...)
	if err != nil {
		if err == sql.ErrNoRows {
			return map[string]map[string]evr.MatchStatistic{
				"arena": {
					"Level": evr.MatchStatistic{
						Operator: "add",
						Value:    1,
					},
				},
				"combat": {
					"Level": evr.MatchStatistic{
						Operator: "add",
						Value:    1,
					},
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to query leaderboard records: %w", err)
	}

	records := make(map[string]map[string]evr.MatchStatistic) // map[mode]map[statName]MatchStatistic

	var dbLeaderboardID string
	var dbOperator int
	var dbScore int64
	var dbSubscore int64

	defer rows.Close()

	for rows.Next() {

		err = rows.Scan(&dbLeaderboardID, &dbOperator, &dbScore, &dbSubscore)
		if err != nil {

			return nil, err
		}
		s := strings.SplitN(dbLeaderboardID, ":", 3)
		if len(s) != 3 {
			return nil, fmt.Errorf("invalid leaderboard ID: %s", dbLeaderboardID)
		}
		mode, statName := s[0], s[1]

		var op string
		switch dbOperator {
		case LeaderboardOperatorSet:
			op = "set"
		case LeaderboardOperatorBest:
			op = "max"
		case LeaderboardOperatorIncrement:
			op = "add"
		}

		var value any
		if dbSubscore == 0 {
			value = dbScore
		} else {
			value, _ = strconv.ParseFloat(fmt.Sprintf("%d.%d", dbScore, dbSubscore), 64)
		}

		if _, ok := records[dbLeaderboardID]; !ok {
			records[mode] = make(map[string]evr.MatchStatistic)
		}

		records[mode][statName] = evr.MatchStatistic{
			Operator: op,
			Value:    value,
			Count:    1,
		}
	}

	return records, nil
}
