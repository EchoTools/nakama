# Release Notes Generator

## Usage

Provide a release tag (e.g. `v3.27.2-evr.263`) and optionally a base tag. If no base tag is given, use the immediately preceding tag.

## Instructions

You are generating release notes for the EchoVR game server (nevr-service / nakama). The audience is the EchoVR community on Discord. The server is a custom backend that replaces the defunct official servers.

### Step 1: Gather commits

Run:

```
git log <base_tag>..<release_tag> --oneline --no-merges
```

Filter out merge commits (lines starting with "Merge worktree-agent-" or similar). These are infrastructure noise.

For any commit that isn't self-explanatory from its one-line message, read the full commit message and/or the diff to understand the user-facing impact.

### Step 2: Categorize each change

Assign each meaningful commit to one or more audience categories:

| Category        | Who sees it                                 | Description                                                                                                                                                                                                                                                                                           |
| --------------- | ------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `player`        | All players                                 | Gameplay fixes, matchmaking improvements, party system changes, crash fixes, connection improvements                                                                                                                                                                                                  |
| `enforcer`      | In-game moderators/enforcers                | In-game moderation tools, kick/smite/ban actions, enforcement UI, in-match moderation features                                                                                                                                                                                                        |
| `operator`      | Guild owners, global operators, moderators  | Server management, guild management, Discord bot admin features, enforcement configuration, operator dashboards                                                                                                                                                                                       |
| `app-developer` | App/integration developers                  | API changes, RPC endpoints, webhook changes, integration points, SDK-facing changes                                                                                                                                                                                                                   |
| `competitive`   | Comp server hosts, allocators, coordinators | Changes to echo_arena_private and echo_combat_private modes ONLY. Private match hosting, private match configuration, server allocation for private matches, team settings in private matches. Do NOT include public matchmaking (echo_arena, echo_combat) changes here — those are `player` category |
| `echotools`     | EchoTools core developers                   | Internal refactors, binary protocol changes, migration changes, code cleanup, architecture, infrastructure                                                                                                                                                                                            |

A single commit can belong to multiple categories.

### Step 3: Write the descriptions

For each change, write a SHORT (one line) player-friendly description. Do NOT use commit-message language. Translate technical changes into what the player experiences.

Examples:

- `fix: matchmaker reservation system broken by wrong property key` → "Fixed a bug where matchmaking could fail to find matches"
- `fix: party members don't follow leader into social lobbies` → "Fixed party members not following the party leader into lobbies"
- `feat: add suspension enforcement system` → "Added suspension enforcement system"
- `fix: KickPlayerAllowPrivates defaults to false instead of true` → "Players can no longer join private matches while kicked (previously the default allowed it)"

Do NOT include:

- Commit hashes
- File names or function names
- Technical jargon about binary formats, protocol rewrites, etc. (except in echotools/app-developer notes)

### Game Service Updated Notification

In addition to the release notes, generate a short notification message for the general announcements channel. This is a quick heads-up for players, not a detailed changelog.

```
# :EchoVR: Deployed Game Service Update :EchoVR:

- <short one-line player-facing change>
- <short one-line player-facing change>

### :warning: if you get an error, then reopen your game. If you still get an error, then go pet your dog for 10 minutes. :doggoblob:
```

Only include `player` category items. Keep it to 3-8 short bullet points. No bold titles, no descriptions — just the facts. This message is presented to the user for them to copy and post manually.

### Step 4: Generate the seven release notes

All release notes use this structure. Group changes by TOPIC (e.g. "Matchmaking", "Party System", "Moderation"), not by commit type. Each item gets a **bold title** followed by a colon and a description explaining what changed and why it matters.

All release notes EXCEPT the full changelog (Release Notes 7) must include this footer at the very end:

```
-# [Full Changelog](https://discord.com/channels/1179666360863817749/1485036740820472080)
```

#### Release Notes 1: Players (#release-notes)

Player-focused. Only `player` category items. This is the public-facing release note. No emoji in the title.

```
## Release Notes <release_tag>

<1-2 sentence summary of the release theme — what players will notice>

### <Topic Area>

* **<Title>:** <What changed and why it matters to you as a player>

### <Topic Area>

* **<Title>:** <Description>

-# [Full Changelog](https://discord.com/channels/1179666360863817749/1485036740820472080)
```

Keep it concise — players don't care about internal details. Group small fixes where appropriate.

#### Release Notes 2: EchoTools Core Developers

ALL categories included. Full technical detail. This is the most comprehensive release note (aside from the full changelog).

```
## Release Notes <release_tag>

<1-2 sentence summary>

### <Topic Area>

* **<Title>:** <Description>

### Technical / Internal

* **<Title>:** <Technical description — can use code references, protocol details, architecture notes>

-# [Full Changelog](https://discord.com/channels/1179666360863817749/1485036740820472080)
```

The technical section CAN and SHOULD use technical language. Include refactors, protocol changes, migration changes, binary format changes, etc.

#### Release Notes 3: App Developers

`app-developer` category ONLY. Focus on API surface changes, RPC endpoints, behavioral changes that affect integrations, webhook changes, SDK-facing changes. Skip player-facing fixes and internal refactors unless they change external API behavior.

```
## Release Notes <release_tag>

<1-2 sentence summary focused on API/integration impact>

### <Topic Area>

* **<Title>:** <Description focused on what changed from an integration/API perspective>

-# [Full Changelog](https://discord.com/channels/1179666360863817749/1485036740820472080)
```

If there are no app-developer-specific changes, say so: "No app-developer-facing changes in this release."

#### Release Notes 4: Guild Owners / Operators / Moderators

Include `player`, `operator`, and `enforcer` categories. Focus on management tools, enforcement features, guild administration, operator-facing changes.

```
## Release Notes <release_tag>

<1-2 sentence summary focused on management/moderation changes>

### <Topic Area>

* **<Title>:** <Description focused on how this affects server management and moderation>

-# [Full Changelog](https://discord.com/channels/1179666360863817749/1485036740820472080)
```

#### Release Notes 5: Competitive Server Hosts / Allocators / Coordinators

Include `player`, `enforcer`, and `competitive` categories. Tailored for people who run competitive private match servers, manage match allocations, and coordinate tournaments/leagues. Focus ONLY on echo_arena_private and echo_combat_private mode changes — private match hosting, private match configuration, server allocation for private matches, and enforcement as it affects private matches. Do NOT include public matchmaking or public queue changes (echo_arena, echo_combat) — those belong in the player release notes only.

```
## Release Notes <release_tag>

<1-2 sentence summary focused on competitive/match hosting impact>

### <Topic Area>

* **<Title>:** <Description focused on how this affects competitive hosting, match allocation, or coordination>

-# [Full Changelog](https://discord.com/channels/1179666360863817749/1485036740820472080)
```

If there are no competitive-relevant changes, say so: "No competitive hosting changes in this release."

#### Release Notes 6: In-Game Mod / Enforcer Quick Notes

`enforcer` category ONLY. Brief bullet list — no topic grouping, no elaborate descriptions. Just the facts.

```
## Enforcer Updates <release_tag>

* <Short one-line description of enforcer-relevant change>
* <Short one-line description>

-# [Full Changelog](https://discord.com/channels/1179666360863817749/1485036740820472080)
```

If there are no enforcer-specific changes, say so: "No enforcer-specific changes in this release."

#### Release Notes 7: Full Changelog (#full-changelog)

ALL categories. Concise but comprehensive. Grouped by topic. This is the canonical record of everything that shipped. No footer needed — this IS the full changelog.

```
## Changelog <release_tag>

### <Topic Area>

* **<Title>:** <Concise description>

### <Topic Area>

* **<Title>:** <Description>

### Protocol & Internal

* **<Title>:** <Technical description>
```

### Step 5: Output

Present all seven release notes clearly separated with headers indicating which audience each targets. Wrap each in a markdown code block for easy copy-paste into Discord. Format using Discord markdown (`- ` for lists, `**bold**`, etc. — no HTML tags).

### Step 6: Post to Discord

After presenting all release notes, ask the user which ones they want to post. List the channels and let them confirm:

| #   | Channel               | Audience                            | Env Var                        |
| --- | --------------------- | ----------------------------------- | ------------------------------ |
| 1   | #release-notes        | Players                             | `WEBHOOK_RELEASE_NOTES`        |
| 2   | #echotools-releases   | EchoTools devs                      | `WEBHOOK_ECHOTOOLS_RELEASES`   |
| 3   | #app-dev-releases     | App developers                      | `WEBHOOK_APP_DEV_RELEASES`     |
| 4   | #operator-releases    | Guild owners/operators/mods         | `WEBHOOK_OPERATOR_RELEASES`    |
| 5   | #competitive-releases | Comp hosts/allocators/coordinators  | `WEBHOOK_COMPETITIVE_RELEASES` |
| 6   | #enforcer-updates     | In-game mods/enforcers              | `WEBHOOK_ENFORCER_UPDATES`     |
| 7   | #full-changelog       | Detailed changelog (all categories) | `WEBHOOK_FULL_CHANGELOG`       |

Webhook URLs are stored in `.env` at the project root (gitignored).

Post using the `post-release-notes.sh` script in this directory:

```bash
./docs/post-release-notes.sh <channel_number> <file_with_message_content>
```

Or post manually with curl:

```bash
curl -H "Content-Type: application/json" \
  -d '{"content": "<message content here>"}' \
  "<webhook_url>"
```

Make sure to properly JSON-escape the message content (escape newlines as `\n`, escape quotes, etc.). The Discord content field has a 2000 character limit — if the message exceeds that, split it into multiple webhook calls.

Only post after explicit user confirmation. Never post automatically.
