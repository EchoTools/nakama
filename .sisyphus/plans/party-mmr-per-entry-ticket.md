# Party matchmaking: per-member MMR from ticket (EVR-only changes)

## TL;DR

Fix party members showing identical MMR by changing **only `server/evr_*.go`** code to embed per-member MMR into the ticket properties and teaching EVR to read per-member values first (while keeping ticket-level aggregate rating for search). The matchmaker will not load any player information from storage.

**Deliverables**
- EVR party ticket creation populates:
  - **Ticket-level** (aggregate) `rating_mu`/`rating_sigma` for matchmaking query/indexing.
  - **Per-member keys** in the (shared) ticket numeric properties:
    - `rating_mu_<sanitizedUserId>`
    - `rating_sigma_<sanitizedUserId>`
  - EVR uses these per-member keys to derive entrant/PlayerInfo ratings.
- Go tests (EVR-only files) covering per-member rating extraction and ensuring parties no longer collapse to one rating.

**Estimated Effort**: Medium
**Parallel Execution**: YES (2 waves)
**Critical Path**: EVR party ticket builder → EVR rating reader updates → Tests

---

## Context

### Original Request
- Party members appear to “share” the same MMR (including PlayerInfo) when matchmaking.
- Constraint: the **matchmaker must never load player information**; it must rely only on data present in the matchmaking ticket.

### Key Code Facts (verified)
- `server/matchmaker.go:551+ (LocalMatchmaker.Add)` assigns the **same** `Properties`, `StringProperties`, and `NumericProperties` maps to every `MatchmakerEntry` in a ticket (party or solo).
  - Because we are constrained to **only modify `server/evr_*.go`**, we will *not* fix this shared-map behavior in this iteration.
- EVR currently sources rating from multiple places:
  - `entry.NumericProperties["rating_mu"/"rating_sigma"]` (e.g. `server/evr_lobby_builder.go:441-444`)
  - `entry.GetProperties()["rating_mu"]` (e.g. `server/evr_matchmaker_balance_test.go`)
- Therefore, the EVR-only fix must:
  1) embed per-member values into the shared ticket properties (so each entry can look up its own rating), and
  2) update EVR readers to prefer per-member keyed values first.

---

## Work Objectives

### Core Objective
Make party matchmaking tickets carry **distinct per-player MMR** while keeping the matchmaker storage-free.

### Scope
**IN**
- Only MMR fields: `rating_mu`, `rating_sigma`.
- Party ticket-level rating policy for search/indexing: **Average Mu + RMS Sigma**.
- Missing/zero member rating → substitute defaults.
- Guardrail: aggregate ticket rating (`rating_mu`/`rating_sigma`) must **never** be persisted as a per-user rating.

**OUT**
- Per-entry RTT properties.
- Party blocked list/blocked_ids semantics.
- Any DB/storage reads inside matchmaker.

### Must NOT Have (Guardrails)
- Do not modify any non-`server/evr_*.go` files (explicit user constraint).
- Do not add any matchmaker storage reads (hard requirement).
- Do not persist any ticket-derived rating value (aggregate or per-member keys) via `StorageWrite`.
- Do not change matchmaking search semantics: keep using ticket-level aggregate `rating_mu`/`rating_sigma` (and existing SBMM range keys) for queries/indexing.

Rationale:
- Correctly fixing shared per-entry properties requires `server/matchmaker.go` changes, which are out-of-bounds for this iteration.
- Persisted ratings are authoritative and must continue to come from the established leaderboard/statistics pipeline.

### Persistence Guardrail (explicit)
- Ticket-level aggregate `rating_mu`/`rating_sigma` is **matchmaking-only** (search/indexing).
- Per-user rating persistence (leaderboards and any `StorageWrite`) must continue to be sourced from:
  - `MatchmakingRatingLoad` / `MatchmakingPlayerRatingLoad` (load current per-user rating)
  - `CalculateNew*Ratings` (compute updated per-user rating)
  - `LeaderboardRecordWrite` via the statistics queue
- EVR must not write any user’s persisted rating directly from ticket properties (aggregate or per-member keys).

Verification note:
- As part of execution, audit EVR `StorageWrite` usage and confirm none writes player rating fields sourced from ticket properties.

### Definition of Done
- [ ] Party members’ `PlayerInfo.RatingMu/RatingSigma` can differ when their individual ratings differ.
- [ ] EVR does not depend on per-entry property map isolation (it must work even when matchmaker shares maps across entries).
- [ ] Go tests pass (see Verification Strategy).

---

## Verification Strategy

**Decision**: Add Go tests.

Primary commands (targeted):
```bash
# Run EVR-focused unit tests.
go test -short -vet=off ./server -run "TestEvrRatingFromMatchmakerEntry_.*"

# EVR sanity sweep.
go test -short -vet=off ./server -run ".*evr.*"
```

Persistence guardrail audit (agent-executable):
```bash
# Ensure no EVR StorageWrite payload includes rating fields sourced from ticket properties.
rg -n "StorageWrite\(" server/evr_*.go
rg -n "rating_mu|rating_sigma" server/evr_*.go

# If ripgrep (rg) is unavailable:
grep -R "StorageWrite(" server/evr_*.go
grep -R "rating_mu\|rating_sigma" server/evr_*.go
```

What you should expect to see:
- `StorageWrite` call sites exist in EVR code for unrelated features (linking, settings, journals, etc.).
- There should be **no** `StorageWrite` payloads that include `rating_mu`, `rating_sigma`, `rating_mu_*`, or `rating_sigma_*`.

Known EVR files with `StorageWrite` usage (audit these first):
- `server/evr_linking.go`
- `server/evr_pipeline_login.go`
- `server/evr_global_settings.go`
- `server/evr_discord_appbot.go`
- `server/evr_runtime_rpc.go`
- `server/evr_remotelog_journal.go`
- `server/evr_storable.go`
- `server/evr_runtime_vrml_verifier.go`
- `server/evr_runtime_vrml_ledger.go`

---

## Precedence & Validation Rules (MMR only, EVR-only)

When deriving a player rating from a `MatchmakerEntry`:
1. **Per-member key wins** (from shared ticket properties):
   - `rating_mu_<sanitizedUserId>` / `rating_sigma_<sanitizedUserId>`
2. Else **ticket-level** aggregate `rating_mu`/`rating_sigma`.
3. Else **defaults**: `NewDefaultRating()`.

Per-member key formatting:
- `sanitizedUserId` is derived from `entry.Presence.UserId` by **sanitizing** it for safe use as a property key.
- Assumption (confirmed by user): `userId` is always a UUID.
- Confirmed rule: `strings.ToLower(userId)` and strip `-` characters.

Sanitization:
- Treat `Mu <= 0`, `Sigma <= 0`, `NaN`, and `Inf` as invalid → replace with defaults.
- Negative sigma is invalid → replace with defaults.

---

## Execution Strategy

### Wave 1
1) EVR: build party ticket that includes per-member numeric keys + aggregate ticket rating
2) EVR: centralize rating extraction from `MatchmakerEntry` (helper) and update all EVR readers

### Wave 2
3) EVR-only regression tests ensuring party members don’t collapse to one rating

---

## Implementation Map (exact edits)

> Goal: a developer can follow this list without guessing where changes go.

1) **Add helper**
- Add: `server/evr_matchmaker_entry_rating.go`
  - Add: `EvrRatingFromMatchmakerEntry(entry runtime.MatchmakerEntry) types.Rating`
  - Add: `evrSanitizedUserIDForKey`, `evrRatingMuKey`, `evrRatingSigmaKey`

2) **Party ticket builder**
- Edit: `server/evr_lobby_matchmake.go`
  - `func (p *EvrPipeline) lobbyMatchMakeWithFallback(..., entrants ...*EvrMatchPresence)` (was discarding entrants at `:143`)
  - `replaceTicket` closure inside it must pass `entrants...` into `addTicket`.
  - `func (p *EvrPipeline) addTicket(..., ticketConfig MatchmakingTicketParameters, entrants ...*EvrMatchPresence)`
  - In party branch (currently `lobbyGroup.MatchmakerAdd(...)` around `:278-285`):
    - Build `presences` from `entrants` and call `session.matchmaker.Add(...)` with `partyId = lobbyGroup.IDStr()`.
    - Add per-user numeric keys into `numericProps` using sanitized user IDs.

3) **Use helper in match building**
- Edit: `server/evr_lobby_builder.go`
  - `func (b *LobbyBuilder) buildMatch(...)` at `:394+`
  - Replace `mu := entry.NumericProperties["rating_mu"]` / sigma with `rating := EvrRatingFromMatchmakerEntry(entry)`.

4) **Use helper in post-matchmaker backfill joins**
- Edit: `server/evr_lobby_backfill.go`
  - `func (b *PostMatchmakerBackfill) executeBackfillResultOptimized(...)` at `:277+`
    - Replace `entry.NumericProperties["rating_mu"/"rating_sigma"]` with `EvrRatingFromMatchmakerEntry(entry)`.
  - `func (b *PostMatchmakerBackfill) executeBackfillResult(...)` at `:947+`
    - Replace `entry.NumericProperties["rating_mu"/"rating_sigma"]` with `EvrRatingFromMatchmakerEntry(entry)`.

5) **Use helper in balancing/prediction**
- Edit: `server/evr_matchmaker_prediction.go`
  - `func (g MatchmakerEntries) Ratings(...)`
  - `func (g MatchmakerEntries) RatingsInto(...)`
  - `func (g MatchmakerEntries) RatingsWithPartyBoost(...)`
  - Replace direct `props["rating_mu"/"rating_sigma"]` reads with `EvrRatingFromMatchmakerEntry(e)`.

6) **Tests**
- Add: `server/evr_matchmaker_entry_rating_test.go`
  - Implement `TestEvrRatingFromMatchmakerEntry_PerUserKeysWin`
  - Implement `TestEvrRatingFromMatchmakerEntry_FallbackAggregate`
  - Implement `TestEvrRatingFromMatchmakerEntry_InvalidValuesDefault`

---

## TODOs

### 1) EVR: embed per-member MMR into ticket properties (per-user numeric keys)

**What to do**
- In the EVR party ticket submission path (in `server/evr_lobby_matchmake.go`):
  - Thread `entrants ...*EvrMatchPresence` through `lobbyMatchMakeWithFallback` → `replaceTicket` → `addTicket`.
  - Compute ticket-level aggregate rating (Average Mu + RMS Sigma) and set normal keys:
    - `mu_party = mean(validEntrantMu)`
    - `sigma_party = sqrt(mean(validEntrantSigma^2))`
    - For any entrant with invalid rating (mu/sigma <= 0, NaN/Inf): substitute `NewDefaultRating()` for that entrant in the aggregate calculation.
    - `numericProperties["rating_mu"] = mu_party`
    - `numericProperties["rating_sigma"] = sigma_party`
  - Add per-member keys to the *same* `numericProperties` map:
    - `numericProperties["rating_mu_"+sanitizedUserId] = entrant.Rating.Mu`
    - `numericProperties["rating_sigma_"+sanitizedUserId] = entrant.Rating.Sigma`
  - `sanitizedUserId` should match the formatting rule above.
  - Build matchmaker presences from `entrants` (do not use PartyHandler for the ticket payload):
    - For each entrant:
      - `UserId` = `entrant.UserID.String()`
      - `SessionId` = `entrant.SessionID.String()`
      - `Username` = `entrant.Username`
      - `Node` = `entrant.Node` (or `p.node` if that is the established convention)
      - `SessionID` = `entrant.SessionID` (uuid.UUID)
    - Submit via `session.matchmaker.Add(ctx, presences, session.ID().String(), lobbyGroup.IDStr(), query, ...)`.
- Keep query/indexing behavior unchanged: search still uses `rating_mu` and the SBMM range keys.

**Explicit edit points (call chain + signatures)**
- `server/evr_lobby_find.go:149-154` calls `p.lobbyMatchMakeWithFallback(..., entrants...)`.
- `server/evr_lobby_matchmake.go:143` change `lobbyMatchMakeWithFallback(..., _ ...*EvrMatchPresence)` → `(..., entrants ...*EvrMatchPresence)`.
- `server/evr_lobby_matchmake.go:181-201` inside `replaceTicket`, change:
  - `p.addTicket(ctx, logger, session, lobbyParams, lobbyGroup, newTicketConfig)`
  - to pass entrants through: `p.addTicket(ctx, logger, session, lobbyParams, lobbyGroup, newTicketConfig, entrants...)`.
- `server/evr_lobby_matchmake.go:244` change `addTicket(..., ticketConfig MatchmakingTicketParameters)` to accept `entrants ...*EvrMatchPresence`.
- `server/evr_lobby_matchmake.go:278-285` party branch currently uses `lobbyGroup.MatchmakerAdd(...)` → replace with direct `session.matchmaker.Add(...)` using presences built from `entrants` and `partyId = lobbyGroup.IDStr()`.

**Must NOT do**
- Do not modify non-`server/evr_*.go` files.
- Do not add storage/DB reads.
- Do not expand beyond MMR fields.

**References**
- `server/evr_lobby_find.go:91-99` — party entrants created and passed into matchmaking.
- `server/evr_lobby_matchmake.go` — `lobbyMatchMakeWithFallback` / `replaceTicket` / `addTicket`.
- `server/evr_lobby_parameters.go:618-645` — where aggregate `rating_mu`/`rating_sigma` and SBMM range keys are added.
- `server/evr_lobby_builder.go:441-444` — reads `entry.NumericProperties["rating_mu"]` today.
- `server/evr_lobby_backfill.go` — reads `entry.NumericProperties` and `entry.Properties` today.
- `server/evr_matchmaker_prediction.go`, `server/evr_matchmaker.go`, `server/evr_matchmaker_replay.go` — read `props["rating_mu"]` today.

**Acceptance Criteria (automated)**
- [ ] EVR creates party tickets that include per-member keys for all entrants.
- [ ] EVR rating extraction prefers per-member keys and falls back to aggregate/default.
- [ ] EVR-only tests pass (see Verification Strategy).

---

### 2) EVR: centralize and update rating extraction from MatchmakerEntry

**What to do**
- Add a dedicated helper in a new EVR file:
  - **Add file**: `server/evr_matchmaker_entry_rating.go`
  - **Public helper**: `func EvrRatingFromMatchmakerEntry(entry runtime.MatchmakerEntry) types.Rating`
  - **Key helpers** (same file):
    - `func evrSanitizedUserIDForKey(userID string) string` → `strings.ToLower(userID)` and strip `-`.
    - `func evrRatingMuKey(userID string) string` → `"rating_mu_" + evrSanitizedUserIDForKey(userID)`
    - `func evrRatingSigmaKey(userID string) string` → `"rating_sigma_" + evrSanitizedUserIDForKey(userID)`
  - **Lookup order** (must be implemented exactly once here):
    1) Read `props := entry.GetProperties()` and `userID := entry.GetPresence().GetUserId()`.
    2) Prefer per-user keys in `props`:
       - `props[evrRatingMuKey(userID)]` / `props[evrRatingSigmaKey(userID)]`
    3) Else fall back to aggregate keys:
       - `props["rating_mu"]` / `props["rating_sigma"]`
    4) Else `NewDefaultRating()`
  - **Validation**: any missing/invalid mu/sigma (`<=0`, NaN/Inf) → `NewDefaultRating()`
- Update all EVR call sites to use the helper instead of directly indexing `rating_mu`:
  - `server/evr_lobby_builder.go`
  - `server/evr_lobby_backfill.go`
  - `server/evr_matchmaker_prediction.go`

Non-goals (leave aggregate usage intact):
- Places that intentionally operate on *ticket-level aggregates* (e.g. ticket summaries) may continue to read `props["rating_mu"]` from the first entry for that ticket.

**Must NOT do**
- Do not change matchmaker indexing/search semantics (still ticket-level aggregate).

**References**
- `vendor/github.com/heroiclabs/nakama-common/runtime/runtime.go:912-917` — `runtime.MatchmakerEntry` interface (`GetPresence()`, `GetProperties()`).
- Grep results for rating reads in EVR files:
  - `server/evr_lobby_builder.go:441-443` (NumericProperties)
  - `server/evr_lobby_backfill.go:308-309, 448, 522, 964-965` (NumericProperties/Properties/extract)
  - `server/evr_matchmaker_prediction.go:60-65, 90-95, 121-126` (props)

**Acceptance Criteria (automated)**
- [ ] Unit tests (EVR-only) validate that two entries sharing the same ticket property maps still yield different ratings when per-user keys are present.

Implementation checklist (to avoid “missed call sites”):
- `server/evr_matchmaker_prediction.go`:
  - `func (g MatchmakerEntries) Ratings(...)`
  - `func (g MatchmakerEntries) RatingsInto(...)`
  - `func (g MatchmakerEntries) RatingsWithPartyBoost(...)`
  - Replace direct `props["rating_mu"]` reads with `EvrRatingFromMatchmakerEntry(e)`.

---

### 3) EVR-only tests + persistence audit

**What to do**
- Add a new EVR-focused test file:
  - Recommended: `server/evr_matchmaker_entry_rating_test.go`
- Tests should construct `MatchmakerEntry` objects where multiple entries deliberately share the same `NumericProperties` and/or `Properties` map (to mirror matchmaker’s shared-map behavior), then assert the EVR helper still returns per-user ratings correctly.

**Concrete test names (required)**
- `TestEvrRatingFromMatchmakerEntry_PerUserKeysWin`
- `TestEvrRatingFromMatchmakerEntry_FallbackAggregate`
- `TestEvrRatingFromMatchmakerEntry_InvalidValuesDefault`

**Acceptance Criteria (automated)**
- [ ] `go test -short -vet=off ./server -run "TestEvrRatingFromMatchmakerEntry_.*"` → PASS.
- [ ] `go test -short -vet=off ./server -run ".*evr.*"` → PASS.
- [ ] Persistence audit completed with the commands in “Verification Strategy” and no `StorageWrite` includes ticket-derived rating fields.

---

## Commit Strategy

- Prefer 1 commit (EVR-only) since scope is constrained to `server/evr_*.go`.

---

## Success Criteria

```bash
go test -short -vet=off ./server -run ".*evr.*"
```

- [ ] Party members no longer share identical rating fields unless they truly have the same rating.
- [ ] No storage reads added to matchmaker.
- [ ] (Known limitation) Matchmaker will still share maps across entries; EVR must always use per-member keys first.
