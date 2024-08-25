package server

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/intinig/go-openskill/types"
	"github.com/samber/lo"
)

type GameProfile interface {
	GetVersion() string
	SetLogin(login evr.LoginProfile)
	SetClient(client evr.ClientProfile)
	SetServer(server evr.ServerProfile)
	GetServer() evr.ServerProfile
	GetClient() evr.ClientProfile
	GetLogin() evr.LoginProfile
	GetChannel() uuid.UUID
	SetChannel(c evr.GUID)
	UpdateDisplayName(displayName string)
	UpdateUnlocks(unlocks evr.UnlockedCosmetics) error
	IsStale() bool
	SetStale()
}

type GameProfileData struct {
	Login           evr.LoginProfile               `json:"login"`
	Client          evr.ClientProfile              `json:"client"`
	Server          evr.ServerProfile              `json:"server"`
	Rating          types.Rating                   `json:"rating"`
	EarlyQuits      EarlyQuitStatistics            `json:"early_quit"`
	CosmeticPresets map[string]evr.CosmeticLoadout `json:"cosmetic_presets"`
	Version         string                         // The version of the profile from the DB
	Stale           bool                           // Whether the profile is stale and needs to be updated
}

type AccountProfile struct {
	account  *api.Account
	metadata *AccountMetadata
	evrID    evr.EvrId
}

func GetEVRAccount(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID uuid.UUID, evrID *evr.EvrId) (*AccountProfile, error) {
	account, err := nk.AccountGetId(ctx, userID.String())
	if err != nil || account == nil {
		return nil, fmt.Errorf("failed to get account: %w", err)
	}

	if evrID == nil {
		// Get the Latest EVR-ID from the storage objects
		records, err := GetEVRRecords(ctx, logger, nk, account.GetUser().GetId())
		if err != nil {
			return nil, fmt.Errorf("failed to get evr records: %w", err)
		}
		if len(records) == 0 {
			return nil, fmt.Errorf("no evr records found")
		}
		// Get the latest record
		var latest EVRLoginRecord
		for _, record := range records {
			if record.UpdateTime.After(latest.UpdateTime) {
				latest = record
			}
		}
		if latest.EvrID == evr.EvrIdNil {
			return nil, fmt.Errorf("no evr id found")
		}
		evrID = &latest.EvrID
	}

	return NewAccountProfile(ctx, account, *evrID), nil
}

func NewAccountProfile(ctx context.Context, account *api.Account, evrID evr.EvrId) *AccountProfile {
	metadata := &AccountMetadata{}
	if err := json.Unmarshal([]byte(account.User.GetMetadata()), metadata); err != nil {
		metadata = &AccountMetadata{}
	}

	return &AccountProfile{
		account:  account,
		metadata: metadata,
	}
}

func (p *AccountProfile) GetAccount() *api.Account {
	return p.account
}

func (p *AccountProfile) GetCreateTime() time.Time {
	return p.account.GetUser().GetCreateTime().AsTime()
}

func (p *AccountProfile) GetDisplayName() string {
	if p.metadata.DisplayNameOverride != "" {
		return p.metadata.DisplayNameOverride
	}
	return p.account.User.GetDisplayName()
}

func (p *AccountProfile) GetEvrID() evr.EvrId {
	return p.evrID
}

func (p *AccountProfile) GetCosmeticLoadout() evr.CosmeticLoadout {
	return p.metadata.Cosmetics.Loadout
}

func (p *AccountProfile) GetJerseyNumber() int64 {
	return p.metadata.Cosmetics.JerseyNumber
}

func (p *AccountProfile) GetServerProfile() evr.ServerProfile {
	return evr.ServerProfile{
		SchemaVersion: 4,
		CreateTime:    p.GetCreateTime().UTC().Unix(),
		DisplayName:   p.GetDisplayName(),
		EquippedCosmetics: evr.EquippedCosmetics{
			Number: p.GetJerseyNumber(),
			Instances: evr.CosmeticInstances{
				Unified: evr.UnifiedCosmeticInstance{
					Slots: p.GetCosmeticLoadout(),
				},
			},
		},
	}
}

func NewGameProfile(login evr.LoginProfile, client evr.ClientProfile, server evr.ServerProfile, version string) GameProfileData {
	return GameProfileData{
		Login:   login,
		Client:  client,
		Server:  server,
		Version: version,
	}
}

func (p *GameProfileData) GetVersion() string {
	return p.Version
}

func (p *GameProfileData) SetLogin(login evr.LoginProfile) {
	p.Login = login
	p.SetStale()
}

func (p *GameProfileData) SetClient(client evr.ClientProfile) {
	p.Client = client
	p.SetStale()
}

func (p *GameProfileData) SetServer(server evr.ServerProfile) {
	p.Server = server
	p.SetStale()
}

func (p *GameProfileData) SetEarlyQuitStatistics(stats EarlyQuitStatistics) {
	p.EarlyQuits = stats
	p.SetStale()
}

func (p *GameProfileData) GetServer() evr.ServerProfile {
	return p.Server
}

func (p *GameProfileData) GetClient() evr.ClientProfile {
	return p.Client
}

func (p *GameProfileData) GetLogin() evr.LoginProfile {
	return p.Login
}

func (p *GameProfileData) GetEarlyQuitStatistics() EarlyQuitStatistics {
	return p.EarlyQuits
}

func (p *GameProfileData) GetRating() types.Rating {
	if p.Rating.Mu == 0 || p.Rating.Sigma == 0 {
		return NewDefaultRating()
	}
	return p.Rating
}

func (p *GameProfileData) SetRating(rating types.Rating) {
	p.Rating = rating
	p.SetStale()
}

func (p *GameProfileData) SetEvrID(evrID evr.EvrId) {
	if p.Server.EvrID == evrID && p.Client.EvrID == evrID {
		return
	}
	p.Server.EvrID = evrID
	p.Client.EvrID = evrID
	p.SetStale()

}

func (p *GameProfileData) GetEvrID() evr.EvrId {
	return p.Server.EvrID
}

func (p *GameProfileData) GetChannel() uuid.UUID {
	return uuid.UUID(p.Server.Social.Channel)
}

func (p *GameProfileData) SetChannel(c evr.GUID) {
	if p.Server.Social.Channel == c && p.Client.Social.Channel == c {
		return
	}
	p.Server.Social.Channel = c
	p.Client.Social.Channel = p.Server.Social.Channel
	p.SetStale()
}

func (p *GameProfileData) UpdateDisplayName(displayName string) {
	if p.Server.DisplayName == displayName && p.Client.DisplayName == displayName {
		return
	}
	p.Server.DisplayName = displayName
	p.Client.DisplayName = displayName
	p.SetStale()
}

func (p *GameProfileData) ExpireStatistics(dailyAge time.Duration, weeklyAge time.Duration) {
	updated := false
	for t := range p.Server.Statistics {
		if t == "arena" || t == "combat" {
			continue
		}
		if strings.HasPrefix(t, "daily_") {
			// Parse the date
			date, err := time.Parse("2006_01_02", strings.TrimPrefix(t, "daily_"))
			// Keep anything less than 48 hours old
			if err == nil && time.Since(date) < dailyAge {
				continue
			}
		} else if strings.HasPrefix(t, "weekly_") {
			// Parse the date
			date, err := time.Parse("2006_01_02", strings.TrimPrefix(t, "weekly_"))
			// Keep anything less than 2 weeks old
			if err == nil && time.Since(date) < weeklyAge {
				continue
			}
		}
		delete(p.Server.Statistics, t)
		updated = true
	}
	if updated {
		p.SetStale()
	}
}

func (p *GameProfileData) SetStale() {
	p.Stale = true
	p.Server.UpdateTime = time.Now().UTC().Unix()
	p.Client.ModifyTime = time.Now().UTC().Unix()
}

func (p *GameProfileData) IsStale() bool {
	return p.Stale
}
func (p *GameProfileData) DisableAFKTimeout(enable bool) {

	if enable {
		if p.Server.DeveloperFeatures == nil {
			p.Server.DeveloperFeatures = &evr.DeveloperFeatures{
				DisableAfkTimeout: true,
			}
			p.SetStale()
			return
		}
		if p.Server.DeveloperFeatures.DisableAfkTimeout {
			return
		}
		p.Server.DeveloperFeatures.DisableAfkTimeout = true
		p.SetStale()
		return
	}
	if p.Server.DeveloperFeatures == nil {
		return
	}
	if !p.Server.DeveloperFeatures.DisableAfkTimeout {
		return
	}
	p.Server.DeveloperFeatures.DisableAfkTimeout = false
	p.SetStale()
}

func (r *GameProfileData) UpdateUnlocks(unlocks evr.UnlockedCosmetics) error {
	// Validate the unlocks
	/*
		err := ValidateUnlocks(unlocks)
		if err != nil {
			return fmt.Errorf("failed to validate unlocks: %w", err)
		}
	*/
	current := r.Server.UnlockedCosmetics.ToMap()
	updated := unlocks.ToMap()
	newUnlocks := make([]int64, 0, 10)
	for game, unlocks := range updated {
		curUnlocks := current[game]
		for item, u := range unlocks {
			if u && (curUnlocks == nil || curUnlocks[item] != u) {
				newUnlocks = append(newUnlocks, int64(evr.ToSymbol(item)))
			}
		}
	}
	if len(newUnlocks) > 0 {
		r.Client.NewUnlocks = append(r.Client.NewUnlocks, newUnlocks...)
		r.SetStale()
		//r.Client.Customization.NewUnlocksPoiVersion += 1
	}
	if r.Server.UnlockedCosmetics != unlocks {
		r.Server.UnlockedCosmetics = unlocks
		r.SetStale()
	}
	return nil
}

func (r *GameProfileData) TriggerCommunityValues() {
	r.Client.Social.CommunityValuesVersion = 0
	r.SetStale()
}

func generateDefaultLoadoutMap() map[string]string {
	return evr.DefaultCosmeticLoadout().ToMap()
}

func (r *ProfileRegistry) GetFieldByJSONProperty(i interface{}, itemName string) (bool, error) {
	// Lookup the field name by it's item name (json key)
	fieldName, found := r.unlocksByItemName[itemName]
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

func (r *ProfileRegistry) UpdateEquippedItem(profile *GameProfileData, category string, name string) error {
	// Get the current profile.

	unlocksArena := profile.Server.UnlockedCosmetics.Arena
	unlocksCombat := profile.Server.UnlockedCosmetics.Combat

	// Validate that this user has the item unlocked.
	unlocked, err := r.GetFieldByJSONProperty(unlocksArena, name)
	if err != nil {
		// Check if it is a combat unlock
		unlocked, err = r.GetFieldByJSONProperty(unlocksCombat, name)
		if err != nil {
			return fmt.Errorf("failed to validate unlock: %w", err)
		}
	}
	if !unlocked {
		return nil
	}

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

	s := &profile.Server.EquippedCosmetics.Instances.Unified.Slots

	// Exact mappings
	exactmap := map[string]*string{
		"emissive_default":      &s.Emissive,
		"rwd_decalback_default": &s.Pip,
	}

	if val, ok := exactmap[name]; ok {
		*val = name
	} else {

		switch category {
		case "emote":
			s.Emote = name
			s.SecondEmote = name
		case "decal", "decalback":
			s.Decal = name
			s.DecalBody = name
		case "tint":
			// Assigning a tint to the alignment will also assign it to the body
			if lo.Contains(alignmentTints["tint_alignment_a"], name) {
				s.TintAlignmentA = name
			} else if lo.Contains(alignmentTints["tint_alignment_b"], name) {
				s.TintAlignmentB = name
			}
			if name != "tint_chassis_default" {
				// Equipping "tint_chassis_default" to heraldry tint causes every heraldry equipped to be pitch black.
				// It seems that the tint being pulled from doesn't exist on heraldry equippables.
				s.Tint = name
			}
			s.TintBody = name

		case "pattern":
			s.Pattern = name
			s.PatternBody = name
		case "chassis":
			s.Chassis = name
		case "bracer":
			s.Bracer = name
		case "booster":
			s.Booster = name
		case "title":
			s.Title = name
		case "tag", "heraldry":
			s.Tag = name
		case "banner":
			s.Banner = name
		case "medal":
			s.Medal = name
		case "goal":
			s.GoalFx = name
		case "emissive":
			s.Emissive = name
		//case "decalback":
		//	fallthrough
		case "pip":
			s.Pip = name
		default:
			r.logger.Warn("Unknown cosmetic category `%s`", category)
			return nil
		}
	}
	// Update the timestamp
	profile.SetStale()
	return nil
}

// Set the user's profile based on their groups
func (r *ProfileRegistry) UpdateEntitledCosmetics(ctx context.Context, userID uuid.UUID, profile *GameProfileData) error {

	// Get the user's groups
	// Check if the user has any groups that would grant them cosmetics
	userGroups, _, err := r.nk.UserGroupsList(ctx, userID.String(), 100, nil, "")
	if err != nil {
		return fmt.Errorf("failed to get user groups: %w", err)
	}

	for i, group := range userGroups {
		// If the user is not a member of the group, don't include it.
		if group.GetState().GetValue() > int32(api.GroupUserList_GroupUser_MEMBER) {
			// Remove the group
			userGroups = append(userGroups[:i], userGroups[i+1:]...)
		}
	}

	isDeveloper := false

	groupNames := make([]string, 0, len(userGroups))
	for _, userGroup := range userGroups {
		group := userGroup.GetGroup()
		name := group.GetName()
		groupNames = append(groupNames, name)
	}

	for _, name := range groupNames {
		switch name {
		case GroupGlobalDevelopers:
			isDeveloper = true

		}
	}

	// Disable Restricted Cosmetics
	enableAll := isDeveloper
	err = SetCosmeticDefaults(&profile.Server, enableAll)
	if err != nil {
		return fmt.Errorf("failed to disable restricted cosmetics: %w", err)
	}

	// Set the user's unlocked cosmetics based on their groups
	unlocked := profile.Server.UnlockedCosmetics
	arena := &unlocked.Arena
	// Set the user's unlocked cosmetics based on their groups
	for _, userGroup := range userGroups {
		group := userGroup.GetGroup()

		if group.LangTag != "entitlement" {
			continue
		}

		name := group.GetName()
		if strings.HasPrefix(name, "VRML ") {
			arena.DecalVRML = true
			arena.EmoteVRMLA = true
		}

		switch name {
		case "VRML Season Preseason":
			arena.TagVRMLPreseason = true
			arena.MedalVRMLPreseason = true

		case "VRML Season 1 Champion":
			arena.MedalVRMLS1Champion = true
			arena.TagVRMLS1Champion = true
			fallthrough
		case "VRML Season 1 Finalist":
			arena.MedalVRMLS1Finalist = true
			arena.TagVRMLS1Finalist = true
			fallthrough
		case "VRML Season 1":
			arena.TagVRMLS1 = true
			arena.MedalVRMLS1 = true

		case "VRML Season 2 Champion":
			arena.MedalVRMLS2Champion = true
			arena.TagVRMLS2Champion = true
			fallthrough
		case "VRML Season 2 Finalist":
			arena.MedalVRMLS2Finalist = true
			arena.TagVRMLS2Finalist = true
			fallthrough
		case "VRML Season 2":
			arena.TagVRMLS2 = true
			arena.MedalVRMLS2 = true

		case "VRML Season 3 Champion":
			arena.MedalVRMLS3Champion = true
			arena.TagVRMLS3Champion = true
			fallthrough
		case "VRML Season 3 Finalist":
			arena.MedalVRMLS3Finalist = true
			arena.TagVRMLS3Finalist = true
			fallthrough
		case "VRML Season 3":
			arena.MedalVRMLS3 = true
			arena.TagVRMLS3 = true

		case "VRML Season 4 Champion":
			arena.TagVRMLS4Champion = true
			arena.MedalVRMLS4Champion = true
			fallthrough
		case "VRML Season 4 Finalist":
			arena.TagVRMLS4Finalist = true
			arena.MedalVRMLS4Finalist = true
			fallthrough
		case "VRML Season 4":
			arena.TagVRMLS4 = true
			arena.MedalVRMLS4 = true
		case "VRML Season 5 Champion":
			arena.TagVRMLS5Champion = true
			fallthrough
		case "VRML Season 5 Finalist":
			arena.TagVRMLS5Finalist = true
			fallthrough
		case "VRML Season 5":
			arena.TagVRMLS5 = true

		case "VRML Season 6 Champion":
			arena.TagVRMLS6Champion = true
			fallthrough

		case "VRML Season 6 Finalist":
			arena.TagVRMLS6Finalist = true
			fallthrough
		case "VRML Season 6":
			arena.TagVRMLS6 = true

		case "VRML Season 7 Champion":
			arena.TagVRMLS7Champion = true
			fallthrough
		case "VRML Season 7 Finalist":
			arena.TagVRMLS7Finalist = true
			fallthrough
		case "VRML Season 7":
			arena.TagVRMLS7 = true

		case GroupGlobalDevelopers:
			arena.TagDeveloper = true
			fallthrough
		case GroupGlobalModerators:
			arena.TagGameAdmin = true

		case GroupGlobalTesters:
			arena.DecalOneYearA = true
			arena.RWDEmoteGhost = true
		}
	}

	// Unlock if the user has been a quest user.
	if strings.Contains(profile.Login.SystemInfo.HeadsetType, "Quest") {
		arena.DecalQuestLaunchA = true
		arena.PatternQuestA = true
	}

	// Update the unlocks (and the client profile's newunlocks list)
	err = profile.UpdateUnlocks(unlocked)
	if err != nil {
		return fmt.Errorf("failed to update unlocks: %w", err)
	}
	account, err := r.nk.AccountGetId(ctx, userID.String())
	if err != nil {
		return fmt.Errorf("failed to get account: %w", err)
	}
	md := AccountMetadata{}
	if err := json.Unmarshal([]byte(account.User.GetMetadata()), &md); err != nil {
		return fmt.Errorf("failed to unmarshal metadata: %w", err)
	}

	profile.DisableAFKTimeout(md.DisableAFKTimeout)

	/*
		if err := enforceLoadoutEntitlements(r.logger, &profile.Server.EquippedCosmetics.Instances.Unified.Slots, &profile.Server.UnlockedCosmetics, r.defaults); err != nil {
			return fmt.Errorf("failed to set loadout entitlement: %w", err)
		}
	*/
	return nil
}

func enforceLoadoutEntitlements(logger runtime.Logger, loadout *evr.CosmeticLoadout, unlocked *evr.UnlockedCosmetics, defaults map[string]string) error {
	unlockMap := unlocked.ToMap()

	loadoutMap := loadout.ToMap()

	for k, v := range loadoutMap {
		for _, unlocks := range unlockMap {
			if _, found := unlocks[v]; !found {
				logger.Warn("User has item equip that does not exist: %s: %s", k, v)
				loadoutMap[k] = defaults[k]
			} else if !unlocks[v] {
				logger.Warn("User does not have entitlement to item: %s: %s", k, v)
			}
		}
	}
	loadout.FromMap(loadoutMap)
	return nil
}

func createUnlocksFieldByKey() map[string]string {
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
	return unlocks
}

func ValidateUnlocks(unlocks any) error {
	err := validate.Struct(unlocks)
	if err == nil {
		return nil
	}
	return err
}

// SetCosmeticDefaults sets all the restricted cosmetics to false.
func SetCosmeticDefaults(s *evr.ServerProfile, enableAll bool) error {
	// Set all the VRML cosmetics to false

	structs := []interface{}{&s.UnlockedCosmetics.Arena, &s.UnlockedCosmetics.Combat}
	for _, t := range structs {
		v := reflect.ValueOf(t)
		if v.Kind() == reflect.Ptr {
			v = v.Elem()
		}

		for i := 0; i < v.NumField(); i++ {
			if enableAll {
				v.Field(i).Set(reflect.ValueOf(true))
			} else {
				tag := v.Type().Field(i).Tag.Get("validate")
				disabled := strings.Contains(tag, "restricted") || strings.Contains(tag, "blocked")
				if v.Field(i).CanSet() {
					v.Field(i).Set(reflect.ValueOf(!disabled))
				}
			}
		}
	}
	return nil
}

func (p *GameProfileData) saveCosmeticPreset(profileName string) {
	if p.CosmeticPresets == nil {
		p.CosmeticPresets = make(map[string]evr.CosmeticLoadout)
	}
	p.CosmeticPresets[profileName] = p.Server.EquippedCosmetics.Instances.Unified.Slots
	p.SetStale()
}

func (p *GameProfileData) loadCosmeticPreset(profileName string) {
	p.Server.EquippedCosmetics.Instances.Unified.Slots = p.CosmeticPresets[profileName]
	p.SetStale()
}

func (p *GameProfileData) deleteCosmeticPreset(profileName string) {
	delete(p.CosmeticPresets, profileName)
	p.SetStale()
}
