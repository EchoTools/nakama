package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/samber/lo"
	"go.uber.org/zap"
)

type GameProfile interface {
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
}

type GameProfileData struct {
	Login     evr.LoginProfile  `json:"login"`
	Client    evr.ClientProfile `json:"client"`
	Server    evr.ServerProfile `json:"server"`
	timestamp time.Time         // The time when the profile was last retrieved

}

type AccountProfile struct {
	account  *api.Account
	metadata *AccountUserMetadata
	evrID    evr.EvrID
}

func GetEVRAccount(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID uuid.UUID, evrID *evr.EvrID) (*AccountProfile, error) {
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
		if latest.EvrID == evr.EvrIDNil {
			return nil, fmt.Errorf("no evr id found")
		}
		evrID = &latest.EvrID
	}

	return NewAccountProfile(ctx, account, *evrID), nil

}
func NewAccountProfile(ctx context.Context, account *api.Account, evrID evr.EvrID) *AccountProfile {
	metadata := &AccountUserMetadata{}
	if err := json.Unmarshal([]byte(account.User.GetMetadata()), metadata); err != nil {
		metadata = &AccountUserMetadata{}
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

func (p *AccountProfile) GetEvrID() evr.EvrID {
	return p.evrID
}

func NewGameProfile(login evr.LoginProfile, client evr.ClientProfile, server evr.ServerProfile) GameProfileData {
	return GameProfileData{
		Login:  login,
		Client: client,
		Server: server,
	}
}

func (p *GameProfileData) SetLogin(login evr.LoginProfile) {
	p.Login = login
}

func (p *GameProfileData) SetClient(client evr.ClientProfile) {
	p.Client = client
}

func (p *GameProfileData) SetServer(server evr.ServerProfile) {
	p.Server = server
	p.Server.UpdateTime = time.Now().UTC().Unix()
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

func (p *GameProfileData) SetEvrID(evrID evr.EvrID) {
	p.Server.EvrID = evrID
	p.Client.EvrID = evrID
}

func (p *GameProfileData) GetChannel() uuid.UUID {
	return uuid.UUID(p.Server.Social.Channel)
}

func (p *GameProfileData) SetChannel(c evr.GUID) {
	p.Server.Social.Channel = c
	p.Client.Social.Channel = p.Server.Social.Channel
}

func (p *GameProfileData) UpdateDisplayName(displayName string) {
	p.Server.DisplayName = displayName
	p.Client.DisplayName = displayName
	p.Server.UpdateTime = time.Now().UTC().Unix()
	p.Client.ModifyTime = time.Now().UTC().Unix()

}

func (p *GameProfileData) DisableAFKTimeout(enable bool) {
	if enable {
		if p.Server.DeveloperFeatures == nil {
			p.Server.DeveloperFeatures = &evr.DeveloperFeatures{
				DisableAfkTimeout: true,
			}
		}
	} else {
		p.Server.DeveloperFeatures = nil
	}
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
		r.Client.ModifyTime = time.Now().UTC().Unix()
		//r.Client.Customization.NewUnlocksPoiVersion += 1
	}

	r.Server.UnlockedCosmetics = unlocks
	r.Server.UpdateTime = time.Now().UTC().Unix()
	return nil
}

func (r *GameProfileData) TriggerCommunityValues() {
	r.Client.Social.CommunityValuesVersion = 0
	r.Client.ModifyTime = time.Now().UTC().Unix()
}

// ProfileRegistry is a registry of user evr profiles.
type ProfileRegistry struct {
	ctx         context.Context
	ctxCancelFn context.CancelFunc
	logger      runtime.Logger
	db          *sql.DB
	nk          runtime.NakamaModule

	discordRegistry DiscordRegistry

	// Unlocks by item name
	unlocksByItemName map[string]string

	// Profiles by user ID
	storeMu  sync.Mutex
	profiles map[string]GameProfileData // map[userID]GameProfileData
	cache    *LocalProfileCache
	// Load out default items
	defaults map[string]string
}

func NewProfileRegistry(nk runtime.NakamaModule, db *sql.DB, logger runtime.Logger, discordRegistry DiscordRegistry) *ProfileRegistry {
	ctx, cancel := context.WithCancel(context.Background())

	unlocksByFieldName := createUnlocksFieldByKey()
	registry := &ProfileRegistry{
		ctx:             ctx,
		ctxCancelFn:     cancel,
		logger:          logger,
		db:              db,
		nk:              nk,
		discordRegistry: discordRegistry,

		profiles: make(map[string]GameProfileData, 200),

		unlocksByItemName: unlocksByFieldName,
		defaults:          generateDefaultLoadoutMap(),
	}

	return registry
}

func (r *ProfileRegistry) Stop() {
	select {
	case <-r.ctx.Done():
		return
	default:
		r.ctxCancelFn()
	}
}

func (r *ProfileRegistry) load(userID uuid.UUID) (GameProfileData, bool) {
	r.storeMu.Lock()
	p, ok := r.profiles[userID.String()]
	r.storeMu.Unlock()
	return p, ok

}

func (r *ProfileRegistry) store(userID uuid.UUID, profile GameProfileData) {
	r.storeMu.Lock()
	r.profiles[userID.String()] = profile
	r.storeMu.Unlock()
}

func generateDefaultLoadoutMap() map[string]string {
	return evr.DefaultCosmeticLoadout().ToMap()
}

// Load the user's profile from memory (or storage if not found)
func (r *ProfileRegistry) Load(userID uuid.UUID, evrID evr.EvrID) (profile GameProfileData, created bool) {
	var found bool
	var err error
	ctx, cancel := context.WithTimeout(r.ctx, 2*time.Second)
	defer cancel()

	profile, found = r.load(userID)
	if !found {
		// try to load the profile from storage
		profile, err = r.retrieve(ctx, userID)
		if err != nil {
			r.logger.Debug("failed to load profile for %s: %s", userID.String(), err.Error())
			// try the system profile
			profile, err = r.retrieve(ctx, uuid.Nil)
			if err != nil {
				r.logger.Error("failed to load system profile for %s: %s", userID.String(), err.Error())
				// the profile is missing, just use a default
				profile = GameProfileData{
					Client: evr.NewClientProfile(),
					Server: evr.NewServerProfile(),
				}
				err := r.save(ctx, uuid.Nil, &profile)
				if err != nil {
					r.logger.Warn("failed to save default profile: %s", err.Error())
				}
			}
		}
	}
	r.store(userID, profile)
	if evrID != evr.EvrIDNil {
		profile.SetEvrID(evrID)
	}

	return profile, true
}

var (
	ErrProfileNotFound  = fmt.Errorf("profile not found")
	ErrProfileNotLoaded = fmt.Errorf("profile not loaded")
	ErrProfileStale     = fmt.Errorf("profile is stale")
)

// Set the user's profile in memory
func (r *ProfileRegistry) Store(userID uuid.UUID, p GameProfileData) error {
	p.timestamp = time.Now()
	r.store(userID, p)
	return nil
}

// Save the profile from memory (and store it)
func (r *ProfileRegistry) Save(userID uuid.UUID) {
	profile, loaded := r.load(userID)
	if !loaded {
		r.logger.Warn("Profile not in store for %s, it must have already been saved", userID.String())
	}

	r.save(context.Background(), userID, &profile)
}

// Retrieve the user's profile from storage
func (r *ProfileRegistry) retrieve(ctx context.Context, userID uuid.UUID) (profile GameProfileData, err error) {
	uid := userID.String()
	objs, err := r.nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: GameProfileStorageCollection,
			Key:        GameProfileStorageKey,
			UserID:     uid,
		},
	})

	if err != nil || len(objs) == 0 {
		return profile, ErrProfileNotFound
	}
	if err = json.Unmarshal([]byte(objs[0].Value), &profile); err != nil {
		return
	}

	return
}
func (r *ProfileRegistry) save(ctx context.Context, userID uuid.UUID, profile GameProfile) error {

	b, err := json.Marshal(profile)
	if err != nil {
		return err
	}

	_, err = r.nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection: GameProfileStorageCollection,
			Key:        GameProfileStorageKey,
			UserID:     userID.String(),
			Value:      string(b),
		},
	})
	if err != nil {
		return err
	}

	return nil
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

func (r *ProfileRegistry) NewGameProfile() GameProfileData {
	profile, err := r.retrieve(r.ctx, uuid.Nil)
	if err != nil {
		profile = GameProfileData{
			Client: evr.NewClientProfile(),
			Server: evr.NewServerProfile(),
		}
	}
	// Apply a community "designed" loadout to the new user
	loadout, err := r.retrieveStarterLoadout(r.ctx)
	if err != nil {
		r.logger.Warn("Failed to retrieve starter loadout: %s", err.Error())
		profile.Server.EquippedCosmetics.Instances.Unified.Slots = loadout
	}
	return profile
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

	// Equip the item
	s := &profile.Server.EquippedCosmetics.Instances.Unified.Slots
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

	// Update the timestamp
	now := time.Now().UTC().Unix()
	profile.Server.UpdateTime = now

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
	profile.DisableAFKTimeout(isDeveloper)

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

type StoredCosmeticLoadout struct {
	LoadoutID string              `json:"loadout_id"`
	Loadout   evr.CosmeticLoadout `json:"loadout"`
	UserID    string              `json:"user_id"` // the creator
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

func (r *ProfileRegistry) ValidateArenaUnlockByName(i interface{}, itemName string) (bool, error) {
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

func (r *ProfileRegistry) GetSessionProfile(ctx context.Context, session *sessionWS, loginProfile evr.LoginProfile, evrID evr.EvrID) (GameProfileData, error) {
	logger := session.logger.With(zap.String("evr_id", evrID.String()))

	p, ok := r.Load(session.userID, evrID)
	if !ok {
		// Create a new profile
		p = r.NewGameProfile()
	}
	p.Login = loginProfile
	p.Server.PublisherLock = p.Login.PublisherLock
	p.Server.LobbyVersion = p.Login.LobbyVersion

	if p.Server.Statistics == nil || p.Server.Statistics["arena"] == nil || p.Server.Statistics["combat"] == nil {
		p.Server.Statistics = evr.NewStatistics()
	}

	// Update the account
	if err := r.discordRegistry.UpdateAccount(ctx, session.userID); err != nil {
		logger.Warn("Failed to update account", zap.Error(err))
	}

	// Apply any unlocks based on the user's groups
	if err := r.UpdateEntitledCosmetics(ctx, session.userID, &p); err != nil {
		return p, fmt.Errorf("failed to update entitled cosmetics: %w", err)
	}

	p.Server.CreateTime = time.Date(2023, 10, 31, 0, 0, 0, 0, time.UTC).Unix()
	p.Server.LoginTime = time.Now().UTC().Unix()
	p.Server.UpdateTime = time.Now().UTC().Unix()

	go func() {
		r.Store(session.userID, p)
		<-session.ctx.Done()
		r.Save(session.userID)
	}()

	return p, nil
}
func (r *ProfileRegistry) UpdateClientProfile(ctx context.Context, logger *zap.Logger, session *sessionWS, update evr.ClientProfile) (profile GameProfileData, err error) {
	// Get the user's profile
	p, found := r.Load(session.userID, update.EvrID)
	if !found {
		err = fmt.Errorf("failed to load user profile")
		return
	}

	// Validate the client profile.
	// TODO FIXME Validate the profile data
	//if errs := evr.ValidateStruct(request.ClientProfile); errs != nil {
	//	return errFailure(fmt.Errorf("invalid client profile: %s", errs), 400)
	//}

	p.Client = update

	r.Store(session.userID, p)
	return p, nil
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

func (r *ProfileRegistry) LoadProfile(ctx context.Context, logger *zap.Logger, nk runtime.NakamaModule, discordRegistry DiscordRegistry, session *sessionWS, evrID evr.EvrID, groupID uuid.UUID) (GameProfileData, error) {

	// Determine the display name
	displayName, err := SetDisplayNameByChannelBySession(ctx, nk, logger, discordRegistry, session, groupID.String())
	if err != nil {
		logger.Warn("Failed to set display name.", zap.Error(err))
	}

	profile, found := r.Load(session.UserID(), evrID)
	if !found {
		defer session.Close("profile not found", runtime.PresenceReasonUnknown)
		return GameProfileData{}, fmt.Errorf("profile not found: %s", session.UserID())
	}

	profile.UpdateDisplayName(displayName)
	profile.SetEvrID(evrID)

	// Get the most recent past thursday
	serverProfile := profile.GetServer()

	for t := range serverProfile.Statistics {
		if t == "arena" || t == "combat" {
			continue
		}
		if strings.HasPrefix(t, "daily_") {
			// Parse the date
			date, err := time.Parse("2006_01_02", strings.TrimPrefix(t, "daily_"))
			// Keep anything less than 48 hours old
			if err == nil && time.Since(date) < 48*time.Hour {
				continue
			}
		} else if strings.HasPrefix(t, "weekly_") {
			// Parse the date
			date, err := time.Parse("2006_01_02", strings.TrimPrefix(t, "weekly_"))
			// Keep anything less than 2 weeks old
			if err == nil && time.Since(date) < 14*24*time.Hour {
				continue
			}
		}
		delete(serverProfile.Statistics, t)
	}
	return profile, nil
}
