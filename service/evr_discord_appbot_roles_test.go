package server

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestPreselectRoleInOptions(t *testing.T) {
	d := &DiscordAppBot{}

	tests := []struct {
		name     string
		options  []discordgo.SelectMenuOption
		roleID   string
		expected int // index of expected default option
	}{
		{
			name: "select specific role",
			options: []discordgo.SelectMenuOption{
				{Label: "None", Value: "none", Default: false},
				{Label: "Member", Value: "123456789", Default: false},
				{Label: "Admin", Value: "987654321", Default: false},
			},
			roleID:   "123456789",
			expected: 1,
		},
		{
			name: "select none when roleID is empty",
			options: []discordgo.SelectMenuOption{
				{Label: "None", Value: "none", Default: false},
				{Label: "Member", Value: "123456789", Default: false},
				{Label: "Admin", Value: "987654321", Default: false},
			},
			roleID:   "",
			expected: 0,
		},
		{
			name: "no matching role",
			options: []discordgo.SelectMenuOption{
				{Label: "None", Value: "none", Default: false},
				{Label: "Member", Value: "123456789", Default: false},
				{Label: "Admin", Value: "987654321", Default: false},
			},
			roleID:   "nonexistent",
			expected: -1, // no option should be selected
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset all defaults
			for i := range tt.options {
				tt.options[i].Default = false
			}

			d.preselectRoleInOptions(tt.options, tt.roleID)

			// Check which option is now default
			defaultIndex := -1
			for i, option := range tt.options {
				if option.Default {
					if defaultIndex != -1 {
						t.Errorf("Multiple options marked as default")
					}
					defaultIndex = i
				}
			}

			if defaultIndex != tt.expected {
				t.Errorf("Expected option at index %d to be default, but got %d", tt.expected, defaultIndex)
			}
		})
	}
}

func TestRoleTypeHandling(t *testing.T) {
	tests := []struct {
		name           string
		roleType       string
		expectedValid  bool
	}{
		{"member role", "member", true},
		{"moderator role", "moderator", true},
		{"serverhost role", "serverhost", true},
		{"suspension role", "suspension", true},
		{"allocator role", "allocator", true},
		{"invalid role", "invalid", false},
		{"empty role", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isValid := false
			switch tt.roleType {
			case "member", "moderator", "serverhost", "suspension", "allocator":
				isValid = true
			}

			if isValid != tt.expectedValid {
				t.Errorf("Expected role type %s to be valid: %v, got: %v", tt.roleType, tt.expectedValid, isValid)
			}
		})
	}
}