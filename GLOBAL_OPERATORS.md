# Global Operators

Global Operators are members of the `Global Operators` system group. This role provides comprehensive administrative capabilities that bypass normal guild-level restrictions.

## Permissions and Abilities

### Discord Bot Commands

Global Operators can use all Discord slash commands without guild-level role restrictions:

#### Match Management Commands
- **`/allocate`** - Allocate new matches
  - Normally requires Guild Allocator role
  - Global Operators can create matches in any guild

- **`/shutdown-match`** - Shut down running matches
  - Normally requires Guild Enforcer role
  - Global Operators can shut down matches on any server, regardless of ownership

#### Player Management Commands
- **`/join-player`** - Join a player's match
  - Normally requires Guild Enforcer role
  - Global Operators can force joins across all guilds

- **`/kick-player`** - Remove a player from a match
  - Normally requires Guild Enforcer role
  - Global Operators can kick players from matches across all guilds

- **`/igp`** - In-game player commands
  - Normally requires Guild Enforcer role
  - Execute player-related commands during active matches

- **`/ign`** - In-game name commands
  - Normally requires Guild Enforcer role
  - Manage player display names and identities

### Enhanced User Profile Access

When viewing user profiles through Discord bot commands, Global Operators have access to:

#### `/lookup` Command
- **VRML History Embed** - Complete match history and statistics
- **Alternates Embed** - Information about alternate accounts
- **Full IP Addresses** - Unredacted IP address information
- **All Guild Information** - User's membership across all guilds
- **30-Day Login History** - Detailed login activity
- **File Export on Error** - Fallback format for `/lookup` errors

#### `/search` Command
- **Enhanced Search Results** - Access to comprehensive player data across all guilds
- **Cross-Guild Player Discovery** - Can search for players regardless of guild membership
- **Advanced Filtering** - Additional search parameters and filters not available to standard users

### Server and Match Operations

#### Match Preparation
- Can `/allocate` matches on any server through RPC calls
- Access to `PrepareMatchRPC` RPC
- No restrictions based on server ownership

#### Player Enforcement
- Can disconnect players from matches
- Enforcement actions are logged with global operator attribution
- Can perform suspensions and other disciplinary actions across all guilds

### System-Level Capabilities

#### Cross-Guild Operations
- Bypass all guild-level role requirements (Allocator, Enforcer, Auditor)
- Perform administrative actions in any guild regardless of membership
- Access to guild audit channels and logging

## Security Considerations

- Global Operators have significant system-wide privileges
- All actions are logged and auditable
- Commands are logged to designated audit channels
- IP address access and sensitive data viewing is restricted to this role
- Cross-guild enforcement capabilities require careful oversight