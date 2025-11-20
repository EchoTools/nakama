# Enforcement Journal Query API

## Overview

The Enforcement Journal Query API provides access to enforcement journal entries for a specified player with fine-grained privacy controls. The API applies access controls based on the requester's guild memberships and privilege levels.

## Endpoint

**RPC Function Name:** `enforcement/journal/query`

**Authentication:** Required (Bearer token with valid user session)

## Request Format

### HTTP Request

```
POST /v2/rpc/enforcement/journal/query
Authorization: Bearer <session_token>
Content-Type: application/json
```

### Request Body (JSON)

```json
{
  "user_id": "nakama-user-id",
  "group_ids": ["group-id-1", "group-id-2"]
}
```

#### Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `user_id` | string | Yes | The Nakama user ID of the player whose enforcement journal to query |
| `group_ids` | array of strings | No | Optional list of guild group IDs to filter results. If omitted, returns entries for all guilds the requester has access to |

## Response Format

### Success Response (200 OK)

```json
{
  "user_id": "nakama-user-id",
  "entries": [
    {
      "id": "record-uuid",
      "user_id": "nakama-user-id",
      "group_id": "guild-group-id",
      "enforcer_user_id": "enforcer-nakama-id",
      "enforcer_discord_id": "discord-id",
      "created_at": "2024-01-01T00:00:00Z",
      "updated_at": "2024-01-01T00:00:00Z",
      "suspension_notice": "User-facing violation notice",
      "suspension_expiry": "2024-01-08T00:00:00Z",
      "community_values_required": false,
      "notes": "Internal moderator notes",
      "allow_private_lobbies": true,
      "is_voided": false,
      "voided_at": null,
      "voided_by_user_id": null,
      "voided_by_discord_id": null,
      "void_notes": null
    }
  ]
}
```

#### Response Fields

| Field | Type | Always Present | Description |
|-------|------|----------------|-------------|
| `user_id` | string | Yes | The user ID that was queried |
| `entries` | array | Yes | Array of enforcement entries (may be empty) |
| `entries[].id` | string | Yes | Unique identifier for the enforcement record |
| `entries[].user_id` | string | Yes | User ID of the player who received the enforcement action |
| `entries[].group_id` | string | Yes | Guild group ID where the enforcement was issued |
| `entries[].enforcer_user_id` | string | Conditional | Nakama user ID of the moderator who issued the enforcement (only if requester has auditor privilege for this guild) |
| `entries[].enforcer_discord_id` | string | Conditional | Discord ID of the moderator who issued the enforcement (only if requester has auditor privilege for this guild) |
| `entries[].created_at` | string (RFC3339) | Yes | Timestamp when the enforcement was created |
| `entries[].updated_at` | string (RFC3339) | Yes | Timestamp when the enforcement was last updated |
| `entries[].suspension_notice` | string | Yes | User-facing notice text displayed to the player |
| `entries[].suspension_expiry` | string (RFC3339) | Conditional | Expiry timestamp for suspensions (omitted for warnings/kicks) |
| `entries[].community_values_required` | boolean | Yes | Whether the player must complete community values training |
| `entries[].notes` | string | Conditional | Internal moderator notes (only if requester has auditor privilege for this guild) |
| `entries[].allow_private_lobbies` | boolean | Yes | Whether the player can join private lobbies during suspension |
| `entries[].is_voided` | boolean | Yes | Whether this enforcement has been voided |
| `entries[].voided_at` | string (RFC3339) | Conditional | Timestamp when the enforcement was voided (only if voided) |
| `entries[].voided_by_user_id` | string | Conditional | User ID who voided the enforcement (only if voided and requester has auditor privilege) |
| `entries[].voided_by_discord_id` | string | Conditional | Discord ID who voided the enforcement (only if voided and requester has auditor privilege) |
| `entries[].void_notes` | string | Conditional | Notes explaining why the enforcement was voided (only if voided and requester has auditor privilege) |

### Error Responses

| Status Code | Error Code | Description |
|-------------|------------|-------------|
| 401 | 16 (StatusUnauthenticated) | No valid authentication token provided |
| 400 | 3 (StatusInvalidArgument) | Invalid request payload or missing required `user_id` field |
| 403 | 7 (StatusPermissionDenied) | Requester does not have access to any enforcement logs for the requested guilds |
| 404 | 5 (StatusNotFound) | User not found (returns empty entries array instead of error) |
| 500 | 13 (StatusInternalError) | Internal server error |

## Privacy and Access Control

### Guild-Based Filtering

- Requesters can only view enforcement entries for guilds they are members of
- Entries for guilds the requester is not a member of are completely excluded
- If `group_ids` is specified, only entries for those guilds are returned (subject to requester's access)
- If `group_ids` is omitted, entries for all guilds the requester has access to are returned

### Privilege-Based Field Redaction

The following fields are considered privileged information and are only included when the requester has **auditor** privilege for the specific guild:

- `enforcer_user_id`
- `enforcer_discord_id`
- `notes` (moderator's internal notes)
- `voided_by_user_id` (for voided entries)
- `voided_by_discord_id` (for voided entries)
- `void_notes` (for voided entries)

Non-auditor members can see:
- All public enforcement details (notice, expiry, created/updated timestamps)
- That an entry is voided and when (`is_voided`, `voided_at`)
- But NOT who voided it or why

### Global Operator Access

Users with global private data access (system group membership) can:
- View enforcement logs for any user
- View all guilds
- See all privileged fields for all guilds

## Example Usage

### Example 1: Query All Accessible Enforcement Logs

**Request:**
```json
{
  "user_id": "8e3c7f4a-1234-5678-9abc-def012345678"
}
```

**Response:**
```json
{
  "user_id": "8e3c7f4a-1234-5678-9abc-def012345678",
  "entries": [
    {
      "id": "f1e2d3c4-5678-90ab-cdef-1234567890ab",
      "user_id": "8e3c7f4a-1234-5678-9abc-def012345678",
      "group_id": "guild-alpha",
      "created_at": "2024-01-15T10:30:00Z",
      "updated_at": "2024-01-15T10:30:00Z",
      "suspension_notice": "Temporary suspension for team damage",
      "suspension_expiry": "2024-01-22T10:30:00Z",
      "community_values_required": true,
      "allow_private_lobbies": false,
      "is_voided": false
    },
    {
      "id": "a1b2c3d4-5678-90ab-cdef-fedcba098765",
      "user_id": "8e3c7f4a-1234-5678-9abc-def012345678",
      "group_id": "guild-beta",
      "enforcer_user_id": "moderator-user-id",
      "enforcer_discord_id": "123456789012345678",
      "created_at": "2024-01-10T14:20:00Z",
      "updated_at": "2024-01-10T14:20:00Z",
      "suspension_notice": "Warning for inappropriate language",
      "community_values_required": false,
      "notes": "First offense, player apologized",
      "allow_private_lobbies": true,
      "is_voided": false
    }
  ]
}
```

In this example:
- The requester is a member of both `guild-alpha` and `guild-beta`
- The requester has auditor privilege in `guild-beta` but not in `guild-alpha`
- For `guild-alpha`: privileged fields (`enforcer_user_id`, `enforcer_discord_id`, `notes`) are omitted
- For `guild-beta`: all fields including privileged information are included

### Example 2: Query Specific Guilds

**Request:**
```json
{
  "user_id": "8e3c7f4a-1234-5678-9abc-def012345678",
  "group_ids": ["guild-alpha"]
}
```

**Response:**
```json
{
  "user_id": "8e3c7f4a-1234-5678-9abc-def012345678",
  "entries": [
    {
      "id": "f1e2d3c4-5678-90ab-cdef-1234567890ab",
      "user_id": "8e3c7f4a-1234-5678-9abc-def012345678",
      "group_id": "guild-alpha",
      "created_at": "2024-01-15T10:30:00Z",
      "updated_at": "2024-01-15T10:30:00Z",
      "suspension_notice": "Temporary suspension for team damage",
      "suspension_expiry": "2024-01-22T10:30:00Z",
      "community_values_required": true,
      "allow_private_lobbies": false,
      "is_voided": false
    }
  ]
}
```

### Example 3: Voided Entry (Auditor View)

**Response Entry:**
```json
{
  "id": "voided-record-id",
  "user_id": "8e3c7f4a-1234-5678-9abc-def012345678",
  "group_id": "guild-beta",
  "enforcer_user_id": "original-moderator-id",
  "enforcer_discord_id": "123456789012345678",
  "created_at": "2024-01-05T09:00:00Z",
  "updated_at": "2024-01-05T09:00:00Z",
  "suspension_notice": "Original violation notice",
  "suspension_expiry": "2024-01-12T09:00:00Z",
  "community_values_required": false,
  "notes": "Original moderator notes",
  "allow_private_lobbies": true,
  "is_voided": true,
  "voided_at": "2024-01-06T11:00:00Z",
  "voided_by_user_id": "senior-moderator-id",
  "voided_by_discord_id": "987654321098765432",
  "void_notes": "Suspension voided - player was not at fault"
}
```

### Example 4: Voided Entry (Non-Auditor View)

**Response Entry:**
```json
{
  "id": "voided-record-id",
  "user_id": "8e3c7f4a-1234-5678-9abc-def012345678",
  "group_id": "guild-alpha",
  "created_at": "2024-01-05T09:00:00Z",
  "updated_at": "2024-01-05T09:00:00Z",
  "suspension_notice": "Original violation notice",
  "suspension_expiry": "2024-01-12T09:00:00Z",
  "community_values_required": false,
  "allow_private_lobbies": true,
  "is_voided": true,
  "voided_at": "2024-01-06T11:00:00Z"
}
```

Note: Privileged void information (`voided_by_user_id`, `voided_by_discord_id`, `void_notes`) is omitted.

### Example 5: Permission Denied

**Request:**
```json
{
  "user_id": "some-user-id",
  "group_ids": ["guild-xyz"]
}
```

**Response (403 Forbidden):**
```json
{
  "error": "no access to enforcement logs for the requested guilds",
  "message": "no access to enforcement logs for the requested guilds",
  "code": 7
}
```

### Example 6: No Enforcement Journal

**Request:**
```json
{
  "user_id": "user-with-no-records"
}
```

**Response (200 OK):**
```json
{
  "user_id": "user-with-no-records",
  "entries": []
}
```

## Integration Examples

### JavaScript/TypeScript

```typescript
async function queryEnforcementJournal(
  client: Client,
  userId: string,
  groupIds?: string[]
): Promise<EnforcementJournalQueryResponse> {
  const request = {
    user_id: userId,
    group_ids: groupIds
  };

  const response = await client.rpc(
    'enforcement/journal/query',
    JSON.stringify(request)
  );

  return JSON.parse(response.payload) as EnforcementJournalQueryResponse;
}

// Usage
const journal = await queryEnforcementJournal(
  client,
  '8e3c7f4a-1234-5678-9abc-def012345678',
  ['guild-alpha', 'guild-beta']
);

console.log(`Found ${journal.entries.length} enforcement entries`);
```

### cURL

```bash
curl -X POST https://your-nakama-server.com/v2/rpc/enforcement/journal/query \
  -H "Authorization: Bearer <session_token>" \
  -H "Content-Type: application/json" \
  -d '{
    "user_id": "8e3c7f4a-1234-5678-9abc-def012345678",
    "group_ids": ["guild-alpha"]
  }'
```

## Security Considerations

1. **Authentication Required**: All requests must include a valid session token
2. **Guild Membership Validation**: The system verifies the requester is a member of each guild before returning data
3. **Privilege-Level Filtering**: Sensitive fields are automatically redacted based on the requester's role in each specific guild
4. **No Cross-Guild Information Leakage**: Enforcers in one guild cannot see privileged details from other guilds
5. **Audit Trail**: All queries are logged with the requester's user ID for security auditing

## Edge Cases

### Multiple Guild Memberships with Different Privileges

If a requester is:
- An auditor in Guild A
- A regular member in Guild B
- Not a member of Guild C

Then for a query of all three guilds:
- Guild A entries will include all privileged fields
- Guild B entries will exclude privileged fields
- Guild C entries will not appear at all

### Voided vs. Expired Entries

- **Voided entries** are explicitly invalidated by a moderator and marked with `is_voided: true`
- **Expired entries** are suspensions where `suspension_expiry` is in the past
- Both types of entries are returned in queries
- The `is_voided` field can be used to distinguish voided entries
- Clients should check both `is_voided` and compare `suspension_expiry` to determine if an entry is currently active

## Change History

- **v1.0.0** (2024-01-20): Initial implementation with guild-based filtering and privilege-level field redaction
