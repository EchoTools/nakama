package server

import (
	"context"
	"fmt"
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
		storageReadError  bool
		expectedUserIDs   []string
		expectPartyLookup bool
	}{
		{
			name:              "user not in party returns only self",
			userID:            "user1",
			partyGroupID:      "",
			partyMembers:      nil,
			storageReadError:  false,
			expectedUserIDs:   []string{"user1"},
			expectPartyLookup: false,
		},
		{
			name:              "user in party returns all members including self",
			userID:            "user1",
			partyGroupID:      "party123",
			partyMembers:      []string{"user1", "user2", "user3"},
			storageReadError:  false,
			expectedUserIDs:   []string{"user1", "user2", "user3"},
			expectPartyLookup: true,
		},
		{
			name:              "single user in party returns only self",
			userID:            "user1",
			partyGroupID:      "party456",
			partyMembers:      []string{"user1"},
			storageReadError:  false,
			expectedUserIDs:   []string{"user1"},
			expectPartyLookup: true,
		},
		{
			name:              "storage read error returns only self",
			userID:            "user1",
			partyGroupID:      "",
			partyMembers:      nil,
			storageReadError:  true,
			expectedUserIDs:   []string{"user1"},
			expectPartyLookup: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create mock NakamaModule
			nk := &mockNakamaModuleForParty{
				partyGroupID:     tt.partyGroupID,
				partyMembers:     tt.partyMembers,
				storageReadError: tt.storageReadError,
			}

			userIDs := getPartyMembersForUser(ctx, nk, tt.userID)

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
	storageReadError  bool
}

func (m *mockNakamaModuleForParty) StorageRead(ctx context.Context, reads []*runtime.StorageRead) ([]*api.StorageObject, error) {
	// Simulate storage read error if requested
	if m.storageReadError {
		return nil, fmt.Errorf("simulated storage read error")
	}

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

func (m *mockNakamaModuleForParty) StorageIndexList(ctx context.Context, callerID, indexName, query string, limit int, order []string, callerID2 string) (*api.StorageObjects, string, error) {
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
