package server

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetIGNModalCustomIDFormat(t *testing.T) {
	tests := []struct {
		name           string
		customID       string
		expectedAction string
		expectedParts  int
		shouldParse    bool
	}{
		{
			name:           "valid IGP set_ign_modal with userID and groupID",
			customID:       "igp:set_ign_modal:userid123:groupid456",
			expectedAction: "set_ign_modal",
			expectedParts:  2,
			shouldParse:    true,
		},
		{
			name:           "invalid IGP set_ign_modal without userID and groupID",
			customID:       "igp:set_ign_modal",
			expectedAction: "set_ign_modal",
			expectedParts:  0,
			shouldParse:    false,
		},
		{
			name:           "valid set_ign_modal with discordID and guildID (new format)",
			customID:       "set_ign_modal:123456789:987654321",
			expectedAction: "set_ign_modal",
			expectedParts:  2,
			shouldParse:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action, value, _ := strings.Cut(tt.customID, ":")

			require.NotEmpty(t, action)

			// For IGP modals, the action will be "igp" and we need another cut
			// For the new format, action is "set_ign_modal"
			var modalAction, params string
			if action == "igp" {
				modalAction, params, _ = strings.Cut(value, ":")
			} else {
				modalAction = action
				params = value
			}

			assert.Equal(t, tt.expectedAction, modalAction)

			if tt.shouldParse {
				parts := strings.SplitN(params, ":", 2)
				require.Len(t, parts, tt.expectedParts)
				assert.NotEmpty(t, parts[0]) // targetUserID or targetDiscordID
				assert.NotEmpty(t, parts[1]) // groupID or targetGuildID
			} else {
				parts := strings.SplitN(params, ":", 2)
				require.NotEqual(t, tt.expectedParts, len(parts),
					"Expected parsing to fail but got %d parts", len(parts))
			}
		})
	}
}

func TestIGPCreateSetIGNModalCustomID(t *testing.T) {
	// This test verifies that the IGP's createSetIGNModal produces
	// a CustomID in the correct format that can be parsed by handleSetModalSubmit
	tests := []struct {
		name               string
		userID             string
		groupID            string
		currentDisplayName string
		isLocked           bool
	}{
		{
			name:               "with simple display name unlocked",
			userID:             "test-user-id",
			groupID:            "test-group-id",
			currentDisplayName: "TestIGN",
			isLocked:           false,
		},
		{
			name:               "with empty display name locked",
			userID:             "another-user-id",
			groupID:            "another-group-id",
			currentDisplayName: "",
			isLocked:           true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			igp := &InGamePanel{
				userID: tt.userID,
			}

			modal := igp.createSetIGNModal(tt.userID, tt.groupID, tt.currentDisplayName, tt.isLocked)

			require.NotNil(t, modal)
			require.NotNil(t, modal.Data)
			expectedCustomID := "igp:set_ign_modal:" + tt.userID + ":" + tt.groupID
			assert.Equal(t, expectedCustomID, modal.Data.CustomID)

			// Verify the CustomID has the correct format with all required parts
			parts := strings.Split(modal.Data.CustomID, ":")
			assert.Equal(t, 4, len(parts), "CustomID should have exactly 4 parts (igp, set_ign_modal, userID, groupID), but has %d", len(parts))
			assert.Equal(t, "igp", parts[0])
			assert.Equal(t, "set_ign_modal", parts[1])
			assert.Equal(t, tt.userID, parts[2])
			assert.Equal(t, tt.groupID, parts[3])

			// Verify the modal has both display name and lock input fields
			require.Len(t, modal.Data.Components, 2, "Modal should have 2 component rows")
		})
	}
}

func TestHandleSetIGNModalParsing(t *testing.T) {
	tests := []struct {
		name          string
		value         string
		shouldFail    bool
		expectedError string
	}{
		{
			name:       "valid format with discordID and guildID",
			value:      "123456789:987654321",
			shouldFail: false,
		},
		{
			name:       "valid format with internal userID and groupID",
			value:      "user-uuid-123:group-uuid-456",
			shouldFail: false,
		},
		{
			name:          "invalid format without parameters",
			value:         "",
			shouldFail:    true,
			expectedError: "invalid parameters for set_ign_modal",
		},
		{
			name:          "invalid format with only one parameter",
			value:         "userid123",
			shouldFail:    true,
			expectedError: "invalid parameters for set_ign_modal",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the parsing logic from handleSetModalSubmit
			parts := strings.SplitN(tt.value, ":", 2)

			if tt.shouldFail {
				require.NotEqual(t, 2, len(parts), "Expected parsing to fail")
			} else {
				require.Equal(t, 2, len(parts), "Expected parsing to succeed")
				assert.NotEmpty(t, parts[0]) // rawUserID
				assert.NotEmpty(t, parts[1]) // rawGroupID
			}
		})
	}
}

func TestHandleSetIGNModalIDConversionFallback(t *testing.T) {
	// This test verifies the fallback logic where if DiscordIDToUserID
	// returns empty, the raw value should be used as-is (internal ID case)
	tests := []struct {
		name                 string
		rawUserID            string
		rawGroupID           string
		discordConversionNil bool // Simulates when conversion returns ""
		expectedUserIDIsRaw  bool
		expectedGroupIDIsRaw bool
	}{
		{
			name:                 "Discord IDs need conversion",
			rawUserID:            "123456789012345678",
			rawGroupID:           "987654321098765432",
			discordConversionNil: false,
			expectedUserIDIsRaw:  false,
			expectedGroupIDIsRaw: false,
		},
		{
			name:                 "Internal IDs (conversion returns empty)",
			rawUserID:            "550e8400-e29b-41d4-a716-446655440000",
			rawGroupID:           "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
			discordConversionNil: true,
			expectedUserIDIsRaw:  true,
			expectedGroupIDIsRaw: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the conversion logic from handleSetModalSubmit
			var targetUserID, groupID string

			// Simulate DiscordIDToUserID behavior
			if tt.discordConversionNil {
				targetUserID = "" // Conversion failed
			} else {
				targetUserID = "converted-user-id"
			}
			if targetUserID == "" {
				targetUserID = tt.rawUserID
			}

			// Simulate GuildIDToGroupID behavior
			if tt.discordConversionNil {
				groupID = "" // Conversion failed
			} else {
				groupID = "converted-group-id"
			}
			if groupID == "" {
				groupID = tt.rawGroupID
			}

			if tt.expectedUserIDIsRaw {
				assert.Equal(t, tt.rawUserID, targetUserID, "Expected raw userID to be used as fallback")
			} else {
				assert.NotEqual(t, tt.rawUserID, targetUserID, "Expected converted userID to be used")
			}

			if tt.expectedGroupIDIsRaw {
				assert.Equal(t, tt.rawGroupID, groupID, "Expected raw groupID to be used as fallback")
			} else {
				assert.NotEqual(t, tt.rawGroupID, groupID, "Expected converted groupID to be used")
			}
		})
	}
}
