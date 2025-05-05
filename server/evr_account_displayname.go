package server

import (
	"regexp"
	"strings"

	anyascii "github.com/anyascii/go"
)

const DisplayNameMaxLength = 20

var (
	DisplayNameFilterRegex  = regexp.MustCompile(`[^-0-9A-Za-z_\[\]]`)
	emojiFilterPattern      = regexp.MustCompile(`:[a-zA-Z0-9_]+:`)
	displayNameMatchPattern = regexp.MustCompile(`[A-Za-z]`)
	displayNameScorePattern = regexp.MustCompile(`\s\(\d+\)\s\[\d+\.\d+%]`)
)

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
	displayName = displayNameScorePattern.ReplaceAllLiteralString(displayName, "")

	// Treat the unicode NBSP as a terminator
	displayName, _, _ = strings.Cut(displayName, "\u00a0")

	// Convert unicode characters to their closest ascii representation
	displayName = anyascii.Transliterate(displayName)

	// Remove emojis
	displayName = emojiFilterPattern.ReplaceAllLiteralString(displayName, "")

	// Require a minimum matching pattern
	displayName = DisplayNameFilterRegex.ReplaceAllLiteralString(displayName, "")

	// twenty characters maximum
	if len(displayName) > 20 {
		displayName = displayName[:20]
	}

	if !displayNameMatchPattern.MatchString(displayName) {
		return ""
	}

	// Trim spaces from both ends
	displayName = strings.TrimSpace(displayName)
	return displayName
}
