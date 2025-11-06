package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

const (
	StorageCollectionGroupProfile = "GroupProfile"
	StorageKeyUnlockedItems       = "unlocks"
)

type GroupInGameName struct {
	GroupID     string `json:"group_id"`
	DisplayName string `json:"display_name"`
	IsOverride  bool   `json:"is_override"` // If this is an override for the group
	Lock        bool   `json:"lock"`        // If true, prevents users from changing their IGN override
}

type EVRProfile struct {
	EnableAllRemoteLogs    bool                       `json:"enable_all_remote_logs"`    // Enable debug mode
	InGameNames            map[string]GroupInGameName `json:"group_igns"`                // The display names for each group map[groupID]displayName
	ActiveGroupID          string                     `json:"active_group_id"`           // The active group ID
	DiscordDebugMessages   bool                       `json:"discord_debug_messages"`    // Enable debug messages in Discord
	RelayMessagesToDiscord bool                       `json:"relay_messages_to_discord"` // Relay messages to Discord
	TeamName               string                     `json:"team_name"`                 // The team name
	Options                ProfileOptions             `json:"options"`                   // Profile flags
	LoadoutCosmetics       AccountCosmetics           `json:"cosmetic_loadout"`          // The equipped cosmetics
	CombatLoadout          CombatLoadout              `json:"combat_loadout"`            // The combat loadout
	MutedPlayers           []evr.EvrId                `json:"muted_players"`             // The muted players
	GhostedPlayers         []evr.EvrId                `json:"ghosted_players"`           // The ghosted players
	NewUnlocks             []int64                    `json:"new_unlocks"`               // The new unlocks
	GamePauseSettings      *evr.GamePauseSettings     `json:"game_pause_settings"`       // The game settings
	LegalConsents          evr.LegalConsents          `json:"legal_consents"`            // The legal consents
	CustomizationPOIs      *evr.Customization         `json:"customization_pois"`        // The customization POIs
	MatchmakingDivision    string                     `json:"matchmaking_division"`      // The matchmaking division (e.g. bronze, silver, gold, etc.)
	account                *api.Account
}

func BuildEVRProfileFromAccount(account *api.Account) (*EVRProfile, error) {
	a := &EVRProfile{}
	if err := json.Unmarshal([]byte(account.User.Metadata), &a); err != nil {
		return nil, fmt.Errorf("error unmarshalling account metadata: %w", err)
	}
	a.account = account
	return a, nil
}

func (e EVRProfile) UserID() string {
	return e.account.User.Id
}

func (e EVRProfile) IsDisabled() bool {
	return e.account.DisableTime != nil && e.account.DisableTime.GetSeconds() > 0
}

func (e EVRProfile) DisabledAt() time.Time {
	t := time.Time{}
	if e.account.DisableTime != nil {
		t = e.account.DisableTime.AsTime()
	}
	return t
}

func (e EVRProfile) IsLinked() bool {
	for _, d := range e.account.Devices {
		if _, err := evr.ParseEvrId(d.Id); err == nil {
			return true
		}
	}
	return false
}

func (e EVRProfile) XPIDs() []evr.EvrId {
	xpids := make([]evr.EvrId, 0, len(e.account.Devices))
	for _, d := range e.account.Devices {
		xpid, err := evr.ParseEvrId(d.Id)
		if err != nil || xpid == nil {
			continue
		}
		xpids = append(xpids, *xpid)
	}
	return xpids
}

func (e EVRProfile) HasPasswordSet() bool {
	return e.account.GetEmail() != ""
}

func (e EVRProfile) IsOnline() bool {
	return e.account.User.GetOnline()
}

func (e EVRProfile) DiscordID() string {
	return e.account.GetCustomId()
}

func (e EVRProfile) CreatedAt() time.Time {
	return e.account.User.GetCreateTime().AsTime()
}

func (e EVRProfile) UpdatedAt() time.Time {
	return e.account.User.GetUpdateTime().AsTime()
}

func (e EVRProfile) LinkedXPIDs() []evr.EvrId {
	devices := make([]evr.EvrId, 0, len(e.account.Devices))
	for _, d := range e.account.Devices {
		if xpid, err := evr.ParseEvrId(d.Id); err == nil && xpid != nil {
			devices = append(devices, *xpid)
		}
	}
	return devices
}

func (a EVRProfile) ID() string {
	return a.account.User.Id
}

func (a EVRProfile) Username() string {
	return a.account.User.Username
}

func (a EVRProfile) DisplayName() string {
	return a.account.User.DisplayName
}

func (a EVRProfile) Wallet() string {
	return a.account.Wallet
}

func (a EVRProfile) LangTag() string {
	return a.account.User.LangTag
}

func (a EVRProfile) AvatarURL() string {
	return a.account.User.AvatarUrl
}

func (a EVRProfile) DiscordAccountCreationTime() time.Time {
	t, _ := discordgo.SnowflakeTimestamp(a.DiscordID())
	return t
}

func (a EVRProfile) GetActiveGroupID() uuid.UUID {
	if a.ActiveGroupID == "" {
		return uuid.Nil
	}
	return uuid.FromStringOrNil(a.ActiveGroupID)
}

func (a *EVRProfile) SetActiveGroupID(id uuid.UUID) {
	if a.ActiveGroupID == id.String() {
		return
	}
	a.ActiveGroupID = id.String()
}

func (a EVRProfile) DisplayNamesByGroupID() map[string]string {
	if a.InGameNames == nil {
		return make(map[string]string)
	}
	dnMap := make(map[string]string, len(a.InGameNames))
	for k, v := range a.InGameNames {
		dnMap[k] = v.DisplayName
	}
	return dnMap
}
func (e EVRProfile) GetGroupIGNData(groupID string) GroupInGameName {
	if e.InGameNames == nil {
		return GroupInGameName{
			GroupID:     groupID,
			DisplayName: e.Username(),
			IsOverride:  false,
		}
	}
	return e.InGameNames[groupID]
}

func (e *EVRProfile) SetGroupIGNData(groupID string, groupIGN GroupInGameName) {
	if e.InGameNames == nil {
		e.InGameNames = make(map[string]GroupInGameName)
	}
	e.InGameNames[groupID] = groupIGN
}

func (a EVRProfile) GetGroupIGN(groupID string) string {
	if a.InGameNames != nil {
		if dn := a.InGameNames[groupID].DisplayName; dn != "" {
			// Use the group display name, if it exists
			return sanitizeDisplayName(dn)
		} else if dn := a.InGameNames[a.ActiveGroupID].DisplayName; dn != "" {
			// Otherwise, usethe active group display name
			return sanitizeDisplayName(dn)
		} else {
			// Fallback to the username
			if a.account != nil && a.account.User != nil && a.account.User.Username != "" {
				return sanitizeDisplayName(a.account.User.Username)
			}
		}
	}

	if a.account != nil {
		return a.account.User.Username
	} else {
		return ""
	}
}
func (a *EVRProfile) GetGroupDisplayName(groupID string) (string, bool) {
	if a.InGameNames == nil {
		return "", false
	}
	dn, found := a.InGameNames[groupID]
	return dn.DisplayName, found
}

func (a *EVRProfile) SetGroupDisplayName(groupID, displayName string) (updated bool) {
	displayName = sanitizeDisplayName(displayName)
	if groupID == "" || displayName == "" {
		return false
	}
	if a.InGameNames == nil {
		a.InGameNames = make(map[string]GroupInGameName)
	}
	current, exists := a.InGameNames[groupID]
	if exists && current.DisplayName == displayName {
		return false
	}
	a.InGameNames[groupID] = GroupInGameName{
		GroupID:     groupID,
		DisplayName: displayName,
		IsOverride:  current.IsOverride,
	}
	return true
}

func (a *EVRProfile) DeleteGroupDisplayName(groupID string) (updated bool) {
	if a.InGameNames == nil {
		return false
	}
	if _, found := a.InGameNames[groupID]; !found {
		return false
	}
	delete(a.InGameNames, groupID)
	return true
}

func (a EVRProfile) GetActiveGroupDisplayName() string {
	return a.GetGroupIGN(a.ActiveGroupID)
}

func (a EVRProfile) MarshalMap() map[string]any {
	b, _ := json.Marshal(a)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func (a EVRProfile) GetMuted() []evr.EvrId {
	if a.MutedPlayers == nil {
		return make([]evr.EvrId, 0)
	}
	return a.MutedPlayers
}

func (a EVRProfile) GetGhosted() []evr.EvrId {
	if a.GhostedPlayers == nil {
		return make([]evr.EvrId, 0)
	}
	return a.GhostedPlayers
}

func (a *EVRProfile) FixBrokenCosmetics() bool {

	d := evr.DefaultCosmeticLoadout()

	mapping := map[*string]string{
		&a.LoadoutCosmetics.Loadout.Banner:         d.Banner,
		&a.LoadoutCosmetics.Loadout.Booster:        d.Booster,
		&a.LoadoutCosmetics.Loadout.Bracer:         d.Bracer,
		&a.LoadoutCosmetics.Loadout.Chassis:        d.Chassis,
		&a.LoadoutCosmetics.Loadout.Decal:          d.Decal,
		&a.LoadoutCosmetics.Loadout.DecalBody:      d.DecalBody,
		&a.LoadoutCosmetics.Loadout.Emissive:       d.Emissive,
		&a.LoadoutCosmetics.Loadout.Emote:          d.Emote,
		&a.LoadoutCosmetics.Loadout.GoalFX:         d.GoalFX,
		&a.LoadoutCosmetics.Loadout.Medal:          d.Medal,
		&a.LoadoutCosmetics.Loadout.Pattern:        d.Pattern,
		&a.LoadoutCosmetics.Loadout.PatternBody:    d.PatternBody,
		&a.LoadoutCosmetics.Loadout.PIP:            d.PIP,
		&a.LoadoutCosmetics.Loadout.SecondEmote:    d.SecondEmote,
		&a.LoadoutCosmetics.Loadout.Tag:            d.Tag,
		&a.LoadoutCosmetics.Loadout.Tint:           d.Tint,
		&a.LoadoutCosmetics.Loadout.TintAlignmentA: d.TintAlignmentA,
		&a.LoadoutCosmetics.Loadout.TintAlignmentB: d.TintAlignmentB,
		&a.LoadoutCosmetics.Loadout.TintBody:       d.TintBody,
		&a.LoadoutCosmetics.Loadout.Title:          d.Title,
	}

	updated := false
	for k, v := range mapping {
		if *k == "" {
			*k = v
			updated = true
		}
	}

	return updated
}

func EVRProfileLoad(ctx context.Context, nk runtime.NakamaModule, userID string) (*EVRProfile, error) {
	account, err := nk.AccountGetId(ctx, userID)
	if err != nil {
		return nil, err
	}
	return BuildEVRProfileFromAccount(account)
}

func EVRProfileUpdate(ctx context.Context, nk runtime.NakamaModule, userID string, md *EVRProfile) error {
	if userID == SystemUserID {
		return fmt.Errorf("cannot set metadata for system user")
	}
	if md == nil {
		return fmt.Errorf("metadata cannot be nil")
	}
	return nk.AccountUpdateId(ctx, userID, "", md.MarshalMap(), "", "", "", "", "")
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
