package server

import (
	"context"
	"fmt"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
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
			ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "testnode")

			// Create mock NakamaModule
			nk := &mockNakamaModuleForParty{
				partyGroupID:     tt.partyGroupID,
				partyMembers:     tt.partyMembers,
				storageReadError: tt.storageReadError,
			}

			// Create a mock registry that returns a known UUID for any group name.
			pr := &mockPartyRegistryForLookup{groupID: tt.partyGroupID}
			userIDs := getPartyMembersForUser(ctx, nk, pr, tt.userID)

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

func (m *mockNakamaModuleForParty) StreamUserList(mode uint8, subject, subcontext, label string, includeHidden, includeNotHidden bool) ([]runtime.Presence, error) {
	m.partyLookupCalled = true

	presences := make([]runtime.Presence, 0, len(m.partyMembers))
	for _, userID := range m.partyMembers {
		presences = append(presences, &mockPresence{userID: userID})
	}

	return presences, nil
}

type mockPresence struct {
	userID string
}

func (p *mockPresence) GetUserId() string                { return p.userID }
func (p *mockPresence) GetSessionId() string             { return "" }
func (p *mockPresence) GetNodeId() string                { return "testnode" }
func (p *mockPresence) GetHidden() bool                  { return false }
func (p *mockPresence) GetPersistence() bool             { return false }
func (p *mockPresence) GetUsername() string               { return "" }
func (p *mockPresence) GetStatus() string                { return "" }
func (p *mockPresence) GetReason() runtime.PresenceReason { return runtime.PresenceReasonUnknown }

// mockPartyRegistryForLookup implements just the LookupGroupPartyID method.
type mockPartyRegistryForLookup struct {
	PartyRegistry
	groupID string
}

func (m *mockPartyRegistryForLookup) LookupGroupPartyID(groupName string) (uuid.UUID, bool) {
	if m.groupID == "" {
		return uuid.Nil, false
	}
	return uuid.Must(uuid.NewV4()), true
}

func settingsToStorageObject(userID string, settings MatchmakingSettings) (*api.StorageObject, error) {
	return &api.StorageObject{
		Collection: MatchmakerStorageCollection,
		Key:        MatchmakingConfigStorageKey,
		UserId:     userID,
		Value:      `{"group_id":"` + settings.LobbyGroupName + `"}`,
	}, nil
}

func TestIsCreateModeExcluded(t *testing.T) {
	tests := []struct {
		name          string
		mode          evr.Symbol
		excludedModes []string
		expected      bool
	}{
		{
			name:          "no exclusions",
			mode:          evr.ModeArenaPrivate,
			excludedModes: nil,
			expected:      false,
		},
		{
			name:          "exact mode token match",
			mode:          evr.ModeArenaPrivate,
			excludedModes: []string{"echo_arena_private"},
			expected:      true,
		},
		{
			name:          "symbol alias match",
			mode:          evr.ModeArenaPrivate,
			excludedModes: []string{"arena_private"},
			expected:      true,
		},
		{
			name:          "case and whitespace tolerant",
			mode:          evr.ModeCombatPublic,
			excludedModes: []string{"  ECHO_COMBAT  "},
			expected:      true,
		},
		{
			name:          "different mode not excluded",
			mode:          evr.ModeSocialPrivate,
			excludedModes: []string{"echo_combat", "echo_arena"},
			expected:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := isCreateModeExcluded(tt.mode, tt.excludedModes)
			if actual != tt.expected {
				t.Fatalf("expected %v, got %v", tt.expected, actual)
			}
		})
	}
}
