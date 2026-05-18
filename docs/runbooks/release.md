# Release Runbook — Nakama EVR

## THIS IS NOT

- A general operations manual. There will be other runbooks for that.
- A license to deploy without Andrew. Deployment requires his explicit approval every time.

## Overview

Release goes: code → tag → CI builds Docker image → pull on fortytwo → docker compose restart.

CI handles the image build and push. You handle the tag, the release notes, and the deploy.

## Usage

Inputs:

- release tag (for example `v3.27.2-evr.299`)
- optional previous/base tag

If no base tag is given, use the immediately preceding tag.

If no release tag is given, determine the latest candidates with:

```bash
git tag --sort=-v:refname | head -5
```

## Pre-flight

Before you do anything:

- [ ] Andrew has explicitly approved this release in the current conversation
- [ ] You're on the right branch (usually `main`) with the commits you want
- [ ] Tests pass: `go test ./server/...`
- [ ] Binary builds: `make nakama`
- [ ] You know what tag to use — convention is `v<VERSION>-evr.<N>` (e.g. `v3.27.2-evr.299`)

## Step 1: Tag and push

```bash
git tag -a <tag> -m "<tag>"
git push origin <tag>
```

This triggers the `release-nakama` GitHub Action (`.github/workflows/dockerhub-nakama.yaml`), which builds and pushes the Docker image to `ghcr.io/echotools/nakama:<tag>`.

Wait for the action to complete before proceeding. Check: https://github.com/EchoTools/nakama/actions

## Step 2: Generate release notes

### Gather commits

```bash
git log <previous_tag>..<tag> --oneline --no-merges
```

Filter out merge commits (`Merge worktree-agent-*` etc.). Read full messages/diffs on anything that isn't obvious from the one-liner.

### Categorize each change

| Category | Audience | What goes here |
|----------|----------|----------------|
| `player` | All players | Gameplay fixes, matchmaking, party system, crash fixes, connection improvements |
| `enforcer` | In-game moderators | Kick/smite/ban, enforcement UI, in-match moderation |
| `operator` | Guild owners, operators | Server/guild management, Discord bot admin, enforcement config |
| `app-developer` | Integration devs | API changes, RPC endpoints, webhooks, SDK changes |
| `competitive` | Comp hosts/coordinators | Private match hosting/config/allocation ONLY. NOT public matchmaking. |
| `echotools` | EchoTools core devs | Refactors, protocol changes, migrations, infrastructure |

A commit can belong to multiple categories. Write player-facing descriptions in plain language — no commit hashes, no file names, no jargon (except echotools/app-dev notes where technical detail is fine).

### Announcement message

Short notification for #general-announcements:

```
# :EchoVR: Deployed Game Service Update :EchoVR:

- <player-facing change>
- <player-facing change>

### :warning: if you get an error, then reopen your game. If you still get an error, then go pet your dog for 10 minutes. :doggoblob:
```

Only `player` category items. 3-8 bullets. No bold titles, no descriptions. This gets presented to Andrew to post manually.

### Seven release notes

Write all seven. Templates below. Group by topic (e.g. "Matchmaking", "Party System"), not by commit type. Each bullet: **bold title** followed by a description.

All except the full changelog (7) get this footer:

```
-# [Full Changelog](https://discord.com/channels/1179666360863817749/1485036740820472080)
```

#### 1. Players (#release-notes) — `player` only
```
## Release Notes <tag>

<1-2 sentence summary of what players will notice>

### <Topic>

* **<Title>:** <What changed and why it matters>

### <Topic>

* **<Title>:** <Description>

-# [Full Changelog](...)
```

#### 2. EchoTools devs (#echotools-releases) — all categories
```
## Release Notes <tag>

<1-2 sentence summary>

### <Topic>

* **<Title>:** <Description>

### Technical / Internal

* **<Title>:** <Technical detail, code refs, protocol changes welcome>

-# [Full Changelog](...)
```

#### 3. App developers (#app-dev-releases) — `app-developer` only
If no app-dev changes: "No app-developer-facing changes in this release."
```
## Release Notes <tag>

<summary focused on API/integration impact>

### <Topic>

* **<Title>:** <Description from integration/API perspective>

-# [Full Changelog](...)
```

#### 4. Guild owners/operators/mods (#operator-releases) — `player` + `operator` + `enforcer`
```
## Release Notes <tag>

<summary focused on management/moderation changes>

### <Topic>

* **<Title>:** <Description from server management perspective>

-# [Full Changelog](...)
```

#### 5. Competitive hosts (#competitive-releases) — `player` + `enforcer` + `competitive`
If no competitive changes: "No competitive hosting changes in this release."
Focus ONLY on private match hosting/config/allocation. Not public queues.
```
## Release Notes <tag>

<summary focused on competitive/match hosting impact>

### <Topic>

* **<Title>:** <Description from competitive hosting perspective>

-# [Full Changelog](...)
```

#### 6. Enforcers (#enforcer-updates) — `enforcer` only
If no enforcer changes: "No enforcer-specific changes in this release."
```
## Enforcer Updates <tag>

* <Short one-line description>
* <Short one-line description>

-# [Full Changelog](...)
```

#### 7. Full changelog (#full-changelog) — all categories, no footer
```
## Changelog <tag>

### <Topic>

* **<Title>:** <Concise description>

### <Topic>

* **<Title>:** <Description>

### Protocol & Internal

* **<Title>:** <Technical description>
```

### Output format

Present all seven release notes clearly separated with headers indicating which audience each targets. Wrap each one in a markdown code block so Andrew can copy-paste into Discord without cleanup. Use Discord markdown only.

### Post to Discord

Present all seven to Andrew. He picks which to post and confirms. Never post automatically.

Webhook URLs are in `.env` at the project root. Channel mapping:

| # | Channel | Env var |
|---|---------|---------|
| 1 | #release-notes | WEBHOOK_RELEASE_NOTES |
| 2 | #echotools-releases | WEBHOOK_ECHOTOOLS_RELEASES |
| 3 | #app-dev-releases | WEBHOOK_APP_DEV_RELEASES |
| 4 | #operator-releases | WEBHOOK_OPERATOR_RELEASES |
| 5 | #competitive-releases | WEBHOOK_COMPETITIVE_RELEASES |
| 6 | #enforcer-updates | WEBHOOK_ENFORCER_UPDATES |
| 7 | #full-changelog | WEBHOOK_FULL_CHANGELOG |

Post:
```bash
./docs/post-release-notes.sh <channel_number> <file_with_content>
```

Discord has a 2000 character limit. The script handles splitting. Messages over limit get chunked at newlines.

## Step 3: Deploy to fortytwo

Only after Andrew explicitly approves. Only after the CI image build succeeded.

```bash
ssh echovrce@fortytwo.echovrce.com
cd /home/echovrce/deployment/
docker compose pull nakama
docker compose up -d --no-deps nakama
```

## Step 4: Verify

- [ ] `docker compose ps` — nakama shows "Up"
- [ ] Logs look clean: `tail -n 50 /home/echovrce/deployment/logs/nakama.log`
- [ ] Healthcheck passes: `docker compose exec nakama /nakama/nakama healthcheck`
- [ ] (If applicable) Test a match join or party follow in-game
- [ ] Announcement posted to #general-announcements

## Rollback

If the release is broken:

```bash
ssh echovrce@fortytwo.echovrce.com
cd /home/echovrce/deployment/
# Tag the current image so we know what we're rolling back from
docker tag ghcr.io/echotools/nakama:<bad_tag> ghcr.io/echotools/nakama:rollback-from-<bad_tag>
# Re-tag the previous known-good image
docker tag ghcr.io/echotools/nakama:<previous_tag> ghcr.io/echotools/nakama:latest
docker compose up -d --no-deps nakama
```

Then fix the bug, tag a new release, deploy forward. Don't sit on a rollback — the only way out is through.
