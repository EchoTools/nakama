package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/stretchr/testify/assert"
)

// Helper function to create context with query parameters
func contextWithQueryParams(params map[string][]string) context.Context {
	ctx := context.Background()
	return context.WithValue(ctx, runtime.RUNTIME_CTX_QUERY_PARAMS, params)
}

// Helper function to create context with user ID
func contextWithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, runtime.RUNTIME_CTX_USER_ID, userID)
}

func TestAccountLookupRequest_CacheKey(t *testing.T) {
	tests := []struct {
		name     string
		request  AccountLookupRequest
		expected string
	}{
		{
			name: "all fields set",
			request: AccountLookupRequest{
				Username:    "testuser",
				UserID:      uuid.FromStringOrNil("550e8400-e29b-41d4-a716-446655440000"),
				DiscordID:   "123456789",
				XPID:        "OVR-ORG-123412341234",
				DisplayName: "TestDisplayName",
			},
			expected: "testuser:550e8400-e29b-41d4-a716-446655440000:123456789:OVR-ORG-123412341234:TestDisplayName",
		},
		{
			name: "empty fields",
			request: AccountLookupRequest{
				Username:    "",
				UserID:      uuid.Nil,
				DiscordID:   "",
				XPID:        "",
				DisplayName: "",
			},
			expected: ":00000000-0000-0000-0000-000000000000:::",
		},
		{
			name: "partial fields",
			request: AccountLookupRequest{
				Username:    "testuser",
				XPID:        "OVR-ORG-123412341234",
				DisplayName: "TestDisplayName",
			},
			expected: "testuser:00000000-0000-0000-0000-000000000000::OVR-ORG-123412341234:TestDisplayName",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.request.CacheKey()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestAccountLookupRPC_QueryParameterParsing(t *testing.T) {
	// Test the parameter parsing function directly
	tests := []struct {
		name           string
		queryParams    map[string][]string
		expectedFields AccountLookupRequest
		shouldError    bool
		errorMessage   string
	}{
		{
			name: "valid username",
			queryParams: map[string][]string{
				"username": {"testuser"},
			},
			expectedFields: AccountLookupRequest{
				Username: "testuser",
			},
		},
		{
			name: "valid discord_id",
			queryParams: map[string][]string{
				"discord_id": {"123456789"},
			},
			expectedFields: AccountLookupRequest{
				DiscordID: "123456789",
			},
		},
		{
			name: "valid user_id",
			queryParams: map[string][]string{
				"user_id": {"550e8400-e29b-41d4-a716-446655440000"},
			},
			expectedFields: AccountLookupRequest{
				UserID: uuid.FromStringOrNil("550e8400-e29b-41d4-a716-446655440000"),
			},
		},
		{
			name: "invalid user_id",
			queryParams: map[string][]string{
				"user_id": {"invalid-uuid"},
			},
			shouldError:  true,
			errorMessage: "invalid user id",
		},
		{
			name: "valid xp_id",
			queryParams: map[string][]string{
				"xp_id": {"OVR-ORG-123412341234"},
			},
			expectedFields: AccountLookupRequest{
				XPID: "OVR-ORG-123412341234",
			},
		},
		{
			name: "invalid xp_id",
			queryParams: map[string][]string{
				"xp_id": {"invalid-xp-id"},
			},
			shouldError:  true,
			errorMessage: "invalid xp_id",
		},
		{
			name: "valid display_name",
			queryParams: map[string][]string{
				"display_name": {"TestUser"},
			},
			expectedFields: AccountLookupRequest{
				DisplayName: "TestUser",
			},
		},
		{
			name: "multiple parameters",
			queryParams: map[string][]string{
				"username":     {"testuser"},
				"discord_id":   {"123456789"},
				"display_name": {"TestUser"},
				"xp_id":        {"OVR-ORG-123412341234"},
			},
			expectedFields: AccountLookupRequest{
				Username:    "testuser",
				DiscordID:   "123456789",
				DisplayName: "TestUser",
				XPID:        "OVR-ORG-123412341234",
			},
		},
		{
			name: "empty parameter values",
			queryParams: map[string][]string{
				"username":   {""},
				"discord_id": {""},
			},
			expectedFields: AccountLookupRequest{}, // Should be empty
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseAccountLookupQueryParams(tt.queryParams)

			if tt.shouldError {
				assert.Error(t, err)
				if tt.errorMessage != "" {
					assert.Contains(t, err.Error(), tt.errorMessage)
				}
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedFields.Username, result.Username)
				assert.Equal(t, tt.expectedFields.DiscordID, result.DiscordID)
				assert.Equal(t, tt.expectedFields.UserID, result.UserID)
				assert.Equal(t, tt.expectedFields.XPID, result.XPID)
				assert.Equal(t, tt.expectedFields.DisplayName, result.DisplayName)
			}
		})
	}
}

func TestAccountLookupRPC_JSONPayloadParsing(t *testing.T) {
	// Basic test structure for JSON parsing
	// Full testing would require complete mock setup
	
	t.Run("basic JSON structure", func(t *testing.T) {
		// This test verifies that our AccountLookupRequest struct can be used
		// for JSON unmarshaling, which is what the actual function does
		payload := `{
			"username": "testuser",
			"discord_id": "123456789",
			"display_name": "TestUser"
		}`
		
		var request AccountLookupRequest
		err := json.Unmarshal([]byte(payload), &request)
		assert.NoError(t, err)
		assert.Equal(t, "testuser", request.Username)
		assert.Equal(t, "123456789", request.DiscordID)
		assert.Equal(t, "TestUser", request.DisplayName)
	})
}

func TestParseAccountLookupQueryParams_EvrIdFormats(t *testing.T) {
	// Test that various EvrId formats are properly parsed
	tests := []struct {
		name         string
		xpID         string
		shouldError  bool
	}{
		{
			name:        "OVR-ORG format",
			xpID:        "OVR-ORG-123412341234",
			shouldError: false,
		},
		{
			name:        "STM format",
			xpID:        "STM-123412341234",
			shouldError: false,
		},
		{
			name:        "DMO format", 
			xpID:        "DMO-123412341234",
			shouldError: false,
		},
		{
			name:        "invalid format - no dash",
			xpID:        "INVALID123",
			shouldError: true,
		},
		{
			name:        "invalid format - non-numeric account id",
			xpID:        "OVR-ORG-abc123",
			shouldError: true,
		},
		{
			name:        "empty string",
			xpID:        "",
			shouldError: false, // Empty should not error, just not set
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queryParams := map[string][]string{}
			if tt.xpID != "" {
				queryParams["xp_id"] = []string{tt.xpID}
			}

			result, err := parseAccountLookupQueryParams(queryParams)

			if tt.shouldError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.xpID != "" {
					// If we expect it to parse correctly, check that XPID is set
					assert.NotEmpty(t, result.XPID)
				} else {
					// If empty, XPID should remain empty
					assert.Empty(t, result.XPID)
				}
			}
		})
	}
}

// TODO: Add more comprehensive tests for:
// - Different lookup methods (UserID, XPID, Username, DiscordID, DisplayName)
// - Caching behavior
// - Error conditions (user not found, database errors)
// - Response format validation
// - Private data access permissions