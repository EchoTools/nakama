package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	anyascii "github.com/anyascii/go"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/samber/lo"
)

const (
	SystemUserID = "00000000-0000-0000-0000-000000000000"

	LinkTicketCollection         = "LinkTickets"
	LinkTicketIndex              = "Index_" + LinkTicketCollection
	DiscordAccessTokenCollection = "DiscordAccessTokens"
	DiscordAccessTokenKey        = "accessToken"
	SuspensionStatusCollection   = "SuspensionStatus"
	ChannelInfoStorageCollection = "ChannelInfo"
	ChannelInfoStorageKey        = "channelInfo"
	EvrLoginStorageCollection    = "EvrLogins"
	ClientAddrStorageCollection  = "ClientAddrs"
	HmdSerialIndex               = "Index_HmdSerial"
	IpAddressIndex               = "Index_" + EvrLoginStorageCollection
	DisplayNameCollection        = "DisplayNames"
	DisplayNameIndex             = "Index_DisplayName"
	GhostedUsersIndex            = "Index_MutedUsers"
	ActiveSocialGroupIndex       = "Index_SocialGroup"
	ActivePartyGroupIndex        = "Index_PartyGroup"
	CacheStorageCollection       = "Cache"
	IPinfoCacheKey               = "IPinfo"
	CosmeticLoadoutCollection    = "CosmeticLoadouts"
	VRMLStorageCollection        = "VRML"

	// The Application ID for Echo VR
	NoOvrAppId uint64 = 0x0
	QuestAppId uint64 = 0x7de88f07bd07a
	PcvrAppId  uint64 = 0x4dd2b684a47fa
)

var (
	DisplayNameFilterRegex       = regexp.MustCompile(`[^-0-9A-Za-z_\[\] ]`)
	DisplayNameMatchRegex        = regexp.MustCompile(`[A-Za-z]`)
	DisplayNameFilterScoreSuffix = regexp.MustCompile(`\s\(\d+\)\s\[\d+\.\d+%]`)
)

type SessionVars struct {
	AppID           string `json:"app_id"`
	EvrID           string `json:"evr_id"`
	ClientIP        string `json:"client_ip"`
	HeadsetType     string `json:"headset_type"`
	HMDSerialNumber string `json:"hmd_serial_number"`
}

func NewSessionVars(appID uint64, evrID evr.EvrId, clientIP, headsetType, hmdSerialNumber string) *SessionVars {
	return &SessionVars{
		AppID:           strconv.FormatUint(appID, 10),
		EvrID:           evrID.Token(),
		ClientIP:        clientIP,
		HeadsetType:     headsetType,
		HMDSerialNumber: hmdSerialNumber,
	}
}

func (s *SessionVars) Vars() map[string]string {
	var m map[string]string
	b, _ := json.Marshal(s)
	_ = json.Unmarshal(b, &m)
	return m
}

func SessionVarsFromMap(m map[string]string) *SessionVars {
	b, _ := json.Marshal(m)
	var s SessionVars
	_ = json.Unmarshal(b, &s)
	return &s
}

func (s *SessionVars) DeviceID() *DeviceAuth {
	appID, _ := strconv.ParseUint(s.AppID, 10, 64)
	evrID, _ := evr.ParseEvrId(s.EvrID)
	return NewDeviceAuth(appID, *evrID, s.HMDSerialNumber, s.ClientIP)
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

func GetDisplayNameRecords(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userId string) ([]*api.StorageObject, error) {
	listRecords, _, err := nk.StorageList(ctx, SystemUserID, userId, DisplayNameCollection, 150, "")
	if err != nil {
		logger.WithField("err", err).Error("Storage list error.")
		return nil, fmt.Errorf("storage list error: %v", err)
	}
	return listRecords, nil
}

func GetAddressRecords(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userId string) ([]*api.StorageObject, error) {
	listRecords, _, err := nk.StorageList(ctx, SystemUserID, userId, ClientAddrStorageCollection, 100, "")
	if err != nil {
		logger.WithField("err", err).Error("Storage list error.")
		return nil, fmt.Errorf("storage list error: %v", err)
	}
	return listRecords, nil
}

func GetUserIpAddresses(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userId string) ([]*api.StorageObject, error) {
	listRecords, _, err := nk.StorageList(ctx, SystemUserID, userId, IpAddressIndex, 100, "")
	if err != nil {
		logger.WithField("err", err).Error("Storage list error.")
		return nil, fmt.Errorf("storage list error: %v", err)
	}
	return listRecords, nil
}

type DisplayNameHistory struct {
	DisplayName string `json:"display_name"`
	Timestamp   int64  `json:"timestamp"`
}

// SelectDisplayNameByPriority sets the displayName for the account based on the priority of the options.
func SelectDisplayNameByPriority(ctx context.Context, nk runtime.NakamaModule, userId, username string, options []string) (displayName string, err error) {

	// Sanitize the options
	options = lo.Map(options, func(s string, _ int) string { return sanitizeDisplayName(s) })

	// Remove blanks
	options = lo.Filter(options, func(s string, _ int) bool { return s != "" })

	filter := make([]string, 0, len(options))

	// Filter usernames of other players
	users, err := nk.UsersGetUsername(ctx, options)
	if err != nil {
		return "", fmt.Errorf("error getting users by username: %w", err)
	}
	for _, u := range users {
		if u.Id == userId {
			continue
		}
		filter = append(filter, u.Username)
	}

	// Filter displayNames of other players
	ops := make([]*runtime.StorageRead, len(options))
	for i, option := range options {
		if option == "" {
			continue
		}
		ops[i] = &runtime.StorageRead{
			Collection: DisplayNameCollection,
			Key:        strings.ToLower(option),
		}
	}
	result, err := nk.StorageRead(ctx, ops)
	if err != nil {
		return "", fmt.Errorf("error reading displayNames: %w", err)
	}

	for _, o := range result {
		if o.UserId == userId {
			continue
		}
		filter = append(filter, o.Key)
	}

	// Filter the options
	for i, o := range options {
		if lo.Contains(filter, strings.ToLower(o)) {
			continue
		}
		return options[i], nil
	}
	// No options available
	return username, nil
}

type GroupMetadata struct {
	GuildID                    string                 `json:"guild_id"`                      // The guild ID
	RulesText                  string                 `json:"rules_text"`                    // The rules text displayed on the main menu
	MemberRole                 string                 `json:"member_role"`                   // The role that has access to create lobbies/matches and join social lobbies
	ModeratorRole              string                 `json:"moderator_role"`                // The rules that have access to moderation tools
	ServerHostRole             string                 `json:"serverhost_role"`               // The rules that have access to serverdb
	AllocatorRole              string                 `json:"allocator_role"`                // The rules that have access to reserve servers
	SuspensionRole             string                 `json:"suspension_role"`               // The roles that have users suspended
	ServerHostUserIDs          []string               `json:"serverhost_user_ids"`           // The broadcaster hosts
	AllocatorUserIDs           []string               `json:"allocator_user_ids"`            // The allocator hosts
	ModeratorUserIDs           []string               `json:"moderator_user_ids"`            // The moderators
	ArenaMatchmakingChannelID  string                 `json:"arena_matchmaking_channel_id"`  // The matchmaking channel
	CombatMatchmakingChannelID string                 `json:"combat_matchmaking_channel_id"` // The matchmaking channel
	DebugChannel               string                 `json:"debug_channel_id"`              // The debug channel
	MembersOnlyMatchmaking     bool                   `json:"members_only_matchmaking"`      // Restrict matchmaking to members only (when this group is the active one)
	Unhandled                  map[string]interface{} `json:"-"`
}

type AccountMetadata struct {
	DisplayNameOverride string            `json:"display_name_override"` // The display name override
	GlobalBanReason     string            `json:"global_ban_reason"`     // The global ban reason
	ActiveGroupID       string            `json:"active_group_id"`       // The active group ID
	Cosmetics           AccountCosmetics  `json:"cosmetics"`             // The loadout
	GroupDisplayNames   map[string]string `json:"group_display_names"`   // The display names for each guild map[groupID]displayName
}

func (a *AccountMetadata) GetActiveGroupID() uuid.UUID {
	return uuid.FromStringOrNil(a.ActiveGroupID)
}

func (a *AccountMetadata) SetActiveGroupID(id uuid.UUID) {
	a.ActiveGroupID = id.String()
}

func (a *AccountMetadata) GetGroupDisplayNameOrDefault(groupID string) string {
	if a.GroupDisplayNames == nil {
		a.GroupDisplayNames = make(map[string]string)
	}
	if dn, ok := a.GroupDisplayNames[groupID]; ok {
		return dn
	}
	return a.GetActiveGroupDisplayName()
}

func (a *AccountMetadata) SetGroupDisplayName(groupID, displayName string) {
	if a.GroupDisplayNames == nil {
		a.GroupDisplayNames = make(map[string]string)
	}
	a.GroupDisplayNames[groupID] = displayName
}

func (a *AccountMetadata) GetActiveGroupDisplayName() string {
	if a.GroupDisplayNames == nil {
		a.GroupDisplayNames = make(map[string]string)
	}
	return a.GroupDisplayNames[a.ActiveGroupID]
}

func (a *AccountMetadata) MarshalToMap() map[string]interface{} {
	b, _ := json.Marshal(a)
	var m map[string]interface{}
	_ = json.Unmarshal(b, &m)
	return m
}

type AccountCosmetics struct {
	JerseyNumber int64               `json:"number"`           // The loadout number (jersey number)
	Loadout      evr.CosmeticLoadout `json:"cosmetic_loadout"` // The loadout
}

type SuspensionStatus struct {
	GuildId            string        `json:"guild_id"`
	GuildName          string        `json:"guild_name"`
	UserId             string        `json:"userId"`
	UserDiscordId      string        `json:"discordId"`
	ModeratorDiscordId string        `json:"moderatorId"`
	Expiry             time.Time     `json:"expiry"`
	Duration           time.Duration `json:"duration"`
	RoleId             string        `json:"role"`
	RoleName           string        `json:"role_name"`
	Reason             string        `json:"reason"`
}

func (s *SuspensionStatus) Valid() bool {
	/// TODO use validator package
	if s.Expiry.IsZero() || s.Expiry.Before(time.Now()) || s.Duration <= 0 || s.UserId == "" || s.GuildId == "" || s.RoleId == "" {
		return false
	}
	return true
}

func (g *GroupMetadata) MarshalToMap() (map[string]interface{}, error) {
	guildGroupBytes, err := json.Marshal(g)
	if err != nil {
		return nil, err
	}

	var guildGroupMap map[string]interface{}
	err = json.Unmarshal(guildGroupBytes, &guildGroupMap)
	if err != nil {
		return nil, err
	}

	return guildGroupMap, nil
}

func UnmarshalGuildGroupMetadataFromMap(guildGroupMap map[string]interface{}) (*GroupMetadata, error) {
	guildGroupBytes, err := json.Marshal(guildGroupMap)
	if err != nil {
		return nil, err
	}

	var g GroupMetadata
	err = json.Unmarshal(guildGroupBytes, &g)
	if err != nil {
		return nil, err
	}

	return &g, nil
}

type RoleGroupMetadata struct {
	GuildId string `json:"guild_id"` // The Discord Guild ID
	Role    string `json:"role_id"`  // The Discord Role ID
}

func NewRoleGroupMetadata(guildId string, roleId string) *RoleGroupMetadata {
	return &RoleGroupMetadata{
		GuildId: guildId,
		Role:    roleId,
	}
}
func (md *RoleGroupMetadata) MarshalToMap() (map[string]interface{}, error) {
	guildGroupBytes, err := json.Marshal(md)
	if err != nil {
		return nil, err
	}

	var guildGroupMap map[string]interface{}
	err = json.Unmarshal(guildGroupBytes, &guildGroupMap)
	if err != nil {
		return nil, err
	}

	return guildGroupMap, nil
}

func GetDiscordDisplayNames(ctx context.Context, db *sql.DB, discordRegistry DiscordRegistry, userID, groupID string) (string, string, string, error) {

	discordID, err := GetDiscordIDByUserID(ctx, db, userID)
	if err != nil {
		return "", "", "", fmt.Errorf("error getting discord ID by user ID: %w", err)
	}

	guildID, err := GetGuildIDByGroupID(ctx, db, groupID)
	if err != nil {
		return "", "", "", fmt.Errorf("error getting guild ID by group ID: %w", err)
	}

	var username, globalName, guildNick string

	if member, err := discordRegistry.GetGuildMember(ctx, guildID, discordID); err == nil && member != nil {
		username = member.User.Username
		globalName = member.User.GlobalName
		guildNick = member.Nick
	} else {
		user, err := discordRegistry.GetUser(ctx, discordID)
		if err != nil {
			return "", "", "", fmt.Errorf("error getting user by discord ID: %w", err)
		}
		username = user.Username
		globalName = user.GlobalName
	}

	return username, globalName, guildNick, nil
}

// Guild Nick, Nick of your Primary Guild,

func GetDisplayNameByGroupID(ctx context.Context, nk runtime.NakamaModule, userID, groupID string) (string, error) {
	account, err := nk.AccountGetId(ctx, userID)
	if err != nil {
		return "", fmt.Errorf("error getting account: %w", err)
	}
	md := AccountMetadata{}
	err = json.Unmarshal([]byte(account.GetUser().GetMetadata()), &md)
	if err != nil {
		return account.GetUser().GetDisplayName(), fmt.Errorf("error unmarshalling account user metadata: %w", err)
	}
	if dn := md.DisplayNameOverride; dn != "" {
		return dn, nil
	} else if dn := md.GetGroupDisplayNameOrDefault(groupID); dn != "" {
		return dn, nil
	} else if dn := account.GetUser().GetDisplayName(); dn != "" {
		return dn, nil
	} else {
		return account.GetUser().GetUsername(), nil
	}
}

func UpdateDisplayNameByGroupID(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, discordRegistry DiscordRegistry, userID, groupID string) (string, error) {

	account, err := nk.AccountGetId(ctx, userID)
	if err != nil {
		return "", fmt.Errorf("error getting account: %w", err)
	}

	// Get the displayname for the player's active group
	defaultDisplayName := account.GetUser().GetDisplayName()

	metadata := AccountMetadata{}
	err = json.Unmarshal([]byte(account.GetUser().GetMetadata()), &metadata)
	if err != nil {
		return defaultDisplayName, fmt.Errorf("error unmarshalling account user metadata: %w", err)
	}

	// Check for a cached display name
	username, globalName, guildNick, err := GetDiscordDisplayNames(ctx, db, discordRegistry, userID, groupID)
	if err != nil {
		return defaultDisplayName, fmt.Errorf("error getting discord display names: %w", err)
	}

	options := []string{guildNick, defaultDisplayName, globalName, username}

	displayName, err := SelectDisplayNameByPriority(ctx, nk, userID, username, options)
	if err != nil {
		return defaultDisplayName, fmt.Errorf("error selecting display name by priority: %w", err)
	}

	// Purge old display names
	records, err := GetDisplayNameRecords(ctx, logger, nk, userID)
	if err != nil {
		return displayName, fmt.Errorf("error getting display names: %w", err)
	}
	storageDeletes := []*runtime.StorageDelete{}
	if len(records) > 2 {
		// Sort the records by create time
		sort.SliceStable(records, func(i, j int) bool {
			return records[i].CreateTime.Seconds > records[j].CreateTime.Seconds
		})
		// Delete all but the first two
		for i := 2; i < len(records); i++ {
			storageDeletes = append(storageDeletes, &runtime.StorageDelete{
				Collection: DisplayNameCollection,
				Key:        records[i].Key,
				UserID:     userID,
			})
		}
	}

	metadata.SetGroupDisplayName(groupID, displayName)

	// Update the account
	accountUpdates := []*runtime.AccountUpdate{
		{
			UserID:      userID,
			Username:    username,
			Metadata:    metadata.MarshalToMap(),
			DisplayName: metadata.GetActiveGroupDisplayName(),
		},
	}

	storageWrites := []*runtime.StorageWrite{
		{
			Collection: DisplayNameCollection,
			Key:        displayName,
			UserID:     userID,
			Value:      "{}",
			Version:    "",
		},
	}

	walletUpdates := []*runtime.WalletUpdate{}
	updateLedger := true
	if _, _, err = nk.MultiUpdate(ctx, accountUpdates, storageWrites, storageDeletes, walletUpdates, updateLedger); err != nil {
		return displayName, fmt.Errorf("error updating account: %w", err)
	}

	// Add [BOT] to the display name if the user is a bot
	if flags, ok := ctx.Value(ctxFlagsKey{}).(int); ok {
		if flags&FlagNoVR != 0 {
			displayName = fmt.Sprintf("%s [BOT]", displayName)
		}
	}

	logger.WithFields(map[string]any{
		"gid":      groupID,
		"selected": displayName,
		"options":  strings.Join(options, ","),
	}).Debug("SetDisplayNameByChannelBySession")
	return displayName, nil
}

// sanitizeDisplayName filters the provided displayName to ensure it is valid.
func sanitizeDisplayName(displayName string) string {

	// Removes the discord score (i.e. ` (71) [62.95%]`) suffix from display names
	displayName = DisplayNameFilterScoreSuffix.ReplaceAllLiteralString(displayName, "")

	// Treat the unicode NBSP as a terminator
	displayName, _, _ = strings.Cut(displayName, "\u00a0")

	// Convert unicode characters to their closest ascii representation
	displayName = anyascii.Transliterate(displayName)

	// Filter the string using the regular expression
	displayName = DisplayNameFilterRegex.ReplaceAllLiteralString(displayName, "")

	// twenty characters maximum
	if len(displayName) > 20 {
		displayName = displayName[:20]
	}

	if !DisplayNameMatchRegex.MatchString(displayName) {
		return ""
	}
	// Trim spaces from both ends
	displayName = strings.TrimSpace(displayName)
	return displayName
}
