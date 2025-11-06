# Game Server Registration URL Parameters

This document describes all URL parameters that are utilized in the `gameserverRegistrationRequest` function in `server/evr_pipeline_gameserver.go`.

## Overview

Game servers connect to Nakama using WebSocket connections with various URL parameters that configure authentication, server settings, and features. These parameters are parsed during session initialization and used throughout the game server registration process.

## URL Parameter Reference

### Authentication Parameters

#### `discordid` / `discord_id`
- **Type**: String
- **Max Length**: 20 characters
- **Pattern**: `^[0-9]+$` (numeric only)
- **Description**: Discord user ID for authenticating the game server operator
- **Usage**: Used in conjunction with `password` for WebSocket authentication
- **Example**: `wss://example.com/ws?discordid=123456789012345678`

#### `password`
- **Type**: String
- **Max Length**: 32 characters
- **Pattern**: No restriction
- **Description**: Authentication password for the Discord user
- **Usage**: Required when `discordid` is provided for authentication
- **Example**: `wss://example.com/ws?discordid=123456789012345678&password=mypassword`

### Server Configuration Parameters

#### `serveraddr`
- **Type**: String
- **Max Length**: 64 characters
- **Format**: `IP:port` or `hostname:port`
- **Description**: External server address that clients should connect to
- **Usage**: Overrides the automatically detected external IP address
- **Example**: `wss://example.com/ws?serveraddr=203.0.113.1:6792`
- **Note**: If not provided, the system will use the client's IP address as the external IP

#### `tags`
- **Type**: Comma-delimited string list
- **Max Length**: 32 characters per tag
- **Pattern**: `^[-.A-Za-z0-9_:]+$` per tag
- **Description**: Server tags used for server identification and filtering
- **Usage**: Used in match creation and server discovery
- **Example**: `wss://example.com/ws?tags=competitive,ranked,na-west`
- **Special Tags**:
  - `novalidation` - Skips connectivity validation during registration. For testing purposes only. When used:
    - Initial healthcheck is skipped, allowing the server to register even if unreachable
    - An audit message is sent to all guild channels the server is hosting for
    - A Discord DM is sent to the server owner warning about testing-only usage
    - The server will continue to be monitored with ping checks, and failures will generate Discord DMs to the owner
    - Example: `wss://example.com/ws?tags=novalidation,test-server`

#### `guilds`
- **Type**: Comma-delimited string list
- **Max Length**: 32 characters per guild ID
- **Pattern**: `^[0-9]+$` per guild ID (numeric only)
- **Description**: List of Discord guild IDs that this server should host for
- **Usage**: Restricts server to specific guilds unless "any" is specified
- **Example**: `wss://example.com/ws?guilds=123456789012345678,987654321098765432`
- **Special Values**: 
  - `any` - Server hosts for all guilds
  - Empty - Server hosts for all guilds (default behavior)

#### `regions`
- **Type**: Comma-delimited string list
- **Max Length**: 32 characters per region
- **Pattern**: `^[-A-Za-z0-9_]+$` per region  
- **Description**: Geographic regions this server should be available in
- **Usage**: Used for region-based matchmaking and server discovery
- **Example**: `wss://example.com/ws?regions=us-west,us-central,default`
- **Note**: Limited to first 10 regions, region codes truncated to 32 characters

### Feature Management Parameters

#### `features`
- **Type**: Comma-delimited string list
- **Max Length**: 32 characters per feature
- **Pattern**: `^[a-z0-9_]+$` per feature
- **Description**: List of features supported by this game server
- **Usage**: Used for feature-based matchmaking and compatibility checks
- **Example**: `wss://example.com/ws?features=spectate,replay,tournaments`

#### `requires`
- **Type**: Comma-delimited string list  
- **Max Length**: 32 characters per feature
- **Pattern**: `^[a-z0-9_]+$` per feature
- **Description**: List of features required by this game server
- **Usage**: Required features are automatically added to supported features
- **Example**: `wss://example.com/ws?requires=anticheat,voice_chat`

### Display and Personalization Parameters

#### `ign`
- **Type**: String
- **Max Length**: 20 characters
- **Pattern**: No restriction
- **Description**: In-game name or display name override for the server operator
- **Usage**: Overrides the default display name
- **Example**: `wss://example.com/ws?ign=MyServerName`
- **Special Values**:
  - `randomize` - Generates a random display name

### Location and Networking Parameters

#### `geo_precision`
- **Type**: Integer (as string)
- **Range**: 0-12
- **Default**: 8
- **Description**: Geohash precision level for location-based features
- **Usage**: Controls granularity of geographic location data
- **Example**: `wss://example.com/ws?geo_precision=6`
- **Note**: Higher values provide more precise location data

### Security Parameters

#### `disable_encryption`
- **Type**: Boolean
- **Values**: `"true"` or omitted
- **Description**: Disables encryption for the connection
- **Usage**: Should only be used for testing/debugging
- **Example**: `wss://example.com/ws?disable_encryption=true`
- **Warning**: Not recommended for production use

#### `disable_mac`
- **Type**: Boolean  
- **Values**: `"true"` or omitted
- **Description**: Disables Message Authentication Code (MAC)
- **Usage**: Should only be used for testing/debugging
- **Example**: `wss://example.com/ws?disable_mac=true`
- **Warning**: Not recommended for production use

### Debug and Monitoring Parameters

#### `verbose`
- **Type**: Boolean
- **Values**: `"true"` or omitted
- **Description**: Enables verbose logging and message relaying
- **Usage**: Causes some outgoing messages to be relayed via Discord
- **Example**: `wss://example.com/ws?verbose=true`

#### `debug`
- **Type**: Boolean
- **Values**: `"true"` or omitted  
- **Description**: Enables debug mode with additional logging
- **Usage**: Provides extra debug information in logs
- **Example**: `wss://example.com/ws?debug=true`

## Parameter Validation

All URL parameters undergo validation during parsing:

1. **Length Validation**: Parameters exceeding max length are truncated
2. **Pattern Validation**: Parameters must match their specified regex patterns
3. **Type Validation**: Boolean parameters must be exactly `"true"` to be enabled
4. **List Processing**: Comma-delimited lists are split, validated individually, and sorted

## Usage Examples

### Basic Game Server Connection
```
wss://nakama.example.com/ws?discordid=123456789012345678&password=mypassword
```

### Advanced Configuration
```
wss://nakama.example.com/ws?discordid=123456789012345678&password=mypassword&serveraddr=203.0.113.1:6792&tags=competitive,ranked&guilds=123456789012345678&regions=us-west,us-central&features=spectate,replay&geo_precision=6
```

### Debug Setup
```
wss://nakama.example.com/ws?discordid=123456789012345678&password=mypassword&debug=true&verbose=true&ign=TestServer
```

### Local Testing with No Validation
```
wss://nakama.example.com/ws?discordid=123456789012345678&password=mypassword&tags=novalidation&serveraddr=localhost:6792
```
**Note**: The `novalidation` tag is for testing purposes only. It skips connectivity validation during registration but the server will still be monitored for connectivity issues.

## Implementation Details

The URL parameters are processed in the following locations:

- **Parsing**: `server/session_ws.go` - `NewSessionWS()` function (lines 173-205)
- **Storage**: `server/evr_session_parameters.go` - `SessionParameters` struct
- **Usage**: `server/evr_pipeline_gameserver.go` - `gameserverRegistrationRequest()` function

The parameters are stored in the session context and accessed throughout the game server registration and operation lifecycle.