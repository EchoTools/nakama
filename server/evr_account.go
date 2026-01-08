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
	StorageCollectionEVRProfile   = "EVRProfile"
	StorageKeyEVRProfile          = "profile"
)

type GroupInGameName struct {
	GroupID     string `json:"group_id"`
	DisplayName string `json:"display_name"`
	IsOverride  bool   `json:"is_override"` // If this is an override for the group
	IsLocked    bool   `json:"is_locked"`   // If true, prevents user from changing the override
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
	LevelOverride          *int                       `json:"level_override,omitempty"`  // Override the player's level in the ServerProfile
	account                *api.Account
}

// EVRProfileStorage is the authoritative storage object for EVR profile data.
// It is versioned and stored separately from account metadata to prevent staleness.
// The account metadata is generated from this storage object.
type EVRProfileStorage struct {
	EnableAllRemoteLogs    bool                       `json:"enable_all_remote_logs"`
	InGameNames            map[string]GroupInGameName `json:"group_igns"`
	ActiveGroupID          string                     `json:"active_group_id"`
	DiscordDebugMessages   bool                       `json:"discord_debug_messages"`
	RelayMessagesToDiscord bool                       `json:"relay_messages_to_discord"`
	TeamName               string                     `json:"team_name"`
	Options                ProfileOptions             `json:"options"`
	LoadoutCosmetics       AccountCosmetics           `json:"cosmetic_loadout"`
	CombatLoadout          CombatLoadout              `json:"combat_loadout"`
	MutedPlayers           []evr.EvrId                `json:"muted_players"`
	GhostedPlayers         []evr.EvrId                `json:"ghosted_players"`
	NewUnlocks             []int64                    `json:"new_unlocks"`
	GamePauseSettings      *evr.GamePauseSettings     `json:"game_pause_settings"`
	LegalConsents          evr.LegalConsents          `json:"legal_consents"`
	CustomizationPOIs      *evr.Customization         `json:"customization_pois"`
	MatchmakingDivision    string                     `json:"matchmaking_division"`
	LevelOverride          *int                       `json:"level_override,omitempty"`

	version string // Storage version for optimistic concurrency control
}

// StorageMeta implements the StorableAdapter interface
func (s *EVRProfileStorage) StorageMeta() StorableMetadata {
	return StorableMetadata{
		Collection:      StorageCollectionEVRProfile,
		Key:             StorageKeyEVRProfile,
		PermissionRead:  runtime.STORAGE_PERMISSION_NO_READ,
		PermissionWrite: runtime.STORAGE_PERMISSION_NO_WRITE,
		Version:         s.version,
	}
}

// SetStorageMeta implements the StorableAdapter interface
func (s *EVRProfileStorage) SetStorageMeta(meta StorableMetadata) {
	s.version = meta.Version
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
		IsLocked:    current.IsLocked,
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

// GetLevelOverride returns the level override value if set, otherwise nil
func (a EVRProfile) GetLevelOverride() *int {
	return a.LevelOverride
}

// SetLevelOverride sets the level override value; pass nil to clear it
func (a *EVRProfile) SetLevelOverride(level *int) {
	a.LevelOverride = level
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

	// Try to load from the new storage system first
	profileStorage := &EVRProfileStorage{}
	if err := StorableRead(ctx, nk, userID, profileStorage, false); err == nil {
		// Successfully loaded from storage, build EVRProfile from it
		return BuildEVRProfileFromStorage(account, profileStorage), nil
	}

	// Fall back to loading from account metadata for backward compatibility
	return BuildEVRProfileFromAccount(account)
}

func EVRProfileUpdate(ctx context.Context, nk runtime.NakamaModule, userID string, md *EVRProfile) error {
	if userID == SystemUserID {
		return fmt.Errorf("cannot set metadata for system user")
	}
	if md == nil {
		return fmt.Errorf("metadata cannot be nil")
	}

	// Convert EVRProfile to EVRProfileStorage
	profileStorage := md.ToStorage()

	// Write to the new storage system
	if err := StorableWrite(ctx, nk, userID, profileStorage); err != nil {
		return fmt.Errorf("failed to write profile storage: %w", err)
	}

	// Also update the account metadata to keep it in sync
	return nk.AccountUpdateId(ctx, userID, "", md.MarshalMap(), "", "", "", "", "")
}

// BuildEVRProfileFromStorage creates an EVRProfile from account and storage data
func BuildEVRProfileFromStorage(account *api.Account, storage *EVRProfileStorage) *EVRProfile {
	profile := &EVRProfile{
		EnableAllRemoteLogs:    storage.EnableAllRemoteLogs,
		InGameNames:            storage.InGameNames,
		ActiveGroupID:          storage.ActiveGroupID,
		DiscordDebugMessages:   storage.DiscordDebugMessages,
		RelayMessagesToDiscord: storage.RelayMessagesToDiscord,
		TeamName:               storage.TeamName,
		Options:                storage.Options,
		LoadoutCosmetics:       storage.LoadoutCosmetics,
		CombatLoadout:          storage.CombatLoadout,
		MutedPlayers:           storage.MutedPlayers,
		GhostedPlayers:         storage.GhostedPlayers,
		NewUnlocks:             storage.NewUnlocks,
		GamePauseSettings:      storage.GamePauseSettings,
		LegalConsents:          storage.LegalConsents,
		CustomizationPOIs:      storage.CustomizationPOIs,
		MatchmakingDivision:    storage.MatchmakingDivision,
		LevelOverride:          storage.LevelOverride,
		account:                account,
	}
	return profile
}

// ToStorage converts an EVRProfile to EVRProfileStorage
func (e *EVRProfile) ToStorage() *EVRProfileStorage {
	return &EVRProfileStorage{
		EnableAllRemoteLogs:    e.EnableAllRemoteLogs,
		InGameNames:            e.InGameNames,
		ActiveGroupID:          e.ActiveGroupID,
		DiscordDebugMessages:   e.DiscordDebugMessages,
		RelayMessagesToDiscord: e.RelayMessagesToDiscord,
		TeamName:               e.TeamName,
		Options:                e.Options,
		LoadoutCosmetics:       e.LoadoutCosmetics,
		CombatLoadout:          e.CombatLoadout,
		MutedPlayers:           e.MutedPlayers,
		GhostedPlayers:         e.GhostedPlayers,
		NewUnlocks:             e.NewUnlocks,
		GamePauseSettings:      e.GamePauseSettings,
		LegalConsents:          e.LegalConsents,
		CustomizationPOIs:      e.CustomizationPOIs,
		MatchmakingDivision:    e.MatchmakingDivision,
		LevelOverride:          e.LevelOverride,
	}
}

func BuildEVRProfileFromAccount(account *api.Account) (*EVRProfile, error) {
	a := &EVRProfile{}
	if err := json.Unmarshal([]byte(account.User.Metadata), &a); err != nil {
		return nil, fmt.Errorf("error unmarshalling account metadata: %w", err)
	}
	a.account = account
	return a, nil
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
