package server

import (
	"context"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// TestGetPartyMembersForUser tests the helper function that retrieves party members
func TestGetPartyMembersForUser(t *testing.T) {
	tests := []struct {
		name              string
		userID            string
		partyGroupID      string
		partyMembers      []string
		expectedUserIDs   []string
		expectError       bool
		expectPartyLookup bool
	}{
		{
			name:              "user not in party returns only self",
			userID:            "user1",
			partyGroupID:      "",
			partyMembers:      nil,
			expectedUserIDs:   []string{"user1"},
			expectError:       false,
			expectPartyLookup: false,
		},
		{
			name:              "user in party returns all members including self",
			userID:            "user1",
			partyGroupID:      "party123",
			partyMembers:      []string{"user1", "user2", "user3"},
			expectedUserIDs:   []string{"user1", "user2", "user3"},
			expectError:       false,
			expectPartyLookup: true,
		},
		{
			name:              "single user in party returns only self",
			userID:            "user1",
			partyGroupID:      "party456",
			partyMembers:      []string{"user1"},
			expectedUserIDs:   []string{"user1"},
			expectError:       false,
			expectPartyLookup: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create mock NakamaModule
			nk := &mockNakamaModuleForParty{
				partyGroupID: tt.partyGroupID,
				partyMembers: tt.partyMembers,
			}

			userIDs, err := getPartyMembersForUser(ctx, nk, tt.userID)

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if len(userIDs) != len(tt.expectedUserIDs) {
				t.Errorf("Expected %d user IDs, got %d", len(tt.expectedUserIDs), len(userIDs))
				return
			}

			for i, expected := range tt.expectedUserIDs {
				if userIDs[i] != expected {
					t.Errorf("Expected user ID %s at position %d, got %s", expected, i, userIDs[i])
				}
			}

			if nk.partyLookupCalled != tt.expectPartyLookup {
				t.Errorf("Expected partyLookupCalled=%v, got %v", tt.expectPartyLookup, nk.partyLookupCalled)
			}
		})
	}
}

// mockNakamaModuleForParty is a minimal mock for testing party functionality
type mockNakamaModuleForParty struct {
	runtime.NakamaModule
	partyGroupID      string
	partyMembers      []string
	partyLookupCalled bool
}

func (m *mockNakamaModuleForParty) StorageRead(ctx context.Context, reads []*runtime.StorageRead) ([]*api.StorageObject, error) {
	// Mock implementation for LoadMatchmakingSettings
	for _, read := range reads {
		if read.Collection == MatchmakerStorageCollection && read.Key == MatchmakingConfigStorageKey {
			// Return matchmaking settings with party group ID
			settings := MatchmakingSettings{
				LobbyGroupName: m.partyGroupID,
			}
			data, _ := settingsToStorageObject(read.UserID, settings)
			return []*api.StorageObject{data}, nil
		}
	}
	return nil, nil
}

func (m *mockNakamaModuleForParty) StorageIndexList(ctx context.Context, callerID, indexName, query string, limit int, order []string, callerId string) (*api.StorageObjects, string, error) {
	m.partyLookupCalled = true

	// Mock implementation for GetPartyGroupUserIDs
	objects := make([]*api.StorageObject, 0, len(m.partyMembers))
	for _, userID := range m.partyMembers {
		objects = append(objects, &api.StorageObject{
			UserId: userID,
		})
	}

	return &api.StorageObjects{
		Objects: objects,
	}, "", nil
}

func settingsToStorageObject(userID string, settings MatchmakingSettings) (*api.StorageObject, error) {
	return &api.StorageObject{
		Collection: MatchmakerStorageCollection,
		Key:        MatchmakingConfigStorageKey,
		UserId:     userID,
		Value:      `{"group_id":"` + settings.LobbyGroupName + `"}`,
	}, nil
}
