# Break Alternates RPC Endpoint

## Overview

The `account/break_alternates` RPC endpoint allows Global Operators to break alternate account associations between two users.

## Endpoint

**RPC ID:** `account/break_alternates`

**Authorization:** Global Operators only

## Request Payload

```json
{
  "user_id_1": "550e8400-e29b-41d4-a716-446655440000",
  "user_id_2": "550e8400-e29b-41d4-a716-446655440001"
}
```

### Parameters

- `user_id_1` (string, required): First user's UUID
- `user_id_2` (string, required): Second user's UUID

Both UUIDs must be:
- Non-empty
- Valid UUID format
- Different from each other

## Response Payload

```json
{
  "success": true,
  "primary_user_id": "550e8400-e29b-41d4-a716-446655440001",
  "other_user_id": "550e8400-e29b-41d4-a716-446655440000",
  "message": "Alternate account association successfully broken",
  "operator_user_id": "550e8400-e29b-41d4-a716-446655440002"
}
```

### Response Fields

- `success` (boolean): Whether the operation succeeded
- `primary_user_id` (string): UUID of the account with more recent login history
- `other_user_id` (string): UUID of the other account
- `message` (string): Human-readable result message
- `operator_user_id` (string): UUID of the operator who performed the action

## Behavior

1. **Validates Input**
   - Ensures both user IDs are provided
   - Validates UUID format
   - Ensures user IDs are different

2. **Loads Login Histories**
   - Fetches `LoginHistory` for both users
   - Returns `StatusNotFound` if either user doesn't exist

3. **Determines Primary Account**
   - Compares `LastSeen()` timestamps
   - The account with more recent login is considered primary
   - This information is returned but doesn't affect the unlinking operation

4. **Breaks Associations**
   - Removes each user from the other's `AlternateMatches` map
   - Removes each user from the other's `SecondDegreeAlternates` array
   - Operations are bidirectional - both accounts are updated

5. **Saves Changes**
   - Persists both `LoginHistory` records
   - Returns error if either save fails

6. **Logs Action**
   - Records operator ID, primary user ID, and other user ID
   - Creates audit trail for compliance

## Edge Cases

### No Association Exists

If the two users are not marked as alternates:

```json
{
  "success": true,
  "primary_user_id": "...",
  "other_user_id": "...",
  "message": "No alternate association found between the two accounts",
  "operator_user_id": "..."
}
```

The operation is still considered successful (idempotent).

### User Not Found

If either user doesn't exist:

```
Error: "Failed to load login history for user_id_1: ..." 
Status: StatusNotFound (5)
```

## Error Codes

- `StatusInvalidArgument` (3): Invalid input (missing IDs, bad UUID format, same IDs)
- `StatusNotFound` (5): User not found
- `StatusInternalError` (13): Failed to save login history
- `StatusUnauthenticated` (16): Not authenticated (middleware)
- `StatusPermissionDenied` (7): Not a Global Operator (middleware)

## Example Usage

### Using nakama-cli

```bash
nakama rpc call account/break_alternates \
  --payload '{"user_id_1":"550e8400-e29b-41d4-a716-446655440000","user_id_2":"550e8400-e29b-41d4-a716-446655440001"}' \
  --session <your-session-token>
```

### Using HTTP API

```bash
curl -X POST http://localhost:7350/v2/rpc/account/break_alternates \
  -H "Authorization: Bearer <your-session-token>" \
  -H "Content-Type: application/json" \
  -d '{
    "user_id_1": "550e8400-e29b-41d4-a716-446655440000",
    "user_id_2": "550e8400-e29b-41d4-a716-446655440001"
  }'
```

## Implementation Details

### Files

- **Handler**: `server/evr_runtime_rpc_break_alternates.go`
- **Tests**: `server/evr_runtime_rpc_break_alternates_test.go`
- **Registration**: `server/evr_runtime_rpc_registration.go`

### Data Structures Modified

1. **LoginHistory.AlternateMatches**
   - Type: `map[string][]*AlternateSearchMatch`
   - Stores first-degree alternate account relationships
   - Bidirectional: Both accounts reference each other

2. **LoginHistory.SecondDegreeAlternates**
   - Type: `[]string`
   - Stores user IDs of second-degree alternates
   - Derived from first-degree alternates' alternates

### Related Functions

- `LoginHistory.LastSeen()` - Determines most recent login
- `StorableRead()` - Loads LoginHistory from storage
- `StorableWrite()` - Saves LoginHistory to storage
- `removeFromSecondDegreeAlternates()` - Helper to remove from slice

## Security Considerations

1. **Authorization**: Restricted to Global Operators only via middleware
2. **Input Validation**: All UUIDs are validated before use
3. **Audit Trail**: All operations are logged with operator and target user IDs
4. **Idempotent**: Safe to call multiple times with same parameters
5. **No Cascade**: Only removes associations between the two specified accounts
   - Does not affect relationships with other alternate accounts
   - Does not delete any account data
   - Does not affect login histories, devices, or other account properties

## Testing

Run tests with:

```bash
go test -v -vet=off ./server -run TestBreakAlternates
```

Tests cover:
- JSON marshaling and unmarshaling
- Request validation (missing fields, same UUIDs, invalid UUID formats)
- Response structure
