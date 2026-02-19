# Discord Linked Roles Implementation - Manual Validation Guide

## Overview
This implementation adds Discord Linked Roles functionality to Nakama with custom requirement options for Echo VR players.

## Requirements Implemented
- **Has at least one headset connected to their account**: Uses existing `EVRProfile.IsLinked()` function
- **Has played echo**: Uses existing `HasLoggedIntoEcho()` function

## API Endpoint
**URL**: `/discord/linked-roles/metadata`  
**Method**: `GET`  
**Authorization**: Bearer token (Discord OAuth access token)

### Response Format
```json
{
  "platform_name": "Echo VR",
  "platform_username": "player_display_name",
  "has_headset": true,
  "has_played_echo": true
}
```

## Manual Validation Steps

### 1. Basic Server Testing

#### Start the server:
```bash
# Start PostgreSQL
docker compose up -d postgres

# Wait for database to start
sleep 10

# Run migrations
./nakama-debug migrate up --database.address postgres:localdb@127.0.0.1:5432/nakama

# Start server
./nakama-debug --database.address postgres:localdb@127.0.0.1:5432/nakama --logger.level INFO
```

#### Test endpoint registration:
```bash
# Test CORS preflight
curl -X OPTIONS http://localhost:7350/discord/linked-roles/metadata -v

# Expected response: 200 OK with CORS headers

# Test method validation
curl -X POST http://localhost:7350/discord/linked-roles/metadata -v

# Expected response: 405 Method Not Allowed

# Test authentication requirement
curl -X GET http://localhost:7350/discord/linked-roles/metadata -v

# Expected response: 401 Unauthorized - Missing Authorization header
```

### 2. Discord Application Setup

#### Create Discord Application:
1. Go to https://discord.com/developers/applications
2. Create a new application
3. Go to "OAuth2" section
4. Note your Client ID and Client Secret

#### Configure Linked Roles:
1. In your Discord application, go to "Linked Roles"
2. Add metadata schema:
   - `has_headset` (boolean): "Has VR headset connected"
   - `has_played_echo` (boolean): "Has played Echo VR"
3. Set the metadata URL to your server: `https://your-server.com/discord/linked-roles/metadata`

### 3. OAuth Flow Testing

#### Get access token:
```bash
# Step 1: Get authorization code (replace YOUR_CLIENT_ID)
curl "https://discord.com/api/oauth2/authorize?client_id=YOUR_CLIENT_ID&redirect_uri=http://localhost:8080&response_type=code&scope=identify%20role_connections.write"

# Step 2: Exchange code for access token (replace values)
curl -X POST "https://discord.com/api/oauth2/token" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "client_id=YOUR_CLIENT_ID&client_secret=YOUR_CLIENT_SECRET&grant_type=authorization_code&code=YOUR_CODE&redirect_uri=http://localhost:8080"
```

#### Test with real access token:
```bash
# Replace YOUR_ACCESS_TOKEN with the token from above
curl -X GET http://localhost:7350/discord/linked-roles/metadata \
  -H "Authorization: Bearer YOUR_ACCESS_TOKEN" \
  -H "Content-Type: application/json"

# Expected response:
# - 200 OK with user metadata if user exists in Nakama
# - 200 OK with default values if user not found
```

### 4. User Data Testing

#### Create test users with linked headsets:
```bash
# Connect to Nakama console at http://localhost:7351
# Default credentials: admin / password

# Create a user and link a headset through the EVR system
# Test that has_headset returns true for linked users
```

#### Test login history:
```bash
# Ensure users have login history in the system
# Test that has_played_echo returns true for users who have logged in
```

## Expected Behaviors

### For New Users (not in Nakama):
```json
{
  "platform_name": "Echo VR",
  "has_headset": false,
  "has_played_echo": false
}
```

### For Users with Linked Headset:
```json
{
  "platform_name": "Echo VR", 
  "platform_username": "PlayerDisplayName",
  "has_headset": true,
  "has_played_echo": true
}
```

### For Users without Headset:
```json
{
  "platform_name": "Echo VR",
  "platform_username": "PlayerUsername", 
  "has_headset": false,
  "has_played_echo": true
}
```

## Troubleshooting

### Common Issues:

1. **401 Unauthorized**: Check Discord access token validity
2. **404 Not Found**: Verify endpoint registration in server logs
3. **500 Internal Server Error**: Check database connection and user data
4. **CORS Issues**: Verify CORS headers in response

### Debug Commands:
```bash
# Check if endpoint is registered
grep "Registered custom HTTP handler" server_logs.txt

# Check Discord API calls
grep "Failed to get Discord user ID" server_logs.txt

# Check user lookups
grep "Failed to find user by Discord ID" server_logs.txt
```

## Integration with Discord

### Setting Up Role Requirements:
1. In Discord server settings, create roles
2. Set up role requirements using the metadata fields:
   - `has_headset` = true (for VR player role)
   - `has_played_echo` = true (for Echo player role)
   - Both = true (for verified Echo VR player role)

### User Flow:
1. User runs `/link` command in Discord to connect account
2. User goes through OAuth flow and connects their Discord
3. Discord calls the metadata endpoint to check requirements
4. Discord automatically assigns/removes roles based on metadata
5. Roles update in real-time as user's headset/play status changes

## Security Considerations

- Access tokens are validated against Discord's API
- User data is only returned for valid Discord users
- No sensitive information is exposed in metadata
- CORS is properly configured for web integration
- All database queries use prepared statements

## Performance Notes

- Endpoint includes proper caching headers
- Database queries are optimized for user lookup
- Discord API calls are rate-limited appropriately
- Error handling prevents cascading failures