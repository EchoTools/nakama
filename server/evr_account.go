package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.uber.org/atomic"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/samber/lo"
)

const (
	StorageCollectionGroupProfile = "GroupProfile"
	StorageKeyUnlockedItems       = "unlocks"
)

type GroupProfile struct {
	UserID        string       `json:"user_id"`
	GroupID       string       `json:"group_id"`
	UnlockedItems []evr.Symbol `json:"unlocked_items"`
	NewUnlocks    []evr.Symbol `json:"new_unlocks"`
	UpdateTime    time.Time    `json:"update_time"`
}

func (p GroupProfile) GetStorageID() StorageID {
	return StorageID{Collection: StorageCollectionGroupProfile, Key: p.GroupID}

}

func (p *GroupProfile) UpdateUnlockedItems(updated []evr.Symbol) {
	// Update the unlocked items, adding the new ones to newUnlocks
	added, removed := lo.Difference(updated, p.UnlockedItems)

	if len(added) == 0 && len(removed) == 0 {
		return
	}

	p.UnlockedItems = updated
	p.NewUnlocks = append(p.NewUnlocks, added...)

	// Ensure that all new unlocks are unique, and exist in the updated list
	updatedNewUnlocks := make([]evr.Symbol, 0, len(p.NewUnlocks))

	seen := make(map[evr.Symbol]struct{}, len(p.NewUnlocks))
	for _, unlock := range p.NewUnlocks {
		if _, ok := seen[unlock]; !ok {
			seen[unlock] = struct{}{}
			updatedNewUnlocks = append(updatedNewUnlocks, unlock)
		}
	}

	p.NewUnlocks = updatedNewUnlocks
	p.UpdateTime = time.Now()
}

type EVRAccount struct {
	AccountMetadata
	User        *api.User
	Wallet      string
	Email       string
	Devices     []*api.AccountDevice
	CustomId    string
	VerifyTime  time.Time
	DisableTime time.Time
}

func NewEVRAccount(account *api.Account) (*EVRAccount, error) {
	md := AccountMetadata{}
	if err := json.Unmarshal([]byte(account.User.Metadata), &md); err != nil {
		return nil, fmt.Errorf("error unmarshalling account metadata: %w", err)
	}
	md.account = account

	a := &EVRAccount{
		AccountMetadata: md,
		User:            account.User,
		Wallet:          account.Wallet,
		Email:           account.Email,
		Devices:         account.Devices,
		CustomId:        account.CustomId,
	}
	if account.VerifyTime != nil {
		a.VerifyTime = account.VerifyTime.AsTime()
	}
	if account.DisableTime != nil {
		a.DisableTime = account.DisableTime.AsTime()
	}

	return a, nil
}

func (e *EVRAccount) IsDisabled() bool {
	return !e.DisableTime.IsZero()
}

func (e *EVRAccount) IsLinked() bool {
	return len(e.Devices) > 0
}

type AccountMetadata struct {
	account *api.Account

	GlobalBanReason        string                                     `json:"global_ban_reason"`         // The global ban reason
	RandomizeDisplayName   bool                                       `json:"randomize_display_name"`    // Randomize the display name
	RandomizedDisplayName  string                                     `json:"randomized_display_name"`   // The randomized display name
	DisplayNameOverride    string                                     `json:"display_name_override"`     // The display name override
	GroupDisplayNames      map[string]string                          `json:"group_display_names"`       // The display names for each guild map[groupID]displayName
	ActiveGroupID          string                                     `json:"active_group_id"`           // The active group ID
	DiscordDebugMessages   bool                                       `json:"discord_debug_messages"`    // Enable debug messages in Discord
	RelayMessagesToDiscord bool                                       `json:"relay_messages_to_discord"` // Relay messages to Discord
	TeamName               *atomic.String                             `json:"team_name"`                 // The team name
	DisableAFKTimeout      bool                                       `json:"disable_afk_timeout"`       // Disable AFK detection
	EnableAllCosmetics     bool                                       `json:"enable_all_cosmetics"`      // Enable all cosmetics
	LoadoutCosmetics       *atomic.Pointer[AccountCosmetics]          `json:"cosmetic_loadout"`          // The equipped cosmetics
	CombatLoadout          *atomic.Pointer[CombatLoadout]             `json:"combat_loadout"`            // The combat loadout
	MutedPlayers           *atomic.Value                              `json:"muted_players"`             // The muted players
	GhostedPlayers         *atomic.Value                              `json:"ghosted_players"`           // The ghosted players
	GameSettings           *atomic.Pointer[evr.RemoteLogGameSettings] `json:"game_settings"`             // The game settings
}

func (a *AccountMetadata) ID() string {
	return a.account.User.Id
}

func (a *AccountMetadata) DiscordID() string {
	return a.account.CustomId
}

func (a *AccountMetadata) Username() string {
	return a.account.User.Username
}

func (a *AccountMetadata) DisplayName() string {
	return a.account.User.DisplayName
}

func (a *AccountMetadata) LangTag() string {
	return a.account.User.LangTag
}

func (a *AccountMetadata) AvatarURL() string {
	return a.account.User.AvatarUrl
}

func (a *AccountMetadata) DiscordAccountCreationTime() time.Time {
	t, _ := discordgo.SnowflakeTimestamp(a.DiscordID())
	return t
}

func (a *AccountMetadata) GetActiveGroupID() uuid.UUID {
	if a.ActiveGroupID == "" {
		return uuid.Nil
	}
	return uuid.FromStringOrNil(a.ActiveGroupID)
}

func (a *AccountMetadata) SetActiveGroupID(id uuid.UUID) {
	if a.ActiveGroupID == id.String() {
		return
	}
	a.ActiveGroupID = id.String()
}

func (a *AccountMetadata) GetDisplayName(groupID string) string {
	if a.GroupDisplayNames == nil {
		a.GroupDisplayNames = make(map[string]string)
	}
	if dn, ok := a.GroupDisplayNames[groupID]; ok {
		return dn
	}
	return ""
}

func (a *AccountMetadata) GetGroupDisplayNameOrDefault(groupID string) string {

	if a.GroupDisplayNames == nil {
		a.GroupDisplayNames = make(map[string]string)
	}
	if a.DisplayNameOverride != "" {
		return a.DisplayNameOverride
	}
	if dn, ok := a.GroupDisplayNames[groupID]; ok && dn != "" {
		return dn
	}
	if dn, ok := a.GroupDisplayNames[a.ActiveGroupID]; ok && dn != "" {
		return dn
	} else {
		return a.account.User.Username
	}
}

func (a *AccountMetadata) SetGroupDisplayName(groupID, displayName string) bool {
	if a.GroupDisplayNames == nil {
		a.GroupDisplayNames = make(map[string]string)
	}
	if a.GroupDisplayNames[groupID] == displayName {
		return false
	}
	a.GroupDisplayNames[groupID] = displayName
	return true
}

func (a *AccountMetadata) GetActiveGroupDisplayName() string {
	return a.GetGroupDisplayNameOrDefault(a.ActiveGroupID)
}

func (a *AccountMetadata) MarshalMap() map[string]interface{} {
	b, _ := json.Marshal(a)
	var m map[string]interface{}
	_ = json.Unmarshal(b, &m)
	return m
}

func (a *AccountMetadata) GetMuted() []evr.EvrId {
	if a.MutedPlayers == nil {
		return make([]evr.EvrId, 0)
	}
	return a.MutedPlayers.Load().([]evr.EvrId)
}

func (a *AccountMetadata) SetMuted(muted []evr.EvrId) {
	if a.MutedPlayers == nil {
		a.MutedPlayers = &atomic.Value{}
	}
	a.MutedPlayers.Store(muted)
}

func (a *AccountMetadata) GetGhosted() []evr.EvrId {
	if a.GhostedPlayers == nil {
		return make([]evr.EvrId, 0)
	}
	return a.GhostedPlayers.Load().([]evr.EvrId)
}

func (a *AccountMetadata) SetGhosted(ghosted []evr.EvrId) {
	if a.GhostedPlayers == nil {
		a.GhostedPlayers = &atomic.Value{}
	}
	a.GhostedPlayers.Store(ghosted)
}

func (a *AccountMetadata) SetTeamName(name string) {
	if a.TeamName == nil {
		a.TeamName = atomic.NewString(name)
	}
	a.TeamName.Store(name)
}

func (a *AccountMetadata) GetTeamName() string {
	if a.TeamName == nil {
		return ""
	}
	return a.TeamName.Load()
}

func (a *AccountMetadata) GetCombatLoadout() CombatLoadout {
	if a.CombatLoadout == nil {
		return CombatLoadout{}
	}
	if p := a.CombatLoadout.Load(); p != nil {
		return *p
	}
	return CombatLoadout{}
}

func (a *AccountMetadata) SetCombatLoadout(c CombatLoadout) {
	if a.CombatLoadout == nil {
		a.CombatLoadout = atomic.NewPointer(&c)
	}
	a.CombatLoadout.Store(&c)
}

func (a *AccountMetadata) GetEquippedCosmetics() AccountCosmetics {
	var ptr *AccountCosmetics
	if a.LoadoutCosmetics != nil {
		if p := a.LoadoutCosmetics.Load(); p != nil {
			ptr = p
		}
	}
	if ptr == nil {
		return AccountCosmetics{
			JerseyNumber: 0,
			Loadout: evr.CosmeticLoadout{
				Emote:          "emote_blink_smiley_a",
				Decal:          "decal_default",
				Tint:           "tint_neutral_a_default",
				TintAlignmentA: "tint_blue_a_default",
				TintAlignmentB: "tint_orange_a_default",
				Pattern:        "pattern_default",
				PIP:            "rwd_decalback_default",
				Chassis:        "rwd_chassis_body_s11_a",
				Bracer:         "rwd_bracer_default",
				Booster:        "rwd_booster_default",
				Title:          "rwd_title_title_default",
				Tag:            "rwd_tag_s1_a_secondary",
				Banner:         "rwd_banner_s1_default",
				Medal:          "rwd_medal_default",
				GoalFX:         "rwd_goal_fx_default",
				SecondEmote:    "emote_blink_smiley_a",
				Emissive:       "emissive_default",
				TintBody:       "tint_neutral_a_default",
				PatternBody:    "pattern_default",
				DecalBody:      "decal_default",
			},
		}
	}

	return *a.LoadoutCosmetics.Load()
}

func (a *AccountMetadata) SetEquippedCosmetics(c AccountCosmetics) {
	if a.LoadoutCosmetics == nil {
		a.LoadoutCosmetics = atomic.NewPointer(&c)
	}
	a.LoadoutCosmetics.Store(&c)
}

func (a *AccountMetadata) GetGameSettings() *evr.RemoteLogGameSettings {
	if a.GameSettings == nil {
		return nil
	}
	return a.GameSettings.Load()
}

func (a *AccountMetadata) SetGameSettings(settings evr.RemoteLogGameSettings) {
	if a.GameSettings == nil {
		a.GameSettings = atomic.NewPointer(&settings)
	}
	a.GameSettings.Store(&settings)
}

type CombatLoadout struct {
	CombatWeapon       string `json:"combat_weapon"`
	CombatGrenade      string `json:"combat_grenade"`
	CombatDominantHand uint8  `json:"combat_dominant_hand"`
	CombatAbility      string `json:"combat_ability"`
}
type AccountCosmetics struct {
	JerseyNumber int64               `json:"number"`           // The loadout number (jersey number)
	Loadout      evr.CosmeticLoadout `json:"cosmetic_loadout"` // The loadout
}

func GetDisplayNameByGroupID(ctx context.Context, nk runtime.NakamaModule, userID, groupID string) (string, error) {
	md, err := GetAccountMetadata(ctx, nk, userID)
	if err != nil {
		return md.account.GetUser().GetDisplayName(), fmt.Errorf("error unmarshalling account user metadata: %w", err)
	}

	if dn := md.GetGroupDisplayNameOrDefault(groupID); dn != "" {
		return dn, nil
	}
	if dn := md.account.GetUser().GetDisplayName(); dn != "" {
		return dn, nil
	} else {
		return md.account.GetUser().GetUsername(), nil
	}
}

func UserGuildGroupsList(ctx context.Context, nk runtime.NakamaModule, userID string) (map[string]*GuildGroup, error) {
	groups := make(map[string]*GuildGroup, 0)
	cursor := ""
	for {
		// Fetch the groups using the provided userId
		userGroups, _, err := nk.UserGroupsList(ctx, userID, 100, nil, cursor)
		if err != nil {
			return nil, fmt.Errorf("error getting user groups: %w", err)
		}

		for _, ug := range userGroups {
			g := ug.GetGroup()
			if g.GetLangTag() != "guild" {
				continue
			}
			gg, err := NewGuildGroup(g)
			if err != nil {
				return nil, fmt.Errorf("error creating guild group: %w", err)
			}

			groups[g.GetId()] = gg
		}
		if cursor == "" {
			break
		}
	}
	return groups, nil
}

func GetGuildGroupMemberships(ctx context.Context, nk runtime.NakamaModule, userID string) (map[string]GuildGroupMembership, error) {

	// Get the caller's nakama user ID
	groups, err := UserGuildGroupsList(ctx, nk, userID)
	if err != nil {
		return nil, fmt.Errorf("error getting user guild groups: %w", err)
	}

	memberships := make(map[string]GuildGroupMembership, 0)
	for _, group := range groups {
		memberships[group.ID().String()] = *group.PermissionsUser(userID)
	}

	return memberships, nil
}
