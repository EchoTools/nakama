package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	anyascii "github.com/anyascii/go"
	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/samber/lo"
)

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

	// If member is not found in the cache, get it from the API
	member, err := discordRegistry.GetBot().GuildMember(guildID, discordID)
	if err != nil {
		if restError, _ := err.(*discordgo.RESTError); errors.As(err, &restError) && restError.Message != nil && restError.Message.Code != discordgo.ErrCodeUnknownMember {
			return "", "", "", fmt.Errorf("error getting guild member: %w", err)
		}
	} else if member != nil && member.User != nil {
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

func UpdateDisplayNameByGroupID(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, discordRegistry DiscordRegistry, userID, groupID string) (string, error) {

	account, err := nk.AccountGetId(ctx, userID)
	if err != nil {
		return "", fmt.Errorf("error getting account: %w", err)
	}

	metadata := AccountMetadata{}
	err = json.Unmarshal([]byte(account.GetUser().GetMetadata()), &metadata)
	if err != nil {
		return account.GetUser().GetDisplayName(), fmt.Errorf("error unmarshalling account user metadata: %w", err)
	}
	// Get the displayname for the player's active group
	primaryGuildDisplayName := metadata.GetActiveGroupDisplayName()

	username, globalName, guildNick, err := GetDiscordDisplayNames(ctx, db, discordRegistry, userID, groupID)
	if err != nil {
		return primaryGuildDisplayName, fmt.Errorf("error getting discord display names: %w", err)
	}

	if guildNick == "" {
		if groupID == metadata.GetActiveGroupID().String() {
			// If the player has no guild nick, then use their global nick for the active guild
			guildNick = globalName
		} else {
			guildNick = metadata.GetActiveGroupDisplayName()
		}
	}

	options := []string{guildNick, primaryGuildDisplayName, globalName, username}

	displayName, err := SelectDisplayNameByPriority(ctx, nk, userID, username, options)
	if err != nil {
		return primaryGuildDisplayName, fmt.Errorf("error selecting display name by priority: %w", err)
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
	mapping := map[string]string{
		"๒": "b",
		"ɭ": "l",
		"ย": "u",
		"є": "e",
	}

	for k, v := range mapping {
		displayName = strings.ReplaceAll(displayName, k, v)
	}

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
