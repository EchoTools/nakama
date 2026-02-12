package server

import (
	"context"
	"database/sql"

	"github.com/heroiclabs/nakama-common/runtime"
)

// RPCPermission defines what permissions are required to execute an RPC
type RPCPermission struct {
	// RequireAuth indicates if authentication is required (default: true)
	RequireAuth bool
	// AllowedGroups is a list of system group names that can access this RPC
	// If empty, defaults to [GroupGlobalOperators]
	AllowedGroups []string
	// CustomCheck is an optional custom authorization function
	CustomCheck func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, userID string) error
}

// DefaultRPCPermission returns the default permission (Global Operators only)
func DefaultRPCPermission() RPCPermission {
	return RPCPermission{
		RequireAuth:   true,
		AllowedGroups: []string{GroupGlobalOperators},
	}
}

// PublicRPCPermission returns permission for public (unauthenticated) access
func PublicRPCPermission() RPCPermission {
	return RPCPermission{
		RequireAuth:   false,
		AllowedGroups: []string{},
	}
}

// RPCPermissionConfig stores permission configurations for all RPCs
type RPCPermissionConfig struct {
	permissions map[string]RPCPermission
}

// NewRPCPermissionConfig creates a new permission config
func NewRPCPermissionConfig() *RPCPermissionConfig {
	return &RPCPermissionConfig{
		permissions: make(map[string]RPCPermission),
	}
}

// SetPermission sets the permission for a specific RPC
func (c *RPCPermissionConfig) SetPermission(rpcID string, perm RPCPermission) {
	c.permissions[rpcID] = perm
}

// GetPermission gets the permission for a specific RPC, or returns default
func (c *RPCPermissionConfig) GetPermission(rpcID string) RPCPermission {
	if perm, exists := c.permissions[rpcID]; exists {
		return perm
	}
	return DefaultRPCPermission()
}

// WithRPCAuthorization wraps an RPC function with authorization checks
func WithRPCAuthorization(
	rpcID string,
	perm RPCPermission,
	fn func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error),
) func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	return func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
		// Check authentication requirement
		userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
		if perm.RequireAuth && (!ok || userID == "") {
			logger.WithField("rpc_id", rpcID).Warn("Unauthenticated RPC access attempt")
			return "", runtime.NewError("Authentication required", StatusUnauthenticated)
		}

		// If authentication is required, check group membership
		if perm.RequireAuth && len(perm.AllowedGroups) > 0 {
			authorized := false
			for _, groupName := range perm.AllowedGroups {
				isMember, err := CheckSystemGroupMembership(ctx, db, userID, groupName)
				if err != nil {
					logger.WithFields(map[string]interface{}{
						"rpc_id":     rpcID,
						"user_id":    userID,
						"group_name": groupName,
						"error":      err,
					}).Error("Failed to check group membership")
					return "", runtime.NewError("Failed to verify permissions", StatusInternalError)
				}
				if isMember {
					authorized = true
					break
				}
			}

			if !authorized {
				logger.WithFields(map[string]interface{}{
					"rpc_id":         rpcID,
					"user_id":        userID,
					"allowed_groups": perm.AllowedGroups,
				}).Warn("Unauthorized RPC access attempt")
				return "", runtime.NewError("Permission denied: Insufficient privileges", StatusPermissionDenied)
			}
		}

		// Run custom check if provided
		if perm.CustomCheck != nil {
			if err := perm.CustomCheck(ctx, logger, db, nk, userID); err != nil {
				return "", err
			}
		}

		// Call the actual RPC function
		return fn(ctx, logger, db, nk, payload)
	}
}
