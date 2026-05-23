# EVR Module Extraction — Rough Spec

**Status:** Draft for technical-lead review
**Author:** Andrew / Teth
**Date:** 2026-05-23

---

## The Problem

We maintain a fork of [heroiclabs/nakama](https://github.com/heroiclabs/nakama) that
embeds EchoVR-specific game server code directly into the nakama source tree.
The fork has diverged significantly from upstream:

- 332 `server/evr_*.go` files (EVR-specific server logic) mixed in with nakama core
- 104 files in `server/evr/` (EVR binary protocol types) mixed in with nakama core
- ~140+ files of EVR code that don't exist in the upstream at all
- Upstream nakama releases are difficult to track because the fork changes everything
- New engineers have no idea where nakama ends and EVR begins

## The Goal

Extract all EchoVR-specific code from the nakama fork into a **standalone Go module**
that imports `heroiclabs/nakama/v3` as a library dependency. The nakama fork becomes
a thin custom binary (custom `main.go` + plugin loader) or is eliminated entirely.

## Prior Art

There is a prototype at `andrew@spriffy:/home/andrew/src/nevr/` (stale, Sep 2025).
It demonstrates the target architecture:

```
nevr/                    ← standalone binary
├── main.go              ← custom nakama component wiring
├── service/             ← EVR server logic
├── protocol/            ← EVR binary protocol types
├── plugin/              ← separate Go module for runtime plugins
├── internal/            ← nevr-private internals
└── go.mod               ← imports heroiclabs/nakama/v3
```

The prototype went stale while the fork's EVR code continued to evolve. The task
is to build this properly from the current codebase state, using the prototype
as an architectural reference but not as source material.

## Current State

**Nakama fork (`/srv/src/nakama/`):**

```
server/
├── evr_*.go             ← 332 files of EVR-specific Go code
├── evr/                 ← 104 files of EVR binary protocol types
├── evr/wire/            ← auto-generated wire protocol
├── api*.go              ← upstream nakama API (some EVR additions)
├── core*.go             ← upstream nakama core (some EVR additions)
├── ...                  ← rest of upstream nakama
server/evr/          ← EVR protocol types (binary encode/decode)
evr.proto            ← EVR protobuf service definitions
apigrpc/             ← gRPC API (some EVR additions)
```

**Reference prototype (`nevr/` on spriffy):**

```
service/              ← 195 Go files (EVR server logic, stale)
protocol/             ← 97 Go files (EVR protocol types, mostly current)
main.go               ← custom nakama wiring with PipelineDependencies DI
plugin/               ← separate Go module for runtime plugins
internal/             ← nevr-private utilities
```

## What the Extracted Module Must Do

The new module must provide, at minimum:

1. **EVR binary protocol** — Message types with `Symbol()`/`Stream()` interface,
   registered in `NewMessageFromHash()` for routing
2. **EVR pipeline** — Message processing pipeline that handles EVR game client
   messages (login, matchmaking, lobby, game server communication)
3. **Matchmaking** — Custom skill-based matchmaking with OpenSkill ratings,
   team balancing, reservation system
4. **Match handler** — EVR match lifecycle (init, join, loop, terminate) via
   Nakama's match runtime interface
5. **Lobby system** — Lobby creation, joining, parameters, team assignment
6. **Discord bot** — App bot with slash commands, linked roles, integrator
7. **Authentication** — EVR-specific auth flows (device auth, alt accounts,
   display name management)
8. **VRML integration** — VRML league verification, entitlements, OAuth
9. **Runtime RPCs** — All EVR RPC endpoints exposed through Nakama's runtime
10. **Plugin support** — Separate plugin module for user-defined runtime hooks

## Constraints

1. **Backward compatibility** — The extraction must not break the running
   production server (fortytwo.echovrce.com). Must be deployable as a drop-in
   replacement or via a transition period with alias shims.
2. **Build process** — Must use `make nakama` for the fork build. The new
   module should have its own Makefile.
3. **CI/CD** — Must work within our existing GitHub Actions pipelines.
4. **Database schema** — The EVR module uses custom storage indexes and
   database migrations. These must continue to work.
5. **Upstream tracking** — After extraction, the nakama fork should be as
   close to upstream heroiclabs/nakama as possible. The EVR module should
   be the only thing that changes when upstream releases.
6. **Local development** — Must work with `go.work` for local development
   across the fork and the new module.

## Known Risks

1. **Interface drift** — Nakama's internal interfaces (SessionRegistry,
   MatchRegistry, Tracker, etc.) are not designed for external consumption.
   The module needs to depend on them as a library, which means we're
   coupling to heroiclabs/nakama's internal API surface. If upstream changes
   these interfaces, the module breaks.
2. **Circular dependencies** — Some EVR code reaches into nakama internals
   that may be hard to abstract away cleanly.
3. **Test environment** — Many EVR tests require a running PostgreSQL and
   nakama server. The extracted module needs its own test infrastructure.
4. **Deployment** — The transition from embedded to standalone needs a
   rollout plan that doesn't require a full redeploy of everything.

## Non-Goals

1. **Not rewriting the EVR protocol** — The binary protocol types are
   copied as-is, not redesigned.
2. **Not changing the database schema** — Storage indexes and migrations
   stay the same, just live in the new module.
3. **Not changing the EVR client protocol** — Game clients are unaffected.
4. **Not rewriting nakama core** — We use heroiclabs/nakama as-is (from
   our fork, which has minimal EVR-independent changes).

## Questions for Architects

1. How should PipelineDependencies be structured to minimize coupling to
   nakama-internal interfaces?
2. Should the plugin module be a true Go plugin (`plugin` package) or a
   separate module imported at build time?
3. What's the right module path? `github.com/echotools/nakama-evr`?
   Something else?
4. How do we handle the transition period where both the fork and the
   new module need to compile from the same workspace?
5. What's the versioning strategy? Match nakama's semver? Independent?
