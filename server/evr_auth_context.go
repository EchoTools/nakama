package server

import (
	"context"
	"database/sql"
	"fmt"
)

// UserPermissions caches system group memberships resolved in a single SQL query.
// Attach to context at authentication/session creation time to eliminate redundant DB queries.
type UserPermissions struct {
	IsGlobalOperator          bool
	IsGlobalDeveloper         bool
	IsGlobalBot               bool
	IsGlobalTester            bool
	IsGlobalBadgeAdmin        bool
	IsGlobalPrivateDataAccess bool
	IsGlobalRequire2FA        bool
}

type contextKeyUserPermissions struct{}

// ResolveUserPermissions resolves ALL system group memberships in a single SQL query.
// This eliminates ~25+ redundant CheckSystemGroupMembership calls across the codebase.
func ResolveUserPermissions(ctx context.Context, db *sql.DB, userID string) (*UserPermissions, error) {
	if userID == "" {
		return &UserPermissions{}, nil
	}

	// Single query to get ALL system group memberships
	query := `
		SELECT g.name 
		FROM groups g
		JOIN group_edge ge ON g.id = ge.destination_id
		WHERE ge.source_id = $1
		  AND g.lang_tag = $2
		  AND ge.state >= 0 AND ge.state <= 2  -- States: 0=Superadmin, 1=Admin, 2=Member
	`

	rows, err := db.QueryContext(ctx, query, userID, SystemGroupLangTag)
	if err != nil {
		return nil, fmt.Errorf("failed to query system groups: %w", err)
	}
	defer rows.Close()

	perms := &UserPermissions{}
	for rows.Next() {
		var groupName string
		if err := rows.Scan(&groupName); err != nil {
			return nil, fmt.Errorf("failed to scan group name: %w", err)
		}

		// Map group name to permission flags
		switch groupName {
		case GroupGlobalOperators:
			perms.IsGlobalOperator = true
		case GroupGlobalDevelopers:
			perms.IsGlobalDeveloper = true
		case GroupGlobalBots:
			perms.IsGlobalBot = true
		case GroupGlobalTesters:
			perms.IsGlobalTester = true
		case GroupGlobalBadgeAdmins:
			perms.IsGlobalBadgeAdmin = true
		case GroupGlobalPrivateDataAccess:
			perms.IsGlobalPrivateDataAccess = true
		case GroupGlobalRequire2FA:
			perms.IsGlobalRequire2FA = true
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating group rows: %w", err)
	}

	return perms, nil
}

// WithUserPermissions attaches permissions to the context.
func WithUserPermissions(ctx context.Context, perms *UserPermissions) context.Context {
	return context.WithValue(ctx, contextKeyUserPermissions{}, perms)
}

// PermissionsFromContext retrieves permissions from the context.
// Returns nil if no permissions are cached (callers should fall back to CheckSystemGroupMembership).
func PermissionsFromContext(ctx context.Context) *UserPermissions {
	if perms, ok := ctx.Value(contextKeyUserPermissions{}).(*UserPermissions); ok {
		return perms
	}
	return nil
}
