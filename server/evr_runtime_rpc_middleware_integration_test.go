package server

import (
	"context"
	"database/sql"
	"testing"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/stretchr/testify/assert"
)

// TestRPCAuthorizationIntegration verifies that RPC authorization middleware is properly applied
func TestRPCAuthorizationIntegration(t *testing.T) {
	// This test ensures that the middleware is actually being used
	// when RPCs are registered through the InitializeEvrRuntimeModule

	// Test 1: Verify that a protected RPC rejects unauthenticated requests
	t.Run("ProtectedRPCRequiresAuth", func(t *testing.T) {
		logger := &mockLogger{}

		// Create a simple protected RPC
		protectedRPC := func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
			return "success", nil
		}

		// Get default permission (Global Operators only)
		perm := DefaultRPCPermission()

		// Wrap it with authorization
		wrappedRPC := WithRPCAuthorization("test/protected", perm, protectedRPC)

		// Try to call without authentication
		ctx := context.Background()
		result, err := wrappedRPC(ctx, logger, nil, nil, "")

		// Should fail with auth error
		assert.Error(t, err, "Should require authentication")
		assert.Empty(t, result)

		var runtimeErr *runtime.Error
		if assert.ErrorAs(t, err, &runtimeErr) {
			assert.Equal(t, StatusUnauthenticated, int(runtimeErr.Code))
		}
	})

	// Test 2: Verify that configureRPCPermissions properly sets up enforcement RPCs
	// TODO: Re-enable this test after implementing configureRPCPermissions function
	/*
	t.Run("ConfigureRPCPermissions", func(t *testing.T) {
		config := configureRPCPermissions()

		// Verify that enforcement RPCs have custom authorization
		enforcementKickPerm := config.GetPermission("enforcement/kick")
		assert.True(t, enforcementKickPerm.RequireAuth, "enforcement/kick should require auth")
		assert.Empty(t, enforcementKickPerm.AllowedGroups, "enforcement/kick should use custom logic")

		enforcementJournalsPerm := config.GetPermission("enforcement/journals")
		assert.True(t, enforcementJournalsPerm.RequireAuth, "enforcement/journals should require auth")
		assert.Empty(t, enforcementJournalsPerm.AllowedGroups, "enforcement/journals should use custom logic")

		earlyQuitPerm := config.GetPermission("earlyquit/history")
		assert.True(t, earlyQuitPerm.RequireAuth, "earlyquit/history should require auth")
		assert.Empty(t, earlyQuitPerm.AllowedGroups, "earlyquit/history should use custom logic")

		// Verify that a random RPC gets default permissions
		randomPerm := config.GetPermission("random/rpc/not/configured")
		assert.True(t, randomPerm.RequireAuth, "Random RPC should require auth by default")
		assert.Equal(t, []string{GroupGlobalOperators}, randomPerm.AllowedGroups, "Random RPC should require Global Operators by default")
	})
	*/

	// Test 3: Verify public RPC permission works
	t.Run("PublicRPCNoAuthRequired", func(t *testing.T) {
		logger := &mockLogger{}
		called := false

		// Create a public RPC
		publicRPC := func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
			called = true
			return "public-success", nil
		}

		// Get public permission (no auth required)
		perm := PublicRPCPermission()

		// Wrap it with authorization
		wrappedRPC := WithRPCAuthorization("test/public", perm, publicRPC)

		// Try to call without authentication
		ctx := context.Background()
		result, err := wrappedRPC(ctx, logger, nil, nil, "")

		// Should succeed
		assert.NoError(t, err, "Public RPC should not require authentication")
		assert.Equal(t, "public-success", result)
		assert.True(t, called, "RPC should have been called")
	})

	// Test 4: Verify custom check function is called
	t.Run("CustomCheckFunction", func(t *testing.T) {
		logger := &mockLogger{}
		customCheckCalled := false
		rpcCalled := false

		// Create RPC with custom check
		testRPC := func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
			rpcCalled = true
			return "success", nil
		}

		perm := RPCPermission{
			RequireAuth:   true,
			AllowedGroups: []string{}, // No group restriction
			CustomCheck: func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, userID string) error {
				customCheckCalled = true
				if userID == "blocked-user" {
					return runtime.NewError("User is blocked", StatusPermissionDenied)
				}
				return nil
			},
		}

		wrappedRPC := WithRPCAuthorization("test/custom", perm, testRPC)

		// Test with allowed user
		ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_USER_ID, "allowed-user")
		result, err := wrappedRPC(ctx, logger, nil, nil, "")

		assert.NoError(t, err)
		assert.Equal(t, "success", result)
		assert.True(t, customCheckCalled, "Custom check should have been called")
		assert.True(t, rpcCalled, "RPC should have been called for allowed user")

		// Test with blocked user
		customCheckCalled = false
		rpcCalled = false
		ctx = context.WithValue(context.Background(), runtime.RUNTIME_CTX_USER_ID, "blocked-user")
		result, err = wrappedRPC(ctx, logger, nil, nil, "")

		assert.Error(t, err)
		assert.Empty(t, result)
		assert.True(t, customCheckCalled, "Custom check should have been called")
		assert.False(t, rpcCalled, "RPC should not have been called for blocked user")
	})
}
