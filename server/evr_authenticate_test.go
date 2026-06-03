package server

import (
	"testing"
)

func Test_sanitizeDisplayName(t *testing.T) {

	tests := []struct {
		name        string
		displayName string
		want        string
	}{
		{
			"special characters",
			"!@#$%^&*()_+",
			"",
		},
		{
			"numbers",
			"1234567890",
			"",
		},
		{
			"numbers and letters",
			"015A",
			"015A",
		},
		{
			"hyphen and letters",
			"L-A-",
			"L-A-",
		},
		{
			"single letter",
			"X",
			"X",
		},
		{
			"discord bot scoring suffix",
			"KingNerf Crumbcake (71) [62.95%]",
			"KingNerf Crumbcake",
		},
		{
			"NBSP as a terminator",
			"mother\u00a0୨ৎ", // Use a NBSP as a terminator
			"mother",
		},
		{
			"Unicode latin characters",
			"ℚ𝕨𝔼ℤ𝕚",
			"QwEZi",
		},
		{"Icons with spaces",
			"🗕 🗗 🗙",
			// '#' and '-' are now allowed characters (commit "Expand display name
			// length and allowed characters"); anyascii transliterates the icons.
			"- # X",
		},
		{
			"Non-English characters",
			"๒ɭยє",
			"blue",
		},
		{
			// DisplayNameMaxLength was expanded from 20 to 24.
			"Over 24 characters are truncated",
			"a123456789012345678901234567890",
			"a12345678901234567890123",
		},
		{
			"Phone number",
			"408-111-1111",
			"",
		},
		{
			"Enclosed-emojis in name",
			"❄ ℝ𝕒𝕞𝕤𝕖𝕪 ❄",
			"Ramsey",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeDisplayName(tt.displayName); got != tt.want {
				t.Errorf("sanitizeDisplayName() = `%v`, want `%v`", got, tt.want)
			}
		})
	}
}
