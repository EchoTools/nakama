# RPC Authorization Middleware

This document explains how to use the RPC authorization middleware system in the Nakama EchoVR fork.

## Overview

All RPC endpoints are now protected by default with a middleware that enforces:
- **Authentication requirement** - User must be logged in
- **Global Operators only** - By default, only members of the "Global Operators" system group can call RPCs

This provides a secure-by-default approach where new RPCs automatically inherit proper authorization.

## Default Behavior

When you register an RPC without any special configuration:

```go
rpcs := map[string]func(...) (...){
    "my/new/rpc": MyNewRPC,
}
```

The middleware automatically:
1. Checks that the user is authenticated (has a valid session)
2. Verifies the user is a member of the "Global Operators" system group
3. Rejects the request with appropriate error codes if either check fails

## Customizing Permissions

### Option 1: Custom Authorization in `configureRPCPermissions()`

To override the default for specific RPCs, modify `configureRPCPermissions()` in `server/evr_runtime.go`:

```go
func configureRPCPermissions() *RPCPermissionConfig {
    config := NewRPCPermissionConfig()
    
    // Allow multiple groups
    config.SetPermission("my/privileged/rpc", RPCPermission{
        RequireAuth:   true,
        AllowedGroups: []string{GroupGlobalOperators, GroupGlobalDevelopers},
    })
    
    // Public RPC (no authentication)
    config.SetPermission("public/status", PublicRPCPermission())
    
    // Custom authorization logic
    config.SetPermission("my/custom/rpc", RPCPermission{
        RequireAuth:   true,
        AllowedGroups: []string{}, // Empty = skip group check
        // Will rely on custom checks in the RPC itself
    })
    
    return config
}
```

### Option 2: Custom Authorization Logic in RPC

For complex authorization (e.g., users can access their own data, operators can access any):

```go
func MyRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
    // Middleware has already verified authentication
    userID := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
    
    var request struct {
        TargetUserID string `json:"target_user_id"`
    }
    json.Unmarshal([]byte(payload), &request)
    
    // Allow users to access their own data
    if request.TargetUserID == userID {
        // Authorized
        return processRequest(request)
    }
    
    // Check if user is a global operator for accessing others' data
    isOperator, err := CheckSystemGroupMembership(ctx, db, userID, GroupGlobalOperators)
    if err != nil {
        return "", runtime.NewError("Failed to check permissions", StatusInternalError)
    }
    if !isOperator {
        return "", runtime.NewError("Permission denied", StatusPermissionDenied)
    }
    
    return processRequest(request)
}
```

Then configure it to skip group checks:

```go
config.SetPermission("my/custom/rpc", RPCPermission{
    RequireAuth:   true,
    AllowedGroups: []string{}, // Skip group check, use custom logic
})
```

### Option 3: Using CustomCheck Function

For reusable authorization logic:

```go
config.SetPermission("guild/admin/action", RPCPermission{
    RequireAuth: true,
    AllowedGroups: []string{GroupGlobalOperators}, // Operators can always access
    CustomCheck: func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, userID string) error {
        // Additional check: user must also be a guild admin
        // (in addition to being authenticated and in AllowedGroups)
        guildID := ctx.Value("guild_id").(string)
        isGuildAdmin, err := CheckGuildAdmin(ctx, db, userID, guildID)
        if err != nil {
            return runtime.NewError("Failed to check guild admin status", StatusInternalError)
        }
        if !isGuildAdmin {
            return runtime.NewError("Must be guild admin", StatusPermissionDenied)
        }
        return nil
    },
})
```

## System Groups

Available system groups for authorization:
- `GroupGlobalOperators` - "Global Operators"
- `GroupGlobalDevelopers` - "Global Developers"
- `GroupGlobalTesters` - "Global Testers"
- `GroupGlobalBots` - "Global Bots"
- `GroupGlobalBadgeAdmins` - "Global Badge Admins"
- `GroupGlobalPrivateDataAccess` - "Global Private Data Access"

## Error Codes

The middleware returns these error codes:
- `StatusUnauthenticated` (3) - No user ID in context
- `StatusPermissionDenied` (7) - User not in allowed groups
- `StatusInternalError` (13) - Failed to check group membership

## Examples

### Example 1: Public Leaderboard RPC

```go
// In configureRPCPermissions()
config.SetPermission("leaderboard/public", PublicRPCPermission())

// The RPC
func PublicLeaderboardRPC(ctx context.Context, ...) (string, error) {
    // No authentication required
    // Anyone can call this
    return getPublicLeaderboard()
}
```

### Example 2: Admin-Only Match Control

```go
// No configuration needed - uses default (Global Operators only)

func TerminateMatchRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
    // Middleware has verified:
    // 1. User is authenticated
    // 2. User is in Global Operators group
    
    // Safe to get user ID without checking
    operatorID := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
    
    var request struct {
        MatchID string `json:"match_id"`
    }
    json.Unmarshal([]byte(payload), &request)
    
    return terminateMatch(ctx, nk, request.MatchID, operatorID)
}
```

### Example 3: User or Operator Access

```go
// In configureRPCPermissions()
config.SetPermission("profile/get", RPCPermission{
    RequireAuth:   true,
    AllowedGroups: []string{}, // Custom auth logic
})

// The RPC
func GetProfileRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
    callerID := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
    
    var request struct {
        UserID string `json:"user_id"`
    }
    json.Unmarshal([]byte(payload), &request)
    
    // Users can view their own profile
    if request.UserID == callerID {
        return getProfile(ctx, nk, request.UserID, false)
    }
    
    // Operators can view any profile with full details
    isOperator, err := CheckSystemGroupMembership(ctx, db, callerID, GroupGlobalOperators)
    if err != nil {
        return "", runtime.NewError("Failed to check permissions", StatusInternalError)
    }
    if isOperator {
        return getProfile(ctx, nk, request.UserID, true)
    }
    
    return "", runtime.NewError("Permission denied", StatusPermissionDenied)
}
```

## Testing Authorization

When writing tests for RPCs:

```go
func TestMyRPC(t *testing.T) {
    logger := &mockLogger{}
    
    // Test 1: No authentication
    ctx := context.Background()
    result, err := MyRPC(ctx, logger, nil, nil, "")
    assert.Error(t, err) // Should fail without auth
    
    // Test 2: With authentication (but no group membership check in unit test)
    ctx = context.WithValue(context.Background(), runtime.RUNTIME_CTX_USER_ID, "test-user")
    // Note: Full group membership check requires database
}
```

## Migration Guide

If you have existing RPCs with manual authorization checks:

### Before
```go
func MyRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
    userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
    if !ok || userID == "" {
        return "", runtime.NewError("Authentication required", StatusUnauthenticated)
    }
    
    isOperator, err := CheckSystemGroupMembership(ctx, db, userID, GroupGlobalOperators)
    if err != nil {
        return "", runtime.NewError("Failed to check permissions", StatusInternalError)
    }
    if !isOperator {
        return "", runtime.NewError("Permission denied", StatusPermissionDenied)
    }
    
    // Actual logic
    return doWork(userID)
}
```

### After
```go
func MyRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
    // Middleware handles auth + operator check
    // Safe to use without checking
    userID := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
    
    // Actual logic
    return doWork(userID)
}
```

The middleware automatically applies the checks when the RPC is registered.

## Security Best Practices

1. **Default to restrictive** - Start with Global Operators only, relax as needed
2. **Principle of least privilege** - Only grant minimum necessary permissions
3. **Audit sensitive RPCs** - Log operator actions for accountability
4. **Test authorization** - Verify both positive and negative cases
5. **Document exceptions** - Clearly comment why an RPC needs custom permissions

## Troubleshooting

**Q: My RPC returns "Authentication required" even though I'm logged in**

A: Check that your client is sending the session token in requests. The middleware reads from `runtime.RUNTIME_CTX_USER_ID` which is set by Nakama's session authentication.

**Q: My RPC returns "Permission denied" for operators**

A: Verify the user is actually in the "Global Operators" group. Check with:
```bash
# In psql
SELECT * FROM group_edge WHERE source_id = 'user-id' AND destination_id IN (
    SELECT id FROM groups WHERE name = 'Global Operators'
);
```

**Q: How do I allow bots to call RPCs?**

A: Configure the RPC to allow the "Global Bots" group:
```go
config.SetPermission("my/bot/rpc", RPCPermission{
    RequireAuth:   true,
    AllowedGroups: []string{GroupGlobalOperators, GroupGlobalBots},
})
```
