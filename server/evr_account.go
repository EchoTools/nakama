package server

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

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

type AccountMetadata struct {
	account *api.Account

	DisplayNameOverride  string            `json:"display_name_override"`  // The display name override
	GlobalBanReason      string            `json:"global_ban_reason"`      // The global ban reason
	ActiveGroupID        string            `json:"active_group_id"`        // The active group ID
	GroupDisplayNames    map[string]string `json:"group_display_names"`    // The display names for each guild map[groupID]displayName
	DisableAFKTimeout    bool              `json:"disable_afk_timeout"`    // Disable AFK detection
	TargetUserID         string            `json:"target_user_id"`         // The target user ID to follow in public spaces
	DiscordDebugMessages bool              `json:"discord_debug_messages"` // Enable debug messages in Discord
	modified             bool
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
	return uuid.FromStringOrNil(a.ActiveGroupID)
}

func (a *AccountMetadata) SetActiveGroupID(id uuid.UUID) {
	if a.ActiveGroupID == id.String() {
		return
	}
	a.ActiveGroupID = id.String()
	a.modified = true
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

func (a *AccountMetadata) NeedsUpdate() bool {
	return a.modified
}

type AccountCosmetics struct {
	JerseyNumber int64               `json:"number"`           // The loadout number (jersey number)
	Loadout      evr.CosmeticLoadout `json:"cosmetic_loadout"` // The loadout
}
type EVRLoginRecord struct {
	EvrID        evr.EvrId
	LoginProfile *evr.LoginProfile
	CreateTime   time.Time
	UpdateTime   time.Time
}

func GetEVRRecords(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userId string) (map[evr.EvrId]EVRLoginRecord, error) {
	listRecords, _, err := nk.StorageList(ctx, SystemUserID, userId, EvrLoginStorageCollection, 100, "")
	if err != nil {
		logger.WithField("err", err).Error("Storage list error.")
		return nil, fmt.Errorf("storage list error: %v", err)
	}

	records := make(map[evr.EvrId]EVRLoginRecord, len(listRecords))

	for _, record := range listRecords {
		var loginProfile evr.LoginProfile
		if err := json.Unmarshal([]byte(record.Value), &loginProfile); err != nil {
			return nil, fmt.Errorf("error unmarshalling login profile for %s: %v", record.GetKey(), err)
		}
		evrID, err := evr.ParseEvrId(record.Key)
		if err != nil {
			return nil, fmt.Errorf("error parsing evrID: %v", err)
		}
		records[*evrID] = EVRLoginRecord{
			EvrID:        *evrID,
			LoginProfile: &loginProfile,
			CreateTime:   record.CreateTime.AsTime(),
			UpdateTime:   record.UpdateTime.AsTime(),
		}
	}

	return records, nil
}

func GetDisplayNameRecords(ctx context.Context, nk runtime.NakamaModule, userId string) ([]*api.StorageObject, error) {
	listRecords, _, err := nk.StorageList(ctx, SystemUserID, userId, DisplayNameCollection, 150, "")
	if err != nil {
		return nil, fmt.Errorf("storage list error: %v", err)
	}
	return listRecords, nil
}

func GetAddressRecords(ctx context.Context, nk runtime.NakamaModule, userId string) ([]*api.StorageObject, error) {
	listRecords, _, err := nk.StorageList(ctx, SystemUserID, userId, ClientAddrStorageCollection, 100, "")
	if err != nil {
		return nil, fmt.Errorf("storage list error: %v", err)
	}
	return listRecords, nil
}

func GetUserIpAddresses(ctx context.Context, nk runtime.NakamaModule, userId string) ([]*api.StorageObject, error) {
	listRecords, _, err := nk.StorageList(ctx, SystemUserID, userId, IpAddressIndex, 100, "")
	if err != nil {
		return nil, fmt.Errorf("storage list error: %v", err)
	}
	return listRecords, nil
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

func GetGuildGroupMembership(ctx context.Context, nk runtime.NakamaModule, userID, groupID string) (GuildGroupMembership, error) {
	memberships, err := GetGuildGroupMemberships(ctx, nk, userID, []string{groupID})
	if err != nil {
		return GuildGroupMembership{}, fmt.Errorf("error getting guild group memberships: %w", err)
	}
	if len(memberships) == 0 {
		return GuildGroupMembership{}, ErrMemberNotFound
	}
	return memberships[0], nil
}

func GetGuildGroupMemberships(ctx context.Context, nk runtime.NakamaModule, userID string, groupIDs []string) ([]GuildGroupMembership, error) {

	memberships := make([]GuildGroupMembership, 0)
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
			if len(groupIDs) > 0 && !slices.Contains(groupIDs, g.GetId()) {
				continue
			}

			membership, err := NewGuildGroupMembership(g, uuid.FromStringOrNil(userID), api.UserGroupList_UserGroup_State(ug.GetState().GetValue()))
			if err != nil {
				return nil, fmt.Errorf("error creating guild group membership: %w", err)
			}

			memberships = append(memberships, *membership)
		}
		if cursor == "" {
			break
		}
	}
	return memberships, nil
}
