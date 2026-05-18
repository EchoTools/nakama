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

## Prerequisites

Before starting, make sure all of these are true:

- Local repo path: `~/src/nakama`
- Production server: `echovrce@fortytwo.echovrce.com`
- Production deploy path: `/home/echovrce/deployment/`
- Production log path: `/home/echovrce/deployment/logs/nakama.log`
- GitHub repo: `EchoTools/nakama`
- GitHub Actions page: `https://github.com/EchoTools/nakama/actions`
- Container image: `ghcr.io/echotools/nakama:<tag>`
- Docker Compose service name: `nakama`
- Local webhook env file: `~/src/nakama/.env`

Required access/credentials:

- Git push access for tags on `EchoTools/nakama`
- GitHub access to verify the release workflow succeeded
- SSH access to `echovrce@fortytwo.echovrce.com`
- Docker permissions on the production host
- Valid webhook URLs present in local `.env` if posting release notes

Required local tools:

- `git`
- `go`
- `make`
- `ssh`
- `docker` / `docker compose`
- `curl`
- `jq` (required by `docs/post-release-notes.sh`)

Required webhook env vars in `.env`:

- `WEBHOOK_RELEASE_NOTES`
- `WEBHOOK_ECHOTOOLS_RELEASES`
- `WEBHOOK_APP_DEV_RELEASES`
- `WEBHOOK_OPERATOR_RELEASES`
- `WEBHOOK_COMPETITIVE_RELEASES`
- `WEBHOOK_ENFORCER_UPDATES`
- `WEBHOOK_FULL_CHANGELOG`

## Pre-flight

Before you do anything:

- [ ] Andrew has explicitly approved this release in the current conversation
- [ ] You're on the right branch (usually `main`) with the commits you want
- [ ] Tests pass: `go test ./server/...`
- [ ] Binary builds: `make nakama`
- [ ] You know what tag to use — EVR releases use the next monotonic `-evr.<N>` tag in sequence from the latest existing EVR tag (for example, if the latest is `v3.27.2-evr.304`, the next release is `v3.27.2-evr.305`)

## Step 1: Tag and push

```bash
git tag -a <tag> -m "<tag>"
git push origin <tag>
```

This triggers the `release-nakama` GitHub Action (`.github/workflows/dockerhub-nakama.yaml`), which builds and pushes the Docker image to:

- `ghcr.io/echotools/nakama:<tag>`
- `ghcr.io/echotools/nakama:latest`

Wait for the action to complete before proceeding. Check: https://github.com/EchoTools/nakama/actions

You can watch it either in the browser or with GitHub CLI:

```bash
gh run list --repo EchoTools/nakama --workflow release-nakama --limit 5
gh run watch <run-id> --repo EchoTools/nakama
```

### Alternate release path

If explicitly approved, `make release` can also be used to build and push release images directly from the local machine.

```bash
make release
```

This pushes:

- `ghcr.io/echotools/nakama:<tag>`
- `ghcr.io/echotools/nakama:latest`

Use this only when the local tag state is correct and you intentionally want to publish from the workstation instead of relying on GitHub Actions.

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
If there are no meaningful app-dev changes, do not post an app-dev release note at all.
```
## Release Notes <tag>

<summary focused on API/integration impact>

### <Topic>

* **<Title>:** <Description from integration/API perspective>

-# [Full Changelog](...)
```

#### 4. Guild owners/operators/mods (#operator-releases) — `player` + `enforcer` + `operator`
Operator notes always include player-facing changes plus any enforcer-specific and operator-specific changes.
```
## Release Notes <tag>

<summary focused on management/moderation changes>

### <Topic>

* **<Title>:** <Description from server management perspective>

-# [Full Changelog](...)
```

#### 5. Competitive hosts (#competitive-releases) — `player` + `enforcer` + `competitive`
If there are no meaningful competitive changes, do not post a competitive release note at all.
Focus ONLY on private match hosting/config/allocation. Not public queues.
```
## Release Notes <tag>

<summary focused on competitive/match hosting impact>

### <Topic>

* **<Title>:** <Description from competitive hosting perspective>

-# [Full Changelog](...)
```

#### 6. Enforcers (#enforcer-updates) — `player` + `enforcer`
Enforcer notes always include player-facing changes plus any enforcer-specific changes.
If there are no enforcer-specific changes, still include the player-facing items.
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

Do not send filler "no updates" posts to channels. If a channel has no meaningful audience-specific content and the runbook does not explicitly require player items to be included, skip posting to that channel.

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
sleep 3
docker compose exec nginx nginx -s reload
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
