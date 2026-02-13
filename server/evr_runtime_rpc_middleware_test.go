package server

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/stretchr/testify/assert"
)

// Mock logger for testing
type mockLogger struct{}

func (m *mockLogger) Debug(format string, v ...interface{})                   {}
func (m *mockLogger) Info(format string, v ...interface{})                    {}
func (m *mockLogger) Warn(format string, v ...interface{})                    {}
func (m *mockLogger) Error(format string, v ...interface{})                   {}
func (m *mockLogger) WithField(key string, value interface{}) runtime.Logger  { return m }
func (m *mockLogger) WithFields(fields map[string]interface{}) runtime.Logger { return m }
func (m *mockLogger) Fields() map[string]interface{}                          { return nil }

func TestDefaultRPCPermission(t *testing.T) {
	perm := DefaultRPCPermission()

	assert.True(t, perm.RequireAuth, "Default permission should require auth")
	assert.Equal(t, []string{GroupGlobalOperators}, perm.AllowedGroups, "Default should allow Global Operators")
	assert.Nil(t, perm.CustomCheck, "Default should have no custom check")
}

func TestPublicRPCPermission(t *testing.T) {
	perm := PublicRPCPermission()

	assert.False(t, perm.RequireAuth, "Public permission should not require auth")
	assert.Empty(t, perm.AllowedGroups, "Public should have no group restrictions")
}

func TestRPCPermissionConfig(t *testing.T) {
	config := NewRPCPermissionConfig()

	// Test default permission
	defaultPerm := config.GetPermission("nonexistent")
	assert.True(t, defaultPerm.RequireAuth)
	assert.Equal(t, []string{GroupGlobalOperators}, defaultPerm.AllowedGroups)

	// Test custom permission
	customPerm := RPCPermission{
		RequireAuth:   true,
		AllowedGroups: []string{GroupGlobalDevelopers},
	}
	config.SetPermission("test_rpc", customPerm)

	retrieved := config.GetPermission("test_rpc")
	assert.True(t, retrieved.RequireAuth)
	assert.Equal(t, []string{GroupGlobalDevelopers}, retrieved.AllowedGroups)
}

func TestWithRPCAuthorization_NoAuth(t *testing.T) {
	logger := &mockLogger{}

	// Test RPC that doesn't require auth
	publicPerm := PublicRPCPermission()
	called := false
	testRPC := func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
		called = true
		return "success", nil
	}

	wrapped := WithRPCAuthorization("test_public", publicPerm, testRPC)

	// Create context without user ID
	ctx := context.Background()

	result, err := wrapped(ctx, logger, nil, nil, "")

	assert.NoError(t, err)
	assert.Equal(t, "success", result)
	assert.True(t, called, "RPC should have been called")
}

func TestWithRPCAuthorization_RequireAuthMissing(t *testing.T) {
	logger := &mockLogger{}

	// Test RPC that requires auth but context has no user
	defaultPerm := DefaultRPCPermission()
	testRPC := func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
		t.Fatal("Should not be called")
		return "", nil
	}

	wrapped := WithRPCAuthorization("test_auth", defaultPerm, testRPC)

	// Create context without user ID
	ctx := context.Background()

	result, err := wrapped(ctx, logger, nil, nil, "")

	assert.Error(t, err)
	assert.Empty(t, result)

	// Check error is runtime error with correct code
	var runtimeErr *runtime.Error
	if errors.As(err, &runtimeErr) {
		assert.Equal(t, StatusUnauthenticated, int(runtimeErr.Code))
	}
}

func TestWithRPCAuthorization_CustomCheck(t *testing.T) {
	logger := &mockLogger{}

	// Test RPC with custom check
	customCheckCalled := false
	customPerm := RPCPermission{
		RequireAuth:   true,
		AllowedGroups: []string{},
		CustomCheck: func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, userID string) error {
			customCheckCalled = true
			if userID != "allowed_user" {
				return runtime.NewError("Custom check failed", StatusPermissionDenied)
			}
			return nil
		},
	}

	rpcCalled := false
	testRPC := func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
		rpcCalled = true
		return "success", nil
	}

	wrapped := WithRPCAuthorization("test_custom", customPerm, testRPC)

	// Test with allowed user
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_USER_ID, "allowed_user")
	result, err := wrapped(ctx, logger, nil, nil, "")

	assert.NoError(t, err)
	assert.Equal(t, "success", result)
	assert.True(t, customCheckCalled, "Custom check should have been called")
	assert.True(t, rpcCalled, "RPC should have been called")

	// Test with disallowed user
	customCheckCalled = false
	rpcCalled = false
	ctx = context.WithValue(context.Background(), runtime.RUNTIME_CTX_USER_ID, "disallowed_user")
	result, err = wrapped(ctx, logger, nil, nil, "")

	assert.Error(t, err)
	assert.Empty(t, result)
	assert.True(t, customCheckCalled, "Custom check should have been called")
	assert.False(t, rpcCalled, "RPC should not have been called")
}
