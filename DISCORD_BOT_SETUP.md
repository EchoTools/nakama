# Discord Bot Guild Setup Guide

This guide covers how to set up and configure the EchoVRCE Discord bot in your Discord server (guild).

## Prerequisites

- Discord server with administrative permissions
- EchoVRCE Discord bot application created and configured
- Bot token configured in your Nakama server settings

## Initial Guild Setup

### 1. Invite the Bot to Your Guild

1. Get the bot invite link from your server administrator
2. Use the Discord bot invite URL with the following permissions:
   - `bot` scope
   - `applications.commands` scope
   - Required permissions:
     - Send Messages
     - Use Slash Commands
     - Manage Roles (for automatic role assignment)
     - View Channels
     - Read Message History

3. Select your Discord server from the dropdown
4. Click "Authorize" to add the bot to your guild

### 2. Configure Guild Roles

Once the bot is in your guild, you need to configure the role mappings using the `/set-roles` command. This is a **required** setup step that defines how Discord roles map to EchoVRCE features.

#### Using the `/set-roles` Command

Run the `/set-roles` command and specify the following role mappings:

```
/set-roles member:YourMemberRole moderator:YourModeratorRole serverhost:YourServerHostRole suspension:YourSuspensionRole allocator:YourAllocatorRole is-linked:YourLinkedRole
```

**Role Definitions:**

- **member** (required): Users with this role can join social lobbies, use matchmaking, and create private matches. Users without this role can only join private matches they are specifically invited to.

- **moderator** (required): Users with this role have access to:
  - Enhanced `/lookup` command with detailed player information
  - Moderation tools for managing players and matches
  - Ability to join player sessions and kick users

- **serverhost** (required): Users with this role can host game servers for the guild.

- **suspension** (required): Users with this role are prevented from joining any guild matches. This is used for temporary or permanent suspensions.

- **allocator** (required): Users with this role can reserve and allocate game servers.

- **is-linked** (required): This role is automatically assigned/removed by the bot to indicate whether a user has linked their headset to their Discord account. **Do not manually assign this role.**

## Additional Configuration Commands

After setting up roles, you may want to configure these additional guild settings:

### Set Command Channel
Designate a specific channel for bot commands:
```
/set-command-channel
```
Run this command in the channel you want to designate for bot commands.

### Generate Link Button
Create a link button for easy headset linking:
```
/generate-button
```
This places a button in the channel that users can click to start the headset linking process.

### Set Default Lobby
Individual users can set your guild as their default lobby:
```
/set-lobby
```
Users run this command to make your Discord server their default lobby destination.

## Basic User Commands

Once the bot is configured, users can access these basic commands:

### Account Management
- `/link <link-code>` - Link headset device to Discord account using 4-character code
- `/unlink [device-link]` - Unlink headset from Discord account
- `/whoami` - View account information privately
- `/reset-password` - Clear Echo password
- `/verify` - Verify new login locations

### Game Features
- `/next-match <match-id>` - Set your next match
- `/create <mode> [region] [level]` - Create a new game session
- `/party-status` - Show your party members
- `/party group <group-name>` - Set your matchmaking group name
- `/party members` - See members of your party
- `/ign <display-name>` - Set in-game name override (use `-` to reset)
- `/jersey-number <number>` - Set your in-game jersey number
- `/throw-settings` - View your throw settings
- `/vrml-verify` - Link your VRML (Virtual Reality Master League) account

### Cosmetic Management
- `/outfits manage <action> <name>` - Manage cosmetic loadouts:
  - **save**: Save your current cosmetic setup as a named loadout
  - **load**: Apply a previously saved loadout
  - **delete**: Remove a saved loadout
- `/outfits list` - List all your saved cosmetic loadouts

### Information Commands
- `/search <pattern>` - Search for players by display name
- `/hash <token>` - Convert string to symbol hash
- `/region-status <region>` - Get game server status for a region
- `/check-server <address>` - Test if a game server is responding

## Moderator Commands

Users with the **moderator** role have access to additional moderation tools:

### Player Management
- `/lookup <user>` - Get detailed information about a player, including:
  - Account statistics and status
  - Recent activity and connections
  - Device information and linking status
  - Guild membership and roles

- `/kick-player <user> <user-notice> [suspension-duration] [moderator-notes] [allow-private-lobbies] [require-community-values]` - Kick a player's sessions with options for:
  - **user-notice**: Message displayed to the kicked user (48 character max)
  - **suspension-duration**: Temporary suspension time (e.g., 1m, 2h, 3d, 4w)
  - **moderator-notes**: Internal notes for other moderators
  - **allow-private-lobbies**: Limit user to private lobbies only
  - **require-community-values**: Require user to accept community values before rejoining

- `/join-player <user> <reason>` - Join a player's game session as a moderator for supervision or assistance

### Match Management
- `/shutdown-match <match_id> <reason> [disconnect_game_server] [grace_seconds]` - Shutdown a match or game server:
  - **match_id**: Match ID or Game Server ID to shutdown
  - **reason**: Reason for the shutdown (logged)
  - **disconnect_game_server**: Whether to disconnect the server completely
  - **grace_seconds**: Seconds to wait before forcing shutdown

### Advanced Tools
- `/igp` - Enable the gold nametag (In-Game Panel) for your current session to identify yourself as a moderator in-game

- `/badges assign <user> <badges>` - Assign badges to a player (comma-separated list like "p,1,2,5c,6f")

- `/badges link-player <user> <player-id> [force]` - Manually link a VRML player page to a Discord user

- `/stream-list <mode> <subject> [subcontext] [label] [limit]` - List presences for a specific stream (advanced debugging)

### Server Management
- `/allocate <mode> [region] [level]` - Allocate a session on a game server in a specific region
- `/region-status <region>` - Get detailed status of game servers in a region

## Best Practices

### Role Setup
1. Create dedicated roles for each required mapping rather than using existing roles
2. Ensure the bot's role is positioned above the roles it needs to manage
3. Test role assignments with a few users before full deployment

### Moderation
1. Always provide clear reasons when using moderation commands
2. Use appropriate suspension durations - start with shorter periods
3. Document moderation actions in your guild's moderation log channel
4. Coordinate with other moderators to avoid conflicting actions

### Guild Management
1. Set up a dedicated bot commands channel to keep other channels clean
2. Pin important messages about bot usage and linking instructions
3. Regularly monitor the command log channel (if configured) for unusual activity
4. Keep moderator permissions limited to trusted members only

## Troubleshooting

### Bot Not Responding
- Verify the bot is online and has proper permissions
- Check that required roles are configured with `/set-roles`
- Ensure the bot's role hierarchy allows it to assign other roles

### Users Can't Link Headsets
- Verify users have the appropriate role permissions
- Check that the linking process is properly explained to users
- Use `/generate-button` to provide an easy linking interface

### Moderation Commands Not Working
- Confirm the user has the moderator role as defined in `/set-roles`
- Verify the target user is in the guild and bot has permissions to act on them
- Check command syntax and required parameters

## Support

For technical issues or questions about bot functionality, refer to your server administrator or the EchoVRCE support channels.