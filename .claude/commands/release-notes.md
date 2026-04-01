---
description: "Generate audience-targeted release notes and post to Discord. Use when tagging a release, preparing changelog, or user says /release-notes."
allowed-tools: Bash, Read, Write, Edit, Glob, Grep, Agent, WebFetch
---

# Release Notes Generator

Generate release notes for an EchoVR game server called EchoRelay. The audience is the EchoVR community on Discord. The server is a custom backend that replaces the defunct official servers.

**Input:** A release tag (e.g. `v3.27.2-evr.263`) and optionally a base tag. If no base tag is given, use the immediately preceding tag.

If no tag is provided, determine the latest tag from `git tag --sort=-v:refname | head -5` and use it.

## Step 1: Gather commits

```bash
git log <base_tag>..<release_tag> --oneline --no-merges
```

Filter out merge commits (lines starting with "Merge worktree-agent-" or similar). These are infrastructure noise.

For any commit that isn't self-explanatory from its one-line message, read the full commit message and/or the diff to understand the user-facing impact.

## Step 2: Categorize each change

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

## Step 3: Write the descriptions

For each change, write a SHORT (one line) player-friendly description. Do NOT use commit-message language. Translate technical changes into what the player experiences.

Examples:

- `fix: matchmaker reservation system broken by wrong property key` -> "Fixed a bug where matchmaking could fail to find matches"
- `fix: party members don't follow leader into social lobbies` -> "Fixed party members not following the party leader into lobbies"
- `feat: add suspension enforcement system` -> "Added suspension enforcement system"
- `fix: KickPlayerAllowPrivates defaults to false instead of true` -> "Players can no longer join private matches while kicked (previously the default allowed it)"

Do NOT include:

- Commit hashes
- File names or function names
- Technical jargon about binary formats, protocol rewrites, etc. (except in echotools/app-developer notes)

## Step 4: Game Service Updated Notification

Generate a short notification message for the general announcements channel:

```
# :EchoVR: Deployed Game Service Update :EchoVR:

- <short one-line player-facing change>
- <short one-line player-facing change>

### :warning: if you get an error, then reopen your game. If you still get an error, then go pet your dog for 10 minutes. :doggoblob:
```

Only include `player` category items. Keep it to 3-8 short bullet points. No bold titles, no descriptions — just the facts.

## Step 5: Generate seven release notes

All release notes use this structure. Group changes by TOPIC (e.g. "Matchmaking", "Party System", "Moderation"), not by commit type. Each item gets a **bold title** followed by a colon and a description.

All release notes EXCEPT #7 (full changelog) must include this footer:

```
-# [Full Changelog](https://discord.com/channels/1179666360863817749/1485036740820472080)
```

### 1: Players (#release-notes)

`player` category only. No emoji in title.

```
## Release Notes <release_tag>

<1-2 sentence summary of the release theme>

### <Topic Area>
* **<Title>:** <What changed and why it matters>

-# [Full Changelog](https://discord.com/channels/1179666360863817749/1485036740820472080)
```

### 2: EchoTools Core Developers

ALL categories. Full technical detail. Include refactors, protocol changes, migration changes, binary format changes in a "Technical / Internal" section.

### 3: App Developers

`app-developer` category ONLY. Focus on API surface changes, RPC endpoints, behavioral changes that affect integrations. If none, say "No app-developer-facing changes in this release."

### 4: Guild Owners / Operators / Moderators

`player`, `operator`, and `enforcer` categories. Focus on management tools, enforcement features, guild administration.

### 5: Competitive Server Hosts / Allocators / Coordinators

`player`, `enforcer`, and `competitive` categories. ONLY echo_arena_private and echo_combat_private changes. Do NOT include public matchmaking. If none, say "No competitive hosting changes in this release."

### 6: In-Game Mod / Enforcer Quick Notes

`enforcer` category ONLY. Brief bullet list, no topic grouping. If none, say "No enforcer-specific changes in this release."

### 7: Full Changelog (#full-changelog)

ALL categories. Concise but comprehensive. Grouped by topic. No footer needed.

```
## Changelog <release_tag>

### <Topic Area>
* **<Title>:** <Concise description>

### Protocol & Internal
* **<Title>:** <Technical description>
```

## Step 6: Output

Present all seven release notes + the notification message clearly separated. Wrap each in a markdown code block for easy copy-paste into Discord. Format using Discord markdown.

## Step 7: Post to Discord

Present this table and ask which to post:

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

Post using the script:

```bash
./docs/post-release-notes.sh <channel_number> <file_with_message_content>
```

Write each message to a temp file first, then post. Discord content field has a 2000 character limit — the script handles splitting automatically.

**Only post after explicit user confirmation. Never post automatically.**
