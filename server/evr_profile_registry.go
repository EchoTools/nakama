package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
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

type ProfileRegistry struct {
	ctx         context.Context
	ctxCancelFn context.CancelFunc
	logger      runtime.Logger
	db          *sql.DB
	nk          runtime.NakamaModule

	discordRegistry DiscordRegistry

	profiles *MapOf[uuid.UUID, *GameProfile]

	unlocksByItemName map[string]string
}

type GameProfile struct {
	sync.RWMutex
	Login  *evr.LoginProfile  `json:"login"`
	Client *evr.ClientProfile `json:"client"`
	Server *evr.ServerProfile `json:"server"`
}

func (p *GameProfile) SetLoginProfile(login evr.LoginProfile) {
	p.Lock()
	defer p.Unlock()

}

func (p *GameProfile) SetClientProfile(client evr.ClientProfile) {
	p.Lock()
	defer p.Unlock()
	p.Client = &client
}

func (p *GameProfile) SetServerProfile(server evr.ServerProfile) {
	p.Lock()
	defer p.Unlock()
	p.Server = &server
	p.Server.UpdateTime = time.Now().UTC().Unix()
}

func (p *GameProfile) GetServerProfile() *evr.ServerProfile {
	p.RLock()
	defer p.RUnlock()
	return p.Server
}

func (p *GameProfile) GetClientProfile() *evr.ClientProfile {
	p.RLock()
	defer p.RUnlock()
	return p.Client
}

func (p *GameProfile) GetLoginProfile() *evr.LoginProfile {
	p.RLock()
	defer p.RUnlock()
	return p.Login
}

func (p *GameProfile) GetChannel() uuid.UUID {
	p.RLock()
	defer p.RUnlock()
	return uuid.UUID(p.Client.Social.Group)
}

func (p *GameProfile) UpdateDisplayName(displayName string) {
	p.Lock()
	defer p.Unlock()
	p.Server.DisplayName = displayName
}

func (r *GameProfile) UpdateUnlocks(unlocks any) error {
	// Validate the unlocks
	err := ValidateUnlocks(unlocks)
	if err != nil {
		return fmt.Errorf("failed to validate unlocks: %w", err)
	}

	switch unlocks := unlocks.(type) {
	case *evr.ArenaUnlocks:
		updateUnlocks(r.Server.UnlockedCosmetics.Arena, unlocks)
	case *evr.CombatUnlocks:
		updateUnlocks(r.Server.UnlockedCosmetics.Combat, unlocks)
	default:
		return fmt.Errorf("unknown unlock type: %T", unlocks)
	}
	return nil
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
		profiles:        &MapOf[uuid.UUID, *GameProfile]{},

		unlocksByItemName: unlocksByFieldName,
	}
	registry.checkDefaultProfile()
	return registry
}

func (r *ProfileRegistry) checkDefaultProfile() *GameProfile {
	// Create default profiles under the system user
	userID := uuid.Nil

	profile := r.GetProfile(userID)
	if profile == nil {
		profile = &GameProfile{}
	}
	if profile.Login == nil {
		profile.Login = &evr.LoginProfile{}
	}
	if profile.Client == nil {
		profile.Client = evr.NewClientProfile()
	}
	if profile.Server == nil {
		profile.Server = evr.NewServerProfile()
	}

	r.SetProfile(userID, profile)
	return profile
}

func (r *ProfileRegistry) GetProfile(userID uuid.UUID) *GameProfile {
	var err error
	profile, found := r.profiles.Load(userID)
	if !found {
		// Load the profile from storage
		profile, err = r.loadProfile(context.Background(), userID)
		if err != nil {
			r.logger.Error("failed to load profile", zap.Error(err))
		}
	}

	return profile
}

func (r *ProfileRegistry) SetProfile(userID uuid.UUID, profile *GameProfile) {
	r.profiles.Store(userID, profile)

	// Save the profile to storage
	err := r.storeProfile(context.Background(), userID, profile)
	if err != nil {
		r.logger.Error("failed to save profile", zap.Error(err))
	}
}

// Unload the profile from memory
func (r *ProfileRegistry) Unload(userID uuid.UUID) {
	profile, found := r.profiles.LoadAndDelete(userID)
	if !found {
		r.storeProfile(context.Background(), userID, profile)
	}
}

func (r *ProfileRegistry) GetProfileOrNew(userID uuid.UUID) (profile *GameProfile, created bool) {

	profile = r.GetProfile(userID)

	created = profile == nil || profile.Server == nil

	if profile == nil || profile.Server == nil || profile.Client == nil {
		defaultprofile, err := r.loadProfile(r.ctx, uuid.Nil)
		if err != nil {
			panic("failed to load default profile")
		}
		if profile == nil {
			profile = &GameProfile{}
		}
		if profile.Client == nil {
			profile.Client = defaultprofile.Client
		}
		if profile.Server == nil {
			profile.Server = defaultprofile.Server
			// Apply a community "designed" loadout to the new user
			loadout, err := r.retrieveStarterLoadout(r.ctx)
			if err != nil || loadout == nil {
				r.logger.Warn("Failed to retrieve starter loadout", zap.Error(err))
			} else {
				profile.Server.EquippedCosmetics.Instances.Unified.Slots = *loadout
			}
		}
	}

	if profile.Login == nil {
		profile.Login = &evr.LoginProfile{}
	}

	if created {
		r.SetProfile(userID, profile)
	}

	return profile, created
}

func (r *ProfileRegistry) loadProfile(ctx context.Context, userID uuid.UUID) (*GameProfile, error) {
	uid := userID.String()
	objs, err := r.nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: GameProfileStorageCollection,
			Key:        GameProfileStorageKey,
			UserID:     uid,
		},
	})

	if err != nil || len(objs) == 0 {
		return nil, err
	}

	profile := &GameProfile{}
	if err := json.Unmarshal([]byte(objs[0].Value), profile); err != nil {
		return nil, err
	}

	return profile, nil
}
func (r *ProfileRegistry) storeProfile(ctx context.Context, userID uuid.UUID, profile *GameProfile) error {

	b, err := json.Marshal(profile)
	if err != nil {
		return err
	}

	if _, err := r.nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection: GameProfileStorageCollection,
			Key:        GameProfileStorageKey,
			UserID:     userID.String(),
			Value:      string(b),
		},
	}); err != nil {
		return err
	}

	return nil
}

func (r *ProfileRegistry) LookupUnlockByJSONProperty(i interface{}, itemName string) (bool, error) {
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

func (r *ProfileRegistry) UpdateEquippedItem(ctx context.Context, userID uuid.UUID, category string, name string) error {
	// Get the current profile.
	profile := r.GetProfile(userID)

	profile.Lock()
	defer profile.Unlock()

	slots := profile.Server.EquippedCosmetics.Instances.Unified.Slots
	unlocksArena := profile.Server.UnlockedCosmetics.Arena
	unlocksCombat := profile.Server.UnlockedCosmetics.Combat

	// Validate that this user has the item unlocked.
	if unlocked, err := r.LookupUnlockByJSONProperty(unlocksArena, name); err != nil {
		return fmt.Errorf("failed to validate arena unlock: %w", err)
	} else if !unlocked {
		return nil
	}

	if unlocked, err := r.LookupUnlockByJSONProperty(unlocksCombat, name); err != nil {
		return fmt.Errorf("failed to validate combat unlock: %w", err)
	} else if !unlocked {
		return nil
	}

	// Equip the item
	s := &slots
	itemMap := map[string]*string{
		"secondemote":      &s.SecondEmote,
		"emote":            &s.Emote,
		"decal":            &s.Decal,
		"decal_body":       &s.DecalBody,
		"tint":             &s.Tint,
		"tint_body":        &s.TintBody,
		"pattern":          &s.Pattern,
		"pattern_body":     &s.PatternBody,
		"decalback":        &s.Pip,
		"chassis":          &s.Chassis,
		"bracer":           &s.Bracer,
		"booster":          &s.Booster,
		"title":            &s.Title,
		"tag":              &s.Tag,
		"banner":           &s.Banner,
		"medal":            &s.Medal,
		"goal_fx":          &s.GoalFx,
		"emissive":         &s.Emissive,
		"tent_alignment_a": &s.TintAlignmentA,
		"tint_alignment_b": &s.TintAlignmentB,
		"pip":              &s.Pip,
	}

	if val, ok := itemMap[category]; ok {
		// Special case for "tint" category
		if category == "tint" && name == "tint_chassis_default" {
			return nil
		}
		*val = name
	} else {

		return nil
	}

	// Update the timestamp
	now := time.Now().UTC().Unix()
	profile.Server.UpdateTime = now

	go func() {
		profile.RLock()
		defer profile.RUnlock()
		if err := r.storeProfile(r.ctx, userID, profile); err != nil {
			r.logger.Error("failed to save profile", zap.Error(err))
		}
	}()

	return nil
}

// Set the user's profile based on their groups
func (r *ProfileRegistry) UpdateEntitledCosmetics(ctx context.Context, userID uuid.UUID, profile *GameProfile) error {
	// Get the user's groups
	// Check if the user has any groups that would grant them cosmetics
	userGroups, _, err := r.nk.UserGroupsList(ctx, userID.String(), 100, nil, "")
	if err != nil {
		return fmt.Errorf("failed to get user groups: %w", err)
	}

	for i, group := range userGroups {
		// If the user is not a member of the group, remove it
		if group.GetState().GetValue() > int32(api.GroupUserList_GroupUser_MEMBER) {
			// Remove the group
			userGroups = append(userGroups[:i], userGroups[i+1:]...)
		}
	}

	// Set the user's unlocked cosmetics based on their groups
	unlocks := profile.Server.UnlockedCosmetics.Arena
	for _, group := range userGroups {
		name := group.GetGroup().GetName()
		if name[:5] == "VRML" {
			unlocks.DecalVRML = true
			unlocks.EmoteVRMLA = true
		}

		// Set VRML tags and medals based on the user's groups
		unlocks.DecalVRML = true
		unlocks.EmoteVRMLA = true
		switch name {
		default:
			unlocks.DecalVRML = false
			unlocks.EmoteVRMLA = false
		case "VRML Season Preseason":
			unlocks.TagVrmlPreseason = true
			unlocks.MedalVrmlPreseason = true

		case "VRML Season 1 Champion":
			unlocks.MedalVrmlS1Champion = true
			unlocks.TagVrmlS1Champion = true
			fallthrough
		case "VRML Season 1 Finalist":
			unlocks.MedalVrmlS1Finalist = true
			unlocks.TagVrmlS1Finalist = true
			fallthrough
		case "VRML Season 1":
			unlocks.TagVrmlS1 = true
			unlocks.MedalVrmlS1 = true

		case "VRML Season 2 Champion":
			unlocks.MedalVrmlS2Champion = true
			unlocks.TagVrmlS2Champion = true
			fallthrough
		case "VRML Season 2 Finalist":
			unlocks.MedalVrmlS2Finalist = true
			unlocks.TagVrmlS2Finalist = true
			fallthrough
		case "VRML Season 2":
			unlocks.TagVrmlS2 = true
			unlocks.MedalVrmlS2 = true

		case "VRML Season 3 Champion":
			unlocks.MedalVrmlS3Champion = true
			unlocks.TagVrmlS3Champion = true
			fallthrough
		case "VRML Season 3 Finalist":
			unlocks.MedalVrmlS3Finalist = true
			unlocks.TagVrmlS3Finalist = true
			fallthrough
		case "VRML Season 3":
			unlocks.MedalVrmlS3 = true
			unlocks.TagVrmlS3 = true

		case "VRML Season 4 Champion":
			unlocks.TagVrmlS4Champion = true
			fallthrough
		case "VRML Season 4 Finalist":
			unlocks.TagVrmlS4Finalist = true
			fallthrough
		case "VRML Season 4":
			unlocks.TagVrmlS4 = true

		case "VRML Season 5 Champion":
			unlocks.TagVrmlS5Champion = true
			fallthrough
		case "VRML Season 5 Finalist":
			unlocks.TagVrmlS5Finalist = true
			fallthrough
		case "VRML Season 5":
			unlocks.TagVrmlS5 = true

		case "VRML Season 6 Champion":
			unlocks.TagVrmlS6Champion = true
			fallthrough

		case "VRML Season 6 Finalist":
			unlocks.TagVrmlS6Finalist = true
			fallthrough
		case "VRML Season 6":
			unlocks.TagVrmlS6 = true

		case "VRML Season 7 Champion":
			unlocks.TagVrmlS7Champion = true
			fallthrough
		case "VRML Season 7 Finalist":
			unlocks.TagVrmlS7Finalist = true
			fallthrough
		case "VRML Season 7":
			unlocks.TagVrmlS7 = true
		}

		// Other group-based unlocks
		switch name {

		case "Global Developers":
			unlocks.TagDeveloper = true
			profile.Server.DeveloperFeatures = &evr.DeveloperFeatures{
				DisableAfkTimeout: true,
			}
			fallthrough
		case "Global Moderators":
			unlocks.TagGameAdmin = true
			fallthrough
		case "Global Testers":
			unlocks.DecalOneYearA = true
			unlocks.RWDEmoteGhost = true
		}
	}

	if profile.Login != nil {
		// Unlock if the user has been a quest user.
		if strings.Contains(profile.Login.SystemInfo.HeadsetType, "Quest") {
			unlocks.DecalQuestLaunchA = true
			unlocks.PatternQuestA = true
			unlocks.PatternQuestA = true
		}
	}

	return nil
}

type StoredCosmeticLoadout struct {
	LoadoutID string               `json:"loadout_id"`
	Loadout   *evr.CosmeticLoadout `json:"loadout"`
	UserID    string               `json:"user_id"` // the creator
}

func (r *ProfileRegistry) retrieveStarterLoadout(ctx context.Context) (*evr.CosmeticLoadout, error) {
	// Retrieve a random starter loadout from storage
	ids, _, err := r.nk.StorageList(ctx, uuid.Nil.String(), uuid.Nil.String(), CosmeticLoadoutCollection, 100, "")
	if err != nil {
		return nil, fmt.Errorf("failed to list objects: %w", err)
	}
	if len(ids) == 0 {
		return nil, nil
	}

	// Pick a random id
	obj := ids[rand.Intn(len(ids))]
	loadout := &StoredCosmeticLoadout{}
	if err = json.Unmarshal([]byte(obj.Value), loadout); err != nil {
		return nil, fmt.Errorf("error unmarshalling client profile: %w", err)
	}

	return loadout.Loadout, nil
}

func (r *ProfileRegistry) ValidateSocialGroup(ctx context.Context, userID uuid.UUID, groupID evr.GUID) (evr.GUID, error) {

	// Get the user's active groups
	groups, _, err := r.nk.UserGroupsList(ctx, userID.String(), 100, nil, "")
	if err != nil {
		return groupID, fmt.Errorf("failed to get guild groups: %w", err)
	}

	if len(groups) == 0 {
		return groupID, fmt.Errorf("user is not in any groups")
	}

	groupIds := lo.Map(groups, func(g *api.UserGroupList_UserGroup, _ int) string { return g.GetGroup().GetId() })
	if lo.Contains(groupIds, groupID.String()) {
		// User is in the group
		return groupID, nil
	}

	// If the user is not in the group, find the group with the most members
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].Group.EdgeCount > groups[j].Group.EdgeCount
	})

	return evr.GUID(uuid.FromStringOrNil(groups[0].GetGroup().GetId())), nil

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

func (r *ProfileRegistry) GetSessionProfile(ctx context.Context, session *sessionWS, loginProfile evr.LoginProfile) (*GameProfile, error) {
	p, _ := r.GetProfileOrNew(session.userID)
	p.Lock()
	defer p.Unlock()

	go func() {
		<-ctx.Done()
		r.Unload(session.userID)
	}()

	p.Login = &loginProfile
	p.Server.PublisherLock = p.Login.PublisherLock
	p.Server.LobbyVersion = p.Login.LobbyVersion
	p.Server.LoginTime = time.Now().UTC().Unix()

	// Apply any unlocks based on the user's groups
	if err := r.UpdateEntitledCosmetics(ctx, session.userID, p); err != nil {
		return p, fmt.Errorf("failed to update entitled cosmetics: %w", err)
	}

	// Update the account
	if err := r.discordRegistry.UpdateAccount(ctx, session.userID); err != nil {
		session.logger.Warn("Failed to update account", zap.Error(err))
	}

	if groupID, err := r.ValidateSocialGroup(r.ctx, session.userID, p.Client.Social.Group); err != nil {
		return p, fmt.Errorf("failed to validate social group: %w", err)
		// try to continue
	} else {
		p.Client.Social.Group = groupID
		p.Server.Social.Group = groupID
	}
	return p, nil
}
func (r *ProfileRegistry) UpdateSessionProfile(ctx context.Context, session *sessionWS, update evr.ClientProfile) (*GameProfile, error) {
	// Get the user's profile
	profile := r.GetProfile(session.userID)
	if profile == nil {
		return nil, fmt.Errorf("failed to get user profile")
	}
	profile.Lock()
	defer profile.Unlock()
	// Validate the client profile.
	// TODO FIXME Validate the profile data
	//if errs := evr.ValidateStruct(request.ClientProfile); errs != nil {
	//	return errFailure(fmt.Errorf("invalid client profile: %s", errs), 400)
	//}
	profile.Client = &update

	// Update the displayname based on the user's selected channel.
	groupId := uuid.UUID(profile.Client.Social.Group)
	if groupId != uuid.Nil {
		displayName, err := SetDisplayNameByChannelBySession(ctx, r.nk, r.discordRegistry, session, groupId.String())
		if err != nil {
			r.logger.Error("Failed to set display name.", zap.Error(err))
		} else {
			profile.Server.DisplayName = displayName
			profile.Server.UpdateTime = time.Now().UTC().Unix()
		}
	}

	// Save the profile
	r.SetProfile(session.userID, profile)
	return profile, nil

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

// UpdateUnlocks updates the unlocked cosmetic fields in the dst with the src.
func updateUnlocks(dst, src interface{}) {
	dVal := reflect.ValueOf(dst)
	sVal := reflect.ValueOf(src)

	if dVal.Kind() == reflect.Ptr {
		dVal = dVal.Elem()
		sVal = sVal.Elem()
	}

	for i := 0; i < sVal.NumField(); i++ {
		sField := sVal.Field(i)
		dField := dVal.Field(i)

		// Check if the field is a boolean
		if sField.Kind() == reflect.Bool {
			dField.SetBool(sField.Bool())
		}
	}
}
