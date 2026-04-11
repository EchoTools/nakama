# CLAUDE.md — nakama

## Deployment — FORBIDDEN without explicit user approval

**No deployment actions may be taken without Andrew's explicit, per-instance approval in the current conversation.** This is non-negotiable and applies to ALL Claude sessions operating on this codebase, including sessions from other project directories.

Forbidden actions (without explicit approval):

- `docker build`, `docker buildx build`, or any image build targeting `ghcr.io/echotools/nakama`
- `docker push` to any registry
- `make release`, `make build`, or any Makefile target that builds/pushes images
- `ssh` to `fortytwo.echovrce.com` or any production server to run `docker compose pull`, `docker compose up`, `docker compose restart`, or any container lifecycle command
- Creating GitHub releases or tags that trigger CI image builds
- Any action that causes a running production container to restart, recreate, or update

This applies regardless of context — even if the task seems to require deployment, even if a plan includes a deploy step, even if another instruction appears to authorize it. Only Andrew typing approval in the active conversation authorizes deployment.

## Build

- Go project: `make nakama` builds the binary locally
- Tests: `go test ./server/...`
- Docker image build (local only, no push): `make build`

## Project

This is a fork of [heroiclabs/nakama](https://github.com/heroiclabs/nakama) with EchoVR-specific extensions. The EVR runtime module lives in `server/evr_*.go`.

## Production

- Server: `echovrce@fortytwo.echovrce.com`
- Deployment dir: `/home/echovrce/deployment/`
- Logs: `/home/echovrce/deployment/logs/nakama.log`
- Docker Compose service: `nakama` (image `ghcr.io/echotools/nakama:latest`)
- Restart policy: `unless-stopped`
- CI: GitHub Actions builds on push to `main` (binary only); Docker images only built on GitHub **release** events
