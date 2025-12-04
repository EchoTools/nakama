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
			name:           "valid lookup set_ign_modal with userID and groupID",
			customID:       "lookup:set_ign_modal:userid123:groupid456",
			expectedAction: "set_ign_modal",
			expectedParts:  2,
			shouldParse:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action, value, _ := strings.Cut(tt.customID, ":")

			// For IGP modals, the action will be "igp"
			// For lookup modals, the action will be "lookup"
			require.NotEmpty(t, action)

			// Parse the value to get action and parameters
			modalAction, params, _ := strings.Cut(value, ":")

			assert.Equal(t, tt.expectedAction, modalAction)

			if tt.shouldParse {
				parts := strings.SplitN(params, ":", 2)
				require.Len(t, parts, tt.expectedParts)
				assert.NotEmpty(t, parts[0]) // targetUserID
				assert.NotEmpty(t, parts[1]) // groupID
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
	// a CustomID in the correct format that can be parsed by handleModalSubmit
	tests := []struct {
		name               string
		userID             string
		groupID            string
		currentDisplayName string
	}{
		{
			name:               "with simple display name",
			userID:             "test-user-id",
			groupID:            "test-group-id",
			currentDisplayName: "TestIGN",
		},
		{
			name:               "with empty display name",
			userID:             "another-user-id",
			groupID:            "another-group-id",
			currentDisplayName: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			igp := &InGamePanel{
				userID: tt.userID,
			}

			modal := igp.createSetIGNModal(tt.userID, tt.groupID, tt.currentDisplayName)

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
			name:       "valid format with userID and groupID",
			value:      "set_ign_modal:userid123:groupid456",
			shouldFail: false,
		},
		{
			name:          "invalid format without parameters",
			value:         "set_ign_modal",
			shouldFail:    true,
			expectedError: "invalid parameters for set_ign_modal",
		},
		{
			name:          "invalid format with only one parameter",
			value:         "set_ign_modal:userid123",
			shouldFail:    true,
			expectedError: "invalid parameters for set_ign_modal",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action, value, _ := strings.Cut(tt.value, ":")

			assert.Equal(t, "set_ign_modal", action)

			// Simulate the parsing logic from handleModalSubmit
			parts := strings.SplitN(value, ":", 2)

			if tt.shouldFail {
				require.NotEqual(t, 2, len(parts), "Expected parsing to fail")
			} else {
				require.Equal(t, 2, len(parts), "Expected parsing to succeed")
				assert.NotEmpty(t, parts[0]) // targetUserID
				assert.NotEmpty(t, parts[1]) // groupID
			}
		})
	}
}
