# In-Game Panel (IGP) Discord Command

The `/igp` command provides Discord-based moderators with a real-time in-game panel for monitoring and managing players during active matches. This command creates an interactive interface that tracks the moderator's game session and displays current match information, player lists, and moderation tools.

## Overview

**Command**: `/igp`

**Description**: Enable the In-Game Panel to monitor and moderate your current match session

**Permissions Required**: Guild Enforcer role or Global Operator status

**Primary Use Case**: Real-time match moderation and player management while in-game

## How It Works

When a moderator invokes `/igp`, the system creates an ephemeral (private) interactive message that:

1. Waits for the moderator to join a game session
2. Automatically tracks when the moderator joins a match
3. Displays all players in the current match with team information
4. Provides interactive moderation controls
5. Updates automatically every 3 seconds while the match is active
6. Maintains a history of recent players (last 3 minutes)

## Interactive Features

### Player List Display

The IGP shows an embedded list of all players with the following information:

- **Discord Avatar & Username**: Visual identification of the player
- **In-Game Display Name**: The player's current IGN
- **Team Assignment**: Color-coded by team:
  - 🔵 Blue Team (Blue color)
  - 🟠 Orange Team (Orange color)
  - 🟣 Social Lobby Participant (Purple color)
  - ⚪ Recent Players (White/Gray - recently left)
  - 🟡 Moderator (Yellow color)
  - ⚫ Spectator (Gray color)
- **Account Details**: Username and EchoVR ID

### Match Information

- **Current Match Link**: Clickable link to the match session (`spark://c/MATCH_ID`)
- **Match Mode**: Displays the current game mode (e.g., Arena Public, Social Lobby)
- **Auto-Refresh**: Updates every 3 seconds to reflect current players

### Interactive Controls

#### 1. Set IGN Button

**Label**: "Set IGN"

**Purpose**: Opens a modal to change the moderator's own in-game name

**Functionality**:
- Opens a text input modal
- Pre-populates with current display name
- Allows moderator to update their IGN for the current guild
- Changes are saved immediately upon submission

**Example Use**: Quickly change your display name while in a match

#### 2. Player Selection Dropdown

**Placeholder**: "<select a player to moderate>"

**Purpose**: Select a player to perform moderation actions

**Options**:
- All currently active players in the match
- Recent players who left within the last 3 minutes
- Each option shows:
  - Display Name (Label)
  - Username / EchoVR ID (Description)
  - Team indicator emoji

**Behavior**:
- Selecting a player opens the **Moderation Modal**
- Selected player is highlighted in the dropdown for reference

#### 3. Moderation Modal

When a player is selected, a modal appears with the following fields:

**Title**: "Moderate [Player Name]"

**Fields**:

1. **Duration** (Optional)
   - **Label**: "Duration (15m,1h,1d,1w); 0 to void existing."
   - **Format**: Number followed by unit (m=minutes, h=hours, d=days, w=weeks)
   - **Purpose**: Set suspension duration
   - **Special Value**: `0` voids (cancels) existing active suspensions
   - **Examples**: `15m`, `1h`, `2d`, `1w`

2. **Reason (User Visible)** (Required)
   - **Label**: "Reason (user visible)"
   - **Max Length**: 45 characters
   - **Purpose**: Reason displayed to the suspended player
   - **Required**: Yes

3. **Notes (Moderator Only)** (Optional)
   - **Label**: "Notes (mod only)"
   - **Max Length**: 200 characters
   - **Purpose**: Internal notes for the moderation team
   - **Style**: Paragraph text input

**Actions**:
- **Kick Only**: Leave duration blank to kick the player without suspension
- **Kick + Suspend**: Enter duration to kick and apply timed suspension
- **Void Suspension**: Enter `0` as duration to remove active suspensions

## Special Behaviors

### Gold Nametag (Moderator Role)

When a guild has `EnforcersHaveGoldNames` enabled and an enforcer opens the IGP:
- The moderator's in-game nametag becomes **gold/yellow**
- This visually identifies them as a moderator to other players
- The gold nametag persists as long as the IGP session is active

### Matchmaking Adjustment

When the IGP is open:
- The moderator's matchmaking division is automatically set to "green" (beginner)
- This allows enforcers to join any match for moderation purposes
- Normal division restrictions are bypassed

### Session Tracking

The IGP automatically:
- Detects when you log into the game
- Tracks when you join a match
- Updates the player list every 3 seconds
- Maintains a 3-minute history of recent players
- Times out after 3 minutes of inactivity

## Permissions and Requirements

### Required Permissions

To use `/igp`, you must have **one of**:
- **Guild Enforcer Role**: Assigned in the guild's role configuration
- **Global Operator Status**: System-wide admin access

### Enforcement Capabilities

With enforcer permissions, you can:
- Kick players from matches in your guild
- Apply timed suspensions (minutes, hours, days, weeks)
- Void existing suspensions
- Set IGN overrides for yourself
- View all players in your current match

**Note**: Actions are limited to matches hosted by your guild or matches you spawned privately.

## Rate Limits and Timeouts

### Session Timeout: 3 Minutes

The IGP has three timeout stages:

1. **Waiting for Login Session** (3 minutes)
   - IGP waits for you to log into the game
   - If no session is detected within 3 minutes, IGP closes
   - Message: "Session timed out. Please reopen the IGP."

2. **Waiting for Match Join** (3 minutes)
   - After login detected, waits for you to join a match
   - Resets to 3 minutes when you join a match
   - Continues as long as you remain in matches

3. **Recent Player Tracking** (3 minutes)
   - Players are tracked for 3 minutes after leaving the match
   - Allows moderation of recently departed players
   - Useful for handling players who leave before moderation

### Refresh Interval: 3 Seconds

- Player list updates every 3 seconds
- Automatically reflects players joining/leaving
- Match information stays current

### Audit Logging

All moderation actions are logged to:
- **Enforcement Notice Channel**: Detailed action records with target, reason, duration
- **Audit Channel**: Summary of actions for review
- Logs include: enforcer name, target player, action type, reason, timestamps

## Usage Examples

### Example 1: Basic Moderation Flow

```
1. Moderator types `/igp` in Discord
2. Discord shows: "Loading IGP..."
3. Moderator launches the game
4. Discord updates: "Session found! Waiting for player to join a match..."
5. Moderator joins a match
6. IGP displays:
   - Match link: "Currently in Arena Public"
   - Player list with teams and names
   - "Set IGN" button
   - Player selection dropdown
7. Moderator selects a disruptive player from the dropdown
8. Modal opens for moderation
9. Moderator enters:
   - Duration: "1h"
   - Reason: "Unsportsmanlike conduct"
   - Notes: "Targeted harassment of new players"
10. Player is kicked and suspended for 1 hour
11. Enforcement notice posted to mod channel
```

### Example 2: Voiding a Suspension

```
1. Moderator has IGP open
2. Selects a suspended player
3. In the modal, enters:
   - Duration: "0"
   - Reason: "Appeal approved"
   - Notes: "Reviewed video evidence, suspension voided"
4. Existing suspension is cancelled
5. Player can rejoin matches immediately
```

### Example 3: Quick Kick (No Suspension)

```
1. Moderator selects a player
2. Leaves "Duration" field empty
3. Enters:
   - Reason: "AFK during match"
4. Player is kicked from the match only
5. No suspension is applied
6. Player can rejoin immediately
```

### Example 4: Changing Your IGN

```
1. Moderator clicks "Set IGN" button
2. Modal shows current display name
3. Moderator enters new name: "Mod_Guardian"
4. Name is updated immediately
5. New name appears in-game in current session
```

## Technical Details

### Session States

The IGP progresses through these states:

1. **Initializing**: "Loading IGP..."
2. **Waiting for Session**: "Waiting for player session..."
3. **Waiting for Match**: "Session found! Waiting for player to join a match..."
4. **Active**: Displaying match and player information
5. **Timeout**: "Session timed out. Please try again."

### Match Tracking

The IGP uses Nakama's presence system to:
- Monitor your login service stream
- Detect when you join a match service stream
- Query match labels for player information
- Track player teams and roles

### Suspension System

Suspensions are:
- Stored in the player's enforcement journal
- Guild-specific (per group ID)
- Can inherit to child guilds if configured
- Include metadata: enforcer ID, reason, duration, timestamps
- Voided by setting duration to `0`

## Integration with Other Systems

### Discord Roles

The IGP respects Discord role hierarchy:
- Guild Enforcer role grants moderation powers
- Guild Auditor role allows IGN override management
- Global Operators have all permissions

### Enforcement Notices

Actions trigger notifications in configured channels:
- **Enforcement Notice Channel**: Full moderation details (moderator-only)
- **Audit Channel**: Action summary for guild admins

### Match System

The IGP integrates with:
- Match label tracking
- Player presence streams
- Team assignment system
- EchoVR session management

## Troubleshooting

### "No active panel found"

**Cause**: The IGP session timed out or was closed

**Solution**: Re-run `/igp` to create a new session

### "You must be a guild enforcer to use this command"

**Cause**: Missing required permissions

**Solution**: Contact guild admins to request Enforcer role

### IGP not updating

**Cause**: Network connectivity or session expired

**Solution**: Close and reopen the IGP with `/igp`

### Players not appearing in dropdown

**Cause**: Not in a match, or match not tracked

**Solution**: 
- Ensure you've joined a match
- Wait 3 seconds for the next refresh
- Check that the match is hosted by your guild

### Moderation action had no effect

**Possible Causes**:
1. Player already left the match
2. Match is from a different guild (outside your jurisdiction)
3. Player is the match owner in a private match (only owner/operator can kick)

**Solution**: Check audit logs for specific error messages

## Related Commands

- `/kick-player`: Direct kick command without needing to be in-game
- `/ign`: Set IGN override for yourself or others
- `/join-player`: Join a player's current match

## Notes

- The IGP is **ephemeral** (private) - only you can see it
- Opening a new `/igp` closes any previous IGP session
- The IGP automatically closes when you disconnect from the game
- Moderation actions are logged to audit channels for accountability
- All times are displayed in Discord's relative format (e.g., "in 5 minutes", "2 hours ago")

## Discord API References

This command uses the following Discord interaction types:
- **Application Command**: Slash command registration
- **Modal Submit**: For text input forms
- **Message Components**: Buttons and select menus
- **Webhook Edit**: For updating the ephemeral message
- **Embeds**: For player card displays

For more information on Discord interactions, see the [Discord Developer Documentation](https://discord.com/developers/docs/interactions/overview).

---

*For questions or issues with the IGP command, contact your guild administrators or the server operations team.*
