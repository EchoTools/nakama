package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	anyascii "github.com/anyascii/go"
	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/runtime"
)

type DisplayNameHistory struct {
	DisplayName string `json:"display_name"`
	Timestamp   int64  `json:"timestamp"`
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

func GetEVRAccountID(ctx context.Context, nk runtime.NakamaModule, userID string) (*AccountMetadata, error) {
	nkaccount, err := nk.AccountGetId(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("error getting account: %w", err)
	}
	return EVRAccountFromAccount(nkaccount), nil
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
