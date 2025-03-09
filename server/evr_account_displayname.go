package server

import (
	"regexp"
	"strings"
	"time"

	anyascii "github.com/anyascii/go"
)

var (
	DisplayNameFilterRegex       = regexp.MustCompile(`[^-0-9A-Za-z_\[\] ]`)
	DisplayNameMatchRegex        = regexp.MustCompile(`[A-Za-z]`)
	DisplayNameFilterScoreSuffix = regexp.MustCompile(`\s\(\d+\)\s\[\d+\.\d+%]`)
)

type SuspensionStatus struct {
	GuildId           string        `json:"guild_id"`
	GuildName         string        `json:"guild_name"`
	UserId            string        `json:"userId"`
	UserDiscordId     string        `json:"discordId"`
	EnforcerDiscordId string        `json:"moderatorId"`
	Expiry            time.Time     `json:"expiry"`
	Duration          time.Duration `json:"duration"`
	RoleId            string        `json:"role"`
	RoleName          string        `json:"role_name"`
	Reason            string        `json:"reason"`
}

func (s *SuspensionStatus) Valid() bool {
	/// TODO use validator package
	if s.Expiry.IsZero() || s.Expiry.Before(time.Now()) || s.Duration <= 0 || s.UserId == "" || s.GuildId == "" || s.RoleId == "" {
		return false
	}
	return true
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
