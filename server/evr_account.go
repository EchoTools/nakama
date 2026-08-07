package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	DisableAFKTimeout      bool                       `json:"disable_afk_timeout"`       // Disable AFK detection
	IgnoreBrokenCosmetics  bool                       `json:"ignore_broken_cosmetics"`   // Allow broken cosmetics
	EnableAllCosmetics     bool                       `json:"enable_all_cosmetics"`      // Enable all cosmetics
	GoldDisplayNameActive  bool                       `json:"gold_display_name"`         // The gold name displa
	LoadoutCosmetics       AccountCosmetics           `json:"cosmetic_loadout"`          // The equipped cosmetics
	CombatLoadout          CombatLoadout              `json:"combat_loadout"`            // The combat loadout
	MutedPlayers           []evr.EvrId                `json:"muted_players"`             // The muted players
	GhostedPlayers         []evr.EvrId                `json:"ghosted_players"`           // The ghosted players
	NewUnlocks             []int64                    `json:"new_unlocks"`               // The new unlocks
	LegalConsents          evr.LegalConsents          `json:"legal_consents"`            // The legal consents
	CustomizationPOIs      *evr.Customization         `json:"customization_pois"`        // The customization POIs
	MatchmakingDivision    string                     `json:"matchmaking_division"`      // The matchmaking division (e.g. bronze, silver, gold, etc.)
	LevelOverride          *int                       `json:"level_override,omitempty"`  // Override the player's level in the ServerProfile

	account *api.Account // Account data (not stored)
	version string       // Storage version for optimistic concurrency control (not serialized to JSON)
}

// StorageMeta implements the StorableAdapter interface
func (e *EVRProfile) StorageMeta() StorableMetadata {
	return StorableMetadata{
		Collection:      StorageCollectionEVRProfile,
		Key:             StorageKeyEVRProfile,
		PermissionRead:  runtime.STORAGE_PERMISSION_NO_READ,
		PermissionWrite: runtime.STORAGE_PERMISSION_NO_WRITE,
		Version:         e.version,
	}
}

// SetStorageMeta implements the StorableAdapter interface
func (e *EVRProfile) SetStorageMeta(meta StorableMetadata) {
	e.version = meta.Version
}

func (e EVRProfile) UserID() string {
	if e.account == nil || e.account.User == nil {
		return ""
	}
	return e.account.User.Id
}

func (e EVRProfile) IsDisabled() bool {
	if e.account == nil {
		return false
	}
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
	if e.account == nil {
		return false
	}
	for _, d := range e.account.Devices {
		if _, err := evr.ParseEvrId(d.Id); err == nil {
			return true
		}
	}
	return false
}

func (e EVRProfile) XPIDs() []evr.EvrId {
	if e.account == nil {
		return nil
	}
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
	if e.account == nil {
		return false
	}
	return e.account.GetEmail() != ""
}

func (e EVRProfile) IsOnline() bool {
	if e.account == nil || e.account.User == nil {
		return false
	}
	return e.account.User.GetOnline()
}

func (e EVRProfile) DiscordID() string {
	if e.account == nil {
		return ""
	}
	return e.account.GetCustomId()
}

func (e EVRProfile) CreatedAt() time.Time {
	if e.account == nil || e.account.User == nil || e.account.User.GetCreateTime() == nil {
		return time.Time{}
	}
	return e.account.User.GetCreateTime().AsTime()
}

func (e EVRProfile) UpdatedAt() time.Time {
	if e.account == nil || e.account.User == nil || e.account.User.GetUpdateTime() == nil {
		return time.Time{}
	}
	return e.account.User.GetUpdateTime().AsTime()
}

func (e EVRProfile) LinkedXPIDs() []evr.EvrId {
	if e.account == nil {
		return nil
	}
	devices := make([]evr.EvrId, 0, len(e.account.Devices))
	for _, d := range e.account.Devices {
		if xpid, err := evr.ParseEvrId(d.Id); err == nil && xpid != nil {
			devices = append(devices, *xpid)
		}
	}
	return devices
}

func (a EVRProfile) ID() string {
	if a.account == nil || a.account.User == nil {
		return ""
	}
	return a.account.User.Id
}

func (a EVRProfile) Username() string {
	if a.account == nil || a.account.User == nil {
		return ""
	}
	return a.account.User.Username
}

func (a EVRProfile) DisplayName() string {
	if a.account == nil || a.account.User == nil {
		return ""
	}
	return a.account.User.DisplayName
}

func (a EVRProfile) Wallet() string {
	if a.account == nil {
		return ""
	}
	return a.account.Wallet
}

func (a EVRProfile) LangTag() string {
	if a.account == nil || a.account.User == nil {
		return ""
	}
	return a.account.User.LangTag
}

func (a EVRProfile) AvatarURL() string {
	if a.account == nil || a.account.User == nil {
		return ""
	}
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
		IsOverride:  false,
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

// MarshalMap renders the profile as the generic map that runtime.AccountUpdate
// expects for account metadata.
//
// Decoding uses json.Decoder.UseNumber() rather than a plain json.Unmarshal:
// the default decoder turns every JSON number into a float64, which silently
// truncates the 64-bit EchoVR item hashes in NewUnlocks (and the uint64
// customization POI versions) above 2^53. Because EVRProfileUpdate writes the
// storage value with json.Marshal and the account metadata with this function in
// a single MultiUpdate, a lossy encoding here makes the two halves of one atomic
// write commit different data. json.Number keeps the literal intact, and
// encoding/json re-emits it verbatim when MultiUpdate serializes the map.
//
// Both json errors are returned rather than discarded. Swallowing one would hand
// MultiUpdate a nil metadata map, and RuntimeGoNakamaModule.MultiUpdate skips the
// account update entirely in that case (`if update.Metadata != nil`, see
// server/runtime_go_nakama.go) rather than blanking it. The storage row would
// still commit the new profile while account metadata silently kept the old one,
// leaving the two halves of a single atomic write disagreeing with no error
// raised anywhere.
func (a EVRProfile) MarshalMap() (map[string]any, error) {
	b, err := json.Marshal(a)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal profile: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("failed to decode profile into map: %w", err)
	}
	return m, nil
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

	// Try to load from the storage system first
	profile := &EVRProfile{}
	if err := StorableRead(ctx, nk, userID, profile, false); err == nil {
		// Successfully loaded from storage, attach account
		profile.account = account
		return profile, nil
	} else if status.Code(err) != codes.NotFound {
		return nil, err
	}

	// Fall back to loading from account metadata for backward compatibility
	return BuildEVRProfileFromAccount(account)
}

// EVRProfileUpdate persists md for userID in a single atomic transaction: the
// profile storage write, the account metadata sync, and the cached ServerProfile
// invalidation all commit together or not at all.
//
// There is exactly one write attempt. On a storage version conflict the error is
// returned immediately, wrapped with %w so runtime.ErrStorageRejectedVersion
// stays detectable via errors.Is, which is what isVersionConflictError now uses
// (pinned by TestEVRProfileUpdate_VersionConflictStaysRetryable). Callers that
// need retry-on-conflict must reload and re-apply their own mutation — this
// function will not silently discard the caller's changes to make a write
// succeed.
func EVRProfileUpdate(ctx context.Context, nk runtime.NakamaModule, userID string, md *EVRProfile) error {
	if userID == SystemUserID {
		return fmt.Errorf("cannot set metadata for system user")
	}
	if md == nil {
		return fmt.Errorf("metadata cannot be nil")
	}

	meta := md.StorageMeta()
	meta.UserID = userID
	data, err := json.Marshal(md)
	if err != nil {
		return fmt.Errorf("failed to marshal profile: %w", err)
	}

	metadata, err := md.MarshalMap()
	if err != nil {
		return fmt.Errorf("failed to marshal profile metadata: %w", err)
	}

	acks, _, err := nk.MultiUpdate(ctx,
		[]*runtime.AccountUpdate{{
			UserID:   userID,
			Metadata: metadata,
		}},
		[]*runtime.StorageWrite{{
			Collection:      meta.Collection,
			Key:             meta.Key,
			UserID:          meta.UserID,
			Value:           string(data),
			Version:         meta.Version,
			PermissionRead:  meta.PermissionRead,
			PermissionWrite: meta.PermissionWrite,
		}},
		// Invalidate any cached ServerProfile so it is regenerated from the
		// updated EVRProfile. Unconditional (no version): a missing key is a no-op.
		[]*runtime.StorageDelete{{
			Collection: StorageCollectionServerProfile,
			Key:        StorageKeyServerProfile,
			UserID:     userID,
		}},
		nil, // walletUpdates
		false,
	)
	if err != nil {
		return fmt.Errorf("failed to update profile: %w", err)
	}

	// Update the in-memory version from the write ack.
	if len(acks) > 0 {
		meta.Version = acks[0].GetVersion()
		md.SetStorageMeta(meta)
	}

	return nil
}

// evrProfileUpdateMaxAttempts bounds evrProfileUpdateWithRetry. Three attempts
// matches the retry budget the display-name sync in evr_discord_integrator.go
// already uses for the same key.
const evrProfileUpdateMaxAttempts = 3

// evrProfileUpdateRetryBaseDelay is the pause before the first retry; each
// further attempt doubles it, so the three attempts span roughly 60ms.
//
// Without any pause the attempts are issued back to back within a few hundred
// microseconds — far inside the window a genuinely concurrent writer holds the
// key for. All three would then lose to the same writer and the caller would see
// a hard failure that a few milliseconds of patience avoids. The delay is
// deliberately short: login blocks on this call.
var evrProfileUpdateRetryBaseDelay = 20 * time.Millisecond

// evrProfileUpdateRetryBackoff waits before retry number attempt (1-based).
//
// A cancelled context aborts the wait rather than sleeping out the full delay,
// and reports the conflict error joined with the context error: the caller's
// contract is that a returned conflict stays recognisable to
// isVersionConflictError, and errors.Join keeps both messages in Error().
func evrProfileUpdateRetryBackoff(ctx context.Context, attempt int, conflictErr error) error {
	timer := time.NewTimer(evrProfileUpdateRetryBaseDelay << (attempt - 1))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return errors.Join(conflictErr, ctx.Err())
	case <-timer.C:
		return nil
	}
}

// evrProfileUpdateWithRetry writes profile via EVRProfileUpdate with a bounded
// retry on storage version conflicts.
//
// EVRProfileUpdate itself makes exactly one attempt by design: retrying with the
// caller's stale payload would silently discard whatever the concurrent writer
// committed. This helper supplies the safe form of the retry — on a conflict it
// RE-READS the current stored profile and hands it to apply, so the caller's
// mutation is re-applied on top of fresh data instead of overwriting it. Callers
// must therefore keep apply free of side effects; it may run more than once.
//
// The returned *EVRProfile is the object that was actually written: the caller's
// own pointer when the first attempt succeeded, or the reloaded profile
// otherwise. Callers that keep the profile around (e.g. session parameters) must
// adopt the returned value.
//
// Non-conflict errors are surfaced immediately; the final conflict error is
// returned unchanged so errors.Is / isVersionConflictError still recognise it.
func evrProfileUpdateWithRetry(ctx context.Context, nk runtime.NakamaModule, userID string, profile *EVRProfile, apply func(*EVRProfile) error) (*EVRProfile, error) {
	var err error
	for attempt := 0; attempt < evrProfileUpdateMaxAttempts; attempt++ {
		if attempt > 0 {
			if waitErr := evrProfileUpdateRetryBackoff(ctx, attempt, err); waitErr != nil {
				return nil, waitErr
			}
			var reloaded *EVRProfile
			reloaded, err = EVRProfileLoad(ctx, nk, userID)
			if err != nil {
				return nil, fmt.Errorf("failed to reload profile for retry: %w", err)
			}
			profile = reloaded
			if apply != nil {
				if err := apply(profile); err != nil {
					return nil, err
				}
			}
		}

		if err = EVRProfileUpdate(ctx, nk, userID, profile); err == nil {
			return profile, nil
		}
		if !isVersionConflictError(err) {
			return nil, err
		}
	}
	return nil, err
}

func BuildEVRProfileFromAccount(account *api.Account) (*EVRProfile, error) {
	if account == nil || account.User == nil {
		return nil, fmt.Errorf("account is nil")
	}
	a := &EVRProfile{}

	metadata := strings.TrimSpace(account.User.Metadata)
	if metadata != "" && metadata != "null" {
		if err := json.Unmarshal([]byte(metadata), a); err != nil {
			return nil, fmt.Errorf("error unmarshalling account metadata: %w", err)
		}
	}

	if a.InGameNames == nil {
		a.InGameNames = make(map[string]GroupInGameName)
	}

	if a.MutedPlayers == nil {
		a.MutedPlayers = make([]evr.EvrId, 0)
	}

	if a.GhostedPlayers == nil {
		a.GhostedPlayers = make([]evr.EvrId, 0)
	}

	if a.NewUnlocks == nil {
		a.NewUnlocks = make([]int64, 0)
	}

	if a.CustomizationPOIs == nil {
		a.CustomizationPOIs = &evr.Customization{
			BattlePassSeasonPoiVersion: 3246,
			NewUnlocksPoiVersion:       1,
			StoreEntryPoiVersion:       1,
			ClearNewUnlocksVersion:     1,
		}
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
