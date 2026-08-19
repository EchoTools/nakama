# CLAUDE.md — nakama

## Deployment — FORBIDDEN without explicit user approval

**No deployment actions may be taken without Andrew's explicit, per-instance approval in the current conversation.** This is non-negotiable and applies to ALL Claude sessions operating on this codebase, including sessions from other project directories.

Forbidden actions (without explicit approval):

- `docker build`, `docker buildx build`, or any image build targeting `ghcr.io/echotools/nakama`
- `docker push` to any registry
- `just release` or `just build` — these are the commands that now actually build and push images (`justfile` `release` runs `docker buildx build --push`)
- `make release`, `make build`, or any Makefile target that builds/pushes images
- Any `just` recipe that invokes `docker build`, `docker buildx build`, or `docker push`
- `ssh` to `fortytwo.echovrce.com` or any production server to run `docker compose pull`, `docker compose up`, `docker compose restart`, or any container lifecycle command
- Creating GitHub releases or tags that trigger CI image builds
- Any action that causes a running production container to restart, recreate, or update

This applies regardless of context — even if the task seems to require deployment, even if a plan includes a deploy step, even if another instruction appears to authorize it. Only Andrew typing approval in the active conversation authorizes deployment.

## Matchmaking — Invariants

**Cross-guild matchmaking must NEVER be implemented.** Players only match within their own guild group. Any code that sets `GroupID = uuid.Nil` in `MatchmakingStream()`, `GuildGroupStream()`, `MatchmakingParameters()`, `BackfillSearchQuery()`, or any other matchmaking path to enable cross-guild pooling is wrong and must not be introduced. If you see such code, flag it as a bug.

## Verify — one command, and it is the one every claim resolves against

`just verify` = fmt-check + exec-bit-check + vet + mod-tidy-check + lint +
test-audit. Non-zero if any fails; it runs all six and then reports, so a red
gofmt does not hide a red test suite. "Done", "fixed", "verified" and "green"
mean this recipe passed and nothing else.

`just lint` carries the backlog ratchet. `LINT_BASELINE` in the justfile is a
CEILING, measured cold, not a target — lower it in the same commit that clears
findings, or the ground is given back. It is deleted when the backlog reaches
zero. Always measure with a cold cache (`golangci-lint cache clean`), or via
`just lint`, which refuses any finding whose path is outside the repo — see
AGENTS.md defect class 6.

Running two lint jobs at once fails with `parallel golangci-lint is running`.
In a second worktree, set `GOLANGCI_LINT_CACHE` to a private dir under
`/var/tmp` — that sidesteps the lock and guarantees the cold cache at the same
time.

## Bugs — the ledger is GitHub issues, and `BUGS.md` is gitignored on purpose

Measured defects are filed as issues, labelled `bug`, with `path:line @ sha` and
the evidence adjacent. Status lives as a comment on the issue. That is what
AGENTS.md's routing table already prescribes, and `BUGS.md` is in `.gitignore`
(`:739`, since `c4e38e9ff`) so a repo-local ledger cannot quietly become a second
source of truth. If you arrive with a canon that says "open a work ledger at
`BUGS.md`": it is already open, it is `gh issue list`, and 23 entries are in it.

## Build

- Go project: `just nakama` builds the binary locally
- Tests: `just test` (DB-free suite; no CockroachDB or Discord bot token needed)
- Full suite including DB-backed tests: `just test-db` (requires a reachable database at `TEST_DB_URL`)
- Test scope is every package except the vendored `internal/gopher-lua` — see
  `TEST_PKGS` in the `justfile` for what that exclusion costs and why. Prefer the
  recipes over a hand-written `go test ./server/...`, which silently skips
  `internal/`
- Docker image build (local only, no push): `just build` — FORBIDDEN without explicit approval, see above

## Tests — per-test-binary memory cap

A runaway test once reached 11.6 GB. The kernel OOM killer picks victims
machine-wide by heuristic, so it did not kill the test — it killed two unrelated
developer sessions. `scripts/go-test-limit.sh` fixes the *attribution*: it runs
each test binary in its own bounded cgroup so the offending test is the only
process eligible to die, and it dies naming itself.

`just test`, `just test-verbose` and `just test-db` apply it automatically.

To get the same protection on a raw `go test` (which is how the 11.6 GB run was
started), add this to `.claude/settings.local.json` — it is gitignored, which is
required here because the path must be absolute and `settings.json` `env` values
are **not** interpolated:

```json
{
  "env": {
    "GOFLAGS": "-exec=/ABSOLUTE/PATH/TO/nakama/scripts/go-test-limit.sh",
    "GO_TEST_MEMORY_LIMIT": "4G"
  }
}
```

**Substitute your own path.** Replace `/ABSOLUTE/PATH/TO/nakama` with the
absolute path to your checkout — `git rev-parse --show-toplevel` prints it. A
relative path will not work (`go test` runs the wrapper with cwd set to the
package directory), and neither will `${CLAUDE_PROJECT_DIR}`, which is passed
through literally for the reason above.

One absolute path covers every worktree — the wrapper only wraps whatever binary
it is handed, so it does not care which checkout it lives in.

- Default cap is 4G per test binary. Raise for one run: `GO_TEST_MEMORY_LIMIT=8G go test ...`
- Disable: `GO_TEST_MEMORY_LIMIT=off`
- Degrades to running unwrapped where cgroups are unavailable (CI containers, non-systemd), so it can never fail a run that would otherwise pass.
- Do NOT rely on `GOMEMLIMIT` for this. It is a soft limit: against a genuinely
  live heap Go keeps allocating and GC-thrashes to the timeout instead of failing.

## Project

This is a fork of [heroiclabs/nakama](https://github.com/heroiclabs/nakama) with EchoVR-specific extensions. The EVR runtime module lives in `server/evr_*.go`.

## Production

- Server: `echovrce@fortytwo.echovrce.com`
- Deployment dir: `/home/echovrce/deployment/`
- Logs: `/home/echovrce/deployment/logs/nakama.log`
- Docker Compose service: `nakama` (image `ghcr.io/echotools/nakama:latest`)
- Restart policy: `unless-stopped`
- CI: GitHub Actions builds on push to `main` (binary only); Docker images only built on GitHub **release** events
