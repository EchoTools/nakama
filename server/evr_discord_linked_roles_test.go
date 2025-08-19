package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"
)

type mockNakamaModule struct {
	runtime.NakamaModule
}

func (m *mockNakamaModule) StorageRead(ctx context.Context, reads []*runtime.StorageRead) ([]*api.StorageObject, error) {
	// Return empty for HasLoggedIntoEcho check
	return []*api.StorageObject{}, nil
}

func TestDiscordLinkedRolesHandler_ServeHTTP(t *testing.T) {
	consoleLogger := NewJSONLogger(os.Stdout, zapcore.ErrorLevel, JSONFormat)
	logger := NewRuntimeGoLogger(consoleLogger)
	ctx := context.Background()

	// Create a mock database and nakama module
	var db *sql.DB // nil for this test
	nk := &mockNakamaModule{}

	handler := NewDiscordLinkedRolesHandler(ctx, logger, db, nk)

	tests := []struct {
		name           string
		method         string
		authHeader     string
		expectedStatus int
		expectedBody   string
	}{
		{
			name:           "OPTIONS preflight request",
			method:         "OPTIONS",
			authHeader:     "",
			expectedStatus: http.StatusOK,
			expectedBody:   "",
		},
		{
			name:           "Method not allowed",
			method:         "POST",
			authHeader:     "",
			expectedStatus: http.StatusMethodNotAllowed,
			expectedBody:   "Method not allowed\n",
		},
		{
			name:           "Missing Authorization header",
			method:         "GET",
			authHeader:     "",
			expectedStatus: http.StatusUnauthorized,
			expectedBody:   "Missing Authorization header\n",
		},
		{
			name:           "Invalid Authorization header format",
			method:         "GET",
			authHeader:     "Basic dGVzdA==",
			expectedStatus: http.StatusUnauthorized,
			expectedBody:   "Invalid Authorization header format\n",
		},
		{
			name:           "Missing access token",
			method:         "GET",
			authHeader:     "Bearer ",
			expectedStatus: http.StatusUnauthorized,
			expectedBody:   "Missing access token\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/discord/linked-roles/metadata", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}

			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			assert.Equal(t, tt.expectedStatus, rr.Code)
			assert.Equal(t, tt.expectedBody, rr.Body.String())
		})
	}
}

func TestDiscordLinkedRolesMetadata_JSON(t *testing.T) {
	metadata := DiscordLinkedRolesMetadata{
		PlatformName:     "Echo VR",
		PlatformUsername: "TestUser",
		HasHeadset:       true,
		HasPlayedEcho:    true,
	}

	// Test JSON marshaling
	data, err := json.Marshal(metadata)
	require.NoError(t, err)

	expected := `{"platform_name":"Echo VR","platform_username":"TestUser","has_headset":true,"has_played_echo":true}`
	assert.JSONEq(t, expected, string(data))

	// Test JSON unmarshaling
	var unmarshaled DiscordLinkedRolesMetadata
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err)
	assert.Equal(t, metadata, unmarshaled)
}

func TestDiscordLinkedRolesHandler_CORS(t *testing.T) {
	consoleLogger := NewJSONLogger(os.Stdout, zapcore.ErrorLevel, JSONFormat)
	logger := NewRuntimeGoLogger(consoleLogger)
	ctx := context.Background()

	var db *sql.DB
	nk := &mockNakamaModule{}

	handler := NewDiscordLinkedRolesHandler(ctx, logger, db, nk)

	// Test OPTIONS request for CORS preflight
	req := httptest.NewRequest("OPTIONS", "/discord/linked-roles/metadata", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "*", rr.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "GET, OPTIONS", rr.Header().Get("Access-Control-Allow-Methods"))
	assert.Equal(t, "Authorization, Content-Type", rr.Header().Get("Access-Control-Allow-Headers"))
}
