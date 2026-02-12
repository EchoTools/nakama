package server

import (
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestCreateLookupSetIGNModal(t *testing.T) {
	tests := []struct {
		name                    string
		currentDisplayName      string
		isLocked                bool
		expectedLockValue       string
		expectedLockPlaceholder string
	}{
		{
			name:                    "unlocked IGN",
			currentDisplayName:      "TestPlayer",
			isLocked:                false,
			expectedLockValue:       "no",
			expectedLockPlaceholder: "yes or no (currently: no)",
		},
		{
			name:                    "locked IGN",
			currentDisplayName:      "LockedPlayer",
			isLocked:                true,
			expectedLockValue:       "yes",
			expectedLockPlaceholder: "yes or no (currently: yes)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a mock DiscordAppBot instance
			d := &DiscordAppBot{}

			response := d.createLookupSetIGNModal(tt.currentDisplayName, tt.isLocked)

			if response.Type != discordgo.InteractionResponseModal {
				t.Errorf("Expected InteractionResponseModal, got %v", response.Type)
			}

			if response.Data.Title != "Override In-Game Name" {
				t.Errorf("Expected title 'Override In-Game Name', got %s", response.Data.Title)
			}

			if response.Data.CustomID != "lookup:set_ign_modal" {
				t.Errorf("Expected CustomID 'lookup:set_ign_modal', got %s", response.Data.CustomID)
			}

			if len(response.Data.Components) != 2 {
				t.Fatalf("Expected 2 components, got %d", len(response.Data.Components))
			}

			// Check first component (display name input)
			displayNameRow := response.Data.Components[0].(discordgo.ActionsRow)
			displayNameInput := displayNameRow.Components[0].(discordgo.TextInput)

			if displayNameInput.CustomID != "display_name_input" {
				t.Errorf("Expected CustomID 'display_name_input', got %s", displayNameInput.CustomID)
			}

			if displayNameInput.Label != "In-Game Display Name" {
				t.Errorf("Expected label 'In-Game Display Name', got %s", displayNameInput.Label)
			}

			if displayNameInput.Value != tt.currentDisplayName {
				t.Errorf("Expected value %s, got %s", tt.currentDisplayName, displayNameInput.Value)
			}

			// Check second component (lock input)
			lockRow := response.Data.Components[1].(discordgo.ActionsRow)
			lockInput := lockRow.Components[0].(discordgo.TextInput)

			if lockInput.CustomID != "lock_input" {
				t.Errorf("Expected CustomID 'lock_input', got %s", lockInput.CustomID)
			}

			expectedLabel := "🔒 Prevent player from changing this name?"
			if lockInput.Label != expectedLabel {
				t.Errorf("Expected label '%s', got %s", expectedLabel, lockInput.Label)
			}

			if lockInput.Value != tt.expectedLockValue {
				t.Errorf("Expected lock value %s, got %s", tt.expectedLockValue, lockInput.Value)
			}

			if lockInput.Placeholder != tt.expectedLockPlaceholder {
				t.Errorf("Expected placeholder '%s', got %s", tt.expectedLockPlaceholder, lockInput.Placeholder)
			}

			// Verify the lock input is required
			if !lockInput.Required {
				t.Error("Expected lock input to be required")
			}
		})
	}
}

func TestLockInputParsing(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		expectedLocked bool
	}{
		// New yes/no format (recommended)
		{"yes lowercase", "yes", true},
		{"yes uppercase", "YES", true},
		{"yes mixed case", "Yes", true},
		{"no lowercase", "no", false},
		{"no uppercase", "NO", false},
		{"no mixed case", "No", false},

		// Legacy true/false format (backward compatibility)
		{"true lowercase", "true", true},
		{"true uppercase", "TRUE", true},
		{"true mixed case", "True", true},
		{"false lowercase", "false", false},
		{"false uppercase", "FALSE", false},
		{"false mixed case", "False", false},

		// Numeric format
		{"numeric 1", "1", true},
		{"numeric 0", "0", false},

		// With whitespace
		{"yes with spaces", "  yes  ", true},
		{"no with spaces", "  no  ", false},

		// Invalid/empty values default to false
		{"empty string", "", false},
		{"random text", "maybe", false},
		{"number 2", "2", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the parsing logic from handleSetIGNModalSubmit
			lockInput := strings.ToLower(strings.TrimSpace(tt.input))
			isLocked := lockInput == "true" || lockInput == "yes" || lockInput == "1"

			if isLocked != tt.expectedLocked {
				t.Errorf("Input '%s': expected locked=%v, got locked=%v", tt.input, tt.expectedLocked, isLocked)
			}
		})
	}
}
