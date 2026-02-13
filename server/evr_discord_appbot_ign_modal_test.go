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
			// Create an empty DiscordAppBot instance for testing
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

func TestCreateSetIGNModal_IGP(t *testing.T) {
	tests := []struct {
		name                    string
		userID                  string
		groupID                 string
		currentDisplayName      string
		isLocked                bool
		expectedLockValue       string
		expectedLockPlaceholder string
	}{
		{
			name:                    "unlocked IGN in IGP",
			userID:                  "user123",
			groupID:                 "group456",
			currentDisplayName:      "TestPlayer",
			isLocked:                false,
			expectedLockValue:       "no",
			expectedLockPlaceholder: "yes or no (currently: no)",
		},
		{
			name:                    "locked IGN in IGP",
			userID:                  "user789",
			groupID:                 "group012",
			currentDisplayName:      "LockedPlayer",
			isLocked:                true,
			expectedLockValue:       "yes",
			expectedLockPlaceholder: "yes or no (currently: yes)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create an empty InGamePanel instance for testing
			p := &InGamePanel{}

			response := p.createSetIGNModal(tt.userID, tt.groupID, tt.currentDisplayName, tt.isLocked)

			if response.Type != discordgo.InteractionResponseModal {
				t.Errorf("Expected InteractionResponseModal, got %v", response.Type)
			}

			if response.Data.Title != "Set IGN" {
				t.Errorf("Expected title 'Set IGN', got %s", response.Data.Title)
			}

			expectedCustomID := "igp:set_ign_modal:" + tt.userID + ":" + tt.groupID
			if response.Data.CustomID != expectedCustomID {
				t.Errorf("Expected CustomID '%s', got %s", expectedCustomID, response.Data.CustomID)
			}

			if len(response.Data.Components) != 2 {
				t.Fatalf("Expected 2 components, got %d", len(response.Data.Components))
			}

			// Check second component (lock input) - focus on the UX improvements
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

func TestSuccessMessageFormatting(t *testing.T) {
	tests := []struct {
		name                string
		displayName         string
		isLocked            bool
		expectedEmoji       string
		expectedStatus      string
		expectedExplanation string
	}{
		{
			name:                "locked IGN success message",
			displayName:         "TestPlayer",
			isLocked:            true,
			expectedEmoji:       "🔒",
			expectedStatus:      "locked",
			expectedExplanation: "The player cannot change their display name.",
		},
		{
			name:                "unlocked IGN success message",
			displayName:         "FreePlayer",
			isLocked:            false,
			expectedEmoji:       "🔓",
			expectedStatus:      "unlocked",
			expectedExplanation: "The player can change their display name.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the success message construction logic from handleSetIGNModalSubmit
			lockStatusEmoji := "🔓"
			lockStatusText := "unlocked"
			lockExplanation := "The player can change their display name."
			if tt.isLocked {
				lockStatusEmoji = "🔒"
				lockStatusText = "locked"
				lockExplanation = "The player cannot change their display name."
			}

			// Verify emoji
			if lockStatusEmoji != tt.expectedEmoji {
				t.Errorf("Expected emoji %s, got %s", tt.expectedEmoji, lockStatusEmoji)
			}

			// Verify status text
			if lockStatusText != tt.expectedStatus {
				t.Errorf("Expected status %s, got %s", tt.expectedStatus, lockStatusText)
			}

			// Verify explanation
			if lockExplanation != tt.expectedExplanation {
				t.Errorf("Expected explanation '%s', got '%s'", tt.expectedExplanation, lockExplanation)
			}

			// Verify the message contains key components
			successMessage := "✅ **IGN Override Set Successfully**\n\n" +
				"**Display Name:** " + tt.displayName + "\n" +
				"**Lock Status:** " + lockStatusEmoji + " " + lockStatusText + "\n" +
				"_" + lockExplanation + "_"

			// Check message format
			if !strings.Contains(successMessage, "✅") {
				t.Error("Success message should contain success emoji")
			}
			if !strings.Contains(successMessage, tt.displayName) {
				t.Errorf("Success message should contain display name %s", tt.displayName)
			}
			if !strings.Contains(successMessage, lockStatusEmoji) {
				t.Errorf("Success message should contain lock emoji %s", lockStatusEmoji)
			}
			if !strings.Contains(successMessage, lockStatusText) {
				t.Errorf("Success message should contain status text %s", lockStatusText)
			}
			if !strings.Contains(successMessage, lockExplanation) {
				t.Errorf("Success message should contain explanation: %s", lockExplanation)
			}
		})
	}
}
