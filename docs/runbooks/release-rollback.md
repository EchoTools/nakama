# Release Rollback Playbook — Nakama EVR

## THIS IS

- The playbook for when a Nakama release is bad in production and needs to be backed out.
- A fast operational procedure for restoring service first, then diagnosing.

## THIS IS NOT

- A full incident-management handbook.
- A substitute for fixing forward later.
- Permission to improvise on production without Andrew.

## Trigger Conditions

Use this playbook when a release causes one or more of these:

- login failures
- matchmaking failures
- party follow / social convergence failures
- crash loops
- healthcheck failures
- obvious regression confirmed in production logs or live testing

## Prerequisites

- Production host: `echovrce@fortytwo.echovrce.com`
- Deploy path: `/home/echovrce/deployment/`
- Service: `nakama`
- Log path: `/home/echovrce/deployment/logs/nakama.log`
- Known-good previous tag identified before touching anything

## Rule Zero

Stabilize service first. Explain later.

## Step 1: Confirm the bad release

Gather enough evidence to avoid rolling back for noise:

```bash
ssh echovrce@fortytwo.echovrce.com
cd /home/echovrce/deployment/
docker compose ps
docker compose exec nakama /nakama/nakama healthcheck
tail -n 100 /home/echovrce/deployment/logs/nakama.log
```

If the problem is obvious and player-impacting, continue.

## Step 2: Identify the rollback target

Pick the last known-good image tag.

Usually this is the previous EVR tag, for example:

- bad: `v3.27.2-evr.305`
- rollback target: `v3.27.2-evr.304`

Do not guess. Name the target explicitly before proceeding.

## Step 3: Roll back the image

Preferred method: pin the deployment back to the known-good tag, then restart.

If compose is using `latest`, retag locally on the production host:

```bash
ssh echovrce@fortytwo.echovrce.com
cd /home/echovrce/deployment/
docker pull ghcr.io/echotools/nakama:<previous_tag>
docker tag ghcr.io/echotools/nakama:<previous_tag> ghcr.io/echotools/nakama:latest
docker compose up -d --no-deps nakama
sleep 3
docker compose exec nginx nginx -s reload
```

If compose is already pinned to a tag, update it to `<previous_tag>` and restart the service.

## Step 4: Verify rollback success

```bash
ssh echovrce@fortytwo.echovrce.com
cd /home/echovrce/deployment/
docker compose ps
docker compose exec nakama /nakama/nakama healthcheck
tail -n 100 /home/echovrce/deployment/logs/nakama.log
```

Minimum success bar:

- container is up
- healthcheck passes
- logs are clean enough to accept traffic
- original regression no longer reproduces

## Step 5: Record what happened

Capture:

- bad tag
- rollback target tag
- exact user-facing symptom
- first confirming log line or health signal
- time rollback started
- time service recovered

Keep this lightweight but real. Enough to fix forward without re-learning the failure.

## Step 6: Fix forward later

After service is stable:

- isolate root cause
- write regression coverage first
- prepare a new release tag
- deploy forward cleanly

Do not leave the system in rollback limbo longer than necessary.

## Common Mistakes

- Rolling back without naming the known-good tag first
- Re-deploying the bad `latest` image by accident
- Checking only container status and not the explicit healthcheck
- Forgetting the nginx reload after restart
- Treating rollback as closure instead of temporary stabilization
