package server

import (
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
)

// Test cases demonstrating improved testability

func TestLobbyAuthorizationResult_Authorized(t *testing.T) {
	result := &LobbyAuthorizationResult{
		Authorized: true,
	}
	
	assert.True(t, result.Authorized)
	assert.Empty(t, result.ErrorCode)
	assert.Empty(t, result.ErrorMessage)
}

func TestLobbyAuthorizationResult_Unauthorized(t *testing.T) {
	result := &LobbyAuthorizationResult{
		Authorized:   false,
		ErrorCode:    "test_error",
		ErrorMessage: "Test error message",
		AuditMessage: "Test audit message",
	}
	
	assert.False(t, result.Authorized)
	assert.Equal(t, "test_error", result.ErrorCode)
	assert.Equal(t, "Test error message", result.ErrorMessage)
	assert.Equal(t, "Test audit message", result.AuditMessage)
}

func TestLobbyPartyConfiguration_Solo(t *testing.T) {
	sessionID := uuid.Must(uuid.NewV4())
	config := &LobbyPartyConfiguration{
		LobbyGroup:        nil,
		MemberSessionIDs:  []uuid.UUID{sessionID},
		IsLeader:          true,
		EntrantSessionIDs: []uuid.UUID{sessionID},
	}
	
	assert.Nil(t, config.LobbyGroup)
	assert.True(t, config.IsLeader)
	assert.Len(t, config.MemberSessionIDs, 1)
	assert.Equal(t, sessionID, config.MemberSessionIDs[0])
}

func TestRefactoredLobbyFinder_ValidateMode_Valid(t *testing.T) {
	finder := &RefactoredLobbyFinder{}
	
	validModes := []evr.Symbol{
		evr.ModeArenaPublic,
		evr.ModeSocialPublic,
		evr.ModeCombatPublic,
	}
	
	for _, mode := range validModes {
		lobbyParams := &LobbySessionParameters{Mode: mode}
		err := finder.validateMode(lobbyParams)
		assert.NoError(t, err, "Mode %s should be valid", mode)
	}
}

func TestRefactoredLobbyFinder_ValidateMode_Invalid(t *testing.T) {
	finder := &RefactoredLobbyFinder{}
	
	lobbyParams := &LobbySessionParameters{Mode: evr.Symbol(999)} // Invalid mode
	err := finder.validateMode(lobbyParams)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "is an invalid mode for matchmaking")
}

func TestRefactoredLobbyFinder_ShouldStartMatchmaking(t *testing.T) {
	finder := &RefactoredLobbyFinder{}
	
	// Should start matchmaking for competitive modes
	assert.True(t, finder.shouldStartMatchmaking(evr.ModeArenaPublic))
	assert.True(t, finder.shouldStartMatchmaking(evr.ModeCombatPublic))
	
	// Should not start matchmaking for social modes
	assert.False(t, finder.shouldStartMatchmaking(evr.ModeSocialPublic))
}

// Test the individual components for testability
func TestLobbyAuthorizationContext_Creation(t *testing.T) {
	userID := "test-user-id"
	groupID := "test-group-id"
	
	authCtx := &LobbyAuthorizationContext{
		UserID:  userID,
		GroupID: groupID,
	}
	
	assert.Equal(t, userID, authCtx.UserID)
	assert.Equal(t, groupID, authCtx.GroupID)
}

// Test that the extracted structures can be created and used
func TestExtractedStructures_CanBeCreated(t *testing.T) {
	// Test LobbyAuthorizationResult
	result := &LobbyAuthorizationResult{
		Authorized:   true,
		ErrorCode:    "",
		ErrorMessage: "",
		AuditMessage: "",
	}
	assert.NotNil(t, result)
	
	// Test LobbyAuthorizationContext
	authCtx := &LobbyAuthorizationContext{
		UserID:  "test-user",
		GroupID: "test-group",
	}
	assert.NotNil(t, authCtx)
	
	// Test LobbyPartyConfiguration
	partyConfig := &LobbyPartyConfiguration{
		IsLeader:          true,
		EntrantSessionIDs: []uuid.UUID{},
	}
	assert.NotNil(t, partyConfig)
	
	// Test RefactoredLobbyFinder can be created
	finder := &RefactoredLobbyFinder{}
	assert.NotNil(t, finder)
}