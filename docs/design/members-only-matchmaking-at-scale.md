# Design: Members-Only Matchmaking at Scale (60k+ guilds)

**Status:** Design — NOT implemented. Discussion/resume artifact.
**Related:** #228 (JWT guild roles), #467 (guild-ban enforcement — the deny-side, already done).
**Motivation:** EVRL has ~60k members and wants members-only matchmaking. We will **never** materialize a 60k-member roster anywhere.

---

## 1. Problem

Members-only matchmaking needs to answer one question at one moment: *"is THIS player, right now, a member of guild G?"* — a point lookup, not a roster scan. The naive "store the set of members and check against it" approach does not scale to 60k and is explicitly rejected.

## 2. Current state (grounded in code, 2026-06)

Three read-only inspections produced this. Citations are to `server/`.

### Membership model — `role → [users]` (the thing to invert)
- Role cache is `GuildGroupState.RoleCache map[RoleID]map[UserID]bool` — **role-major** (`evr_guild_group_state.go:21`).
- Persisted as **one JSON blob per guild** (Nakama storage, collection `GuildGroupState`, key=GroupID) and **re-serialized + rewritten on every member change** (`evr_guild_group.go:306`). → **Write amplification: an O(1) change rewrites an O(guild-size) object.** At 60k this is the primary cost.
- A background registry goroutine **deep-reloads every guild's blob from storage every 60s** (`evr_guild_group_registry.go:50,72`). → 60k blob reloaded each minute.
- Hybrid: raw membership is Nakama `group_edge`/GroupUsers; the `RoleCache` is the authoritative Discord-role overlay (Member/Enforcer/Suspended/…).

### The gate — hot path, two branches
`lobbyAuthorize` (`evr_lobby_joinentrant.go:359-373`) runs on **every** matchmaking/join/create attempt (`evr_lobby_find.go:88`, `evr_lobby_join.go:34`, `evr_lobby_create.go:16`). `IsPrivate()` == `EnableMembersOnlyMatchmaking` (`evr_group_metadata.go:74-76`).
- **Branch A — Member role defined:** `IsMember` → in-memory `RoleCache` lookup via the shared registry. Cheap. No Discord. (`evr_lobby_joinentrant.go:361`)
- **Branch B — no Member role defined:** falls back to `discordCache.GuildMember(...)` → 5-min TTL cache → discordgo state → **live Discord REST call** (`evr_discord_integrator.go:491`), on the hot path, and **fails open** on error. ⚠️ This violates the "never hit/block/rely on Discord" rule.
- Neither branch reads a session/JWT claim.

**Membership rule (confirmed):** member-role-if-defined, else plain guild membership counts (`evr_lobby_joinentrant.go:361-372`).

### Population — purely lazy, push-primary, no bulk
- No bulk member fetch anywhere (no `RequestGuildMembers`/chunking). Population is per-user, event-driven + login-triggered.
- `GuildMemberAdd` is a **no-op** (commented out) — a new Discord member generates no state until a `GuildMemberUpdate` or a login sync (`evr_discord_integrator.go:655`).
- **No role-change event handlers** (`GuildRoleCreate/Update/Delete`) — per-user role membership flows only through `GuildMemberUpdate` (`:666`, `RoleCacheUpdate` at `:728`). Latent correctness gap.
- Heavy syncs run async via a worker queue (cap 50, 30s cooldown) (`evr_discord_integrator.go:95-271`).

### Login + "deny and retry" already exists
- `initializeSession` loads the user's groups + role cache from the in-memory registry (blocking, **no Discord**) (`evr_pipeline_login.go:609`).
- If the user is in **zero** groups → fires async `QueueSyncMember(full=true)` and **returns an error telling the client to retry in ~30s** (`:615-621`). The cold-start "deny once, they retry" behavior is **already the live contract**.

### No JWT claims (#228 greenfield)
- Guild roles live in in-memory `SessionParameters.guildGroups` (`evr_session_parameters.go:48`) + the shared registry; permissions computed on demand via `HasRole`/`MembershipBitSet`. No JWT/claims assembly. Assembly point would be `initializeSession` (~`evr_pipeline_login.go:609-684`).

## 3. Core insight

**Invert `role → [users]` to `user → [roles]`.** Membership becomes a property of the *user*, not a roster owned by the *guild*. Nobody ever holds the 60k.

This makes membership and bans the **same shape** — both hang off the user, read by one gate. #467 already put bans on the user (sparse enforcement records); this puts membership there too.

## 4. Principles

1. **Discord is write-only TO us, never read BY us.** The gateway pushes events; we persist; our store is the runtime truth. Nothing in any request path touches Discord.
2. **Allow-side (membership) = eventually consistent.** Stale-allow (a just-left member plays one more match) is a rounding error.
3. **Deny-side (bans) = strongly consistent**, live-checked at every join against the sparse enforcement store (#467).
4. **JWT/session carries membership, NEVER bans.** A token you can't un-issue is fine for "allowed", fatal for "forbidden."
5. **Cold start: deny once, they retry.** Already the live behavior (30s retry). Keep it.

## 5. Proposed design

1. **Per-user role records** (user→roles) replace the per-guild `RoleCache` blob → O(1) writes per change, no 60s full-guild reload.
2. **Gate reads the user's own record / session param** — never the giant guild map, never live Discord.
3. **Kill Branch B's Discord call** — the no-member-role path reads the same local per-user store, fed by events.
4. **Keep lazy population + the existing 30s deny-retry**; ensure the retry re-reads fresh per-user state (claim if present, else a per-user cache read the async sync populates — NOT a frozen token).
5. **Add the missing role-change event handlers** (or fold role changes into the per-user path) — close the gap from §2.
6. **Preserve the membership rule:** member-role-if-defined, else guild-membership counts.

## 6. Staleness model

Runs at login / token-refresh only, never on the hot path:
```
needsResolve =
     neverSynced                          // cold → resolve before allowing
  || user.roles_updated_at > built_from   // observed change → dirty (stamp bumped by event handlers)
  || (now - synced_at) > TTL              // missed-event backstop (gateway gaps)
```
Version-stamp catches everything we *saw*; TTL catches everything we *missed*. Resolution reads our local store (gateway-fed), **never Discord**.

## 7. Open decisions

- **EVRL config:** does EVRL define a Member role (→ Branch A, already sane, just needs the inversion for scale) or is it "everyone in the guild counts" (→ Branch B, **live Discord on the hot path today = a current prod fragility**)? This decides whether §5.3 is a fix or future-proofing.
- **#228 now or later:** surface roles as a JWT claim, or keep reading in-memory session params (already effectively per-session)? The gate currently reads the registry, not session params — reconcile.
- Backfill posture for guilds that predate the listener (stay pure-lazy vs. a one-time gateway member-chunk ingest — note: no chunking exists today).

## 8. Non-goals

- No bulk roster materialization. No live Discord in any request path. No ban state in tokens.

## 9. Resume checklist

- [ ] Get EVRL member-role answer (§7)
- [ ] Decide #228 claim vs session-param read
- [ ] Run this through `/review-plan`
- [ ] Then scope implementation PRs (storage migration, gate rewrite, role-event handlers)
