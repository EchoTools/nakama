# Draft: Matchmaker scalability reassessment

## Context (confirmed)
- EchoVR fork registers a matchmaker override: `initializer.RegisterMatchmakerOverride(sbmm.EvrMatchmakerFn)` in `server/evr_runtime.go`.
- When an override is present, `LocalMatchmaker.Process()` calls `processCustom(...)` (see `server/matchmaker.go`).
- `processCustom` builds candidate matches by querying Bluge via `bluge.NewTopNSearch(indexCount, indexQuery)` (see `server/matchmaker_process.go:398`) where `indexCount = len(m.indexes)` (all tickets, not just active).
- `processCustom` then iterates returned hits to build `hitIndexes`, then later truncates to 24 hits (oldest 8 + newest 16) before exploring combinations.

## Operational context (user report)
- Production issue is mostly a "single giant queue": ~30+ players whose queries largely match each other, so candidate sets are large even with structured queries.

## Key observation (why this matters)
- Even though `hitIndexes` is later truncated to 24, the code still pays the cost of requesting/iterating up to `indexCount` results from Bluge per active ticket.

## Research findings (codebase)

### Matchmaker interface contract (authoritative)
- `type Matchmaker interface` is defined in `server/matchmaker.go:175-192`.
- Methods include: `Pause/Resume/Stop`, `OnMatchedEntries`, `OnStatsUpdate`, `Add`, `Insert/Extract`, `RemoveSession*`, `RemoveParty*`, `RemoveAll`, `Remove`, `GetStats/SetStats`.
- Interface signature dependencies:
  - `server/matchmaker.go`: `MatchmakerPresence`, `MatchmakerEntry`, `MatchmakerExtract`
  - `vendor/github.com/heroiclabs/nakama-common/api/api.pb.go`: `api.MatchmakerStats`

### EVR direct couplings to LocalMatchmaker (must remove to swap implementations)
- `server/evr_pipeline.go`
  - `globalMatchmaker := atomic.NewPointer[LocalMatchmaker](nil)` (hard-coded concrete type)
  - `globalMatchmaker.Store(matchmaker.(*LocalMatchmaker))` (type assertion)
- `server/evr_lobby_builder.go`
  - Loads `globalMatchmaker` singleton.
  - Reads `matchmaker.config.GetMatchmaker().IntervalSec` (direct access to LocalMatchmaker internals).
  - Calls `matchmaker.Extract()` (method exists on interface, but reached via concrete global).
- `server/evr_runtime_rpc_matchmaker.go`
  - Loads `globalMatchmaker` singleton.
  - Returns `Stats: matchmaker.GetStats(), Index: matchmaker.Extract()`.

### Where RTAPI matchmaker messages are emitted
- `MatchmakerTicket` is sent by standard RTAPI realtime handler: `server/pipeline_matchmaker.go` in response to RTAPI `MatchmakerAdd`.
- `MatchmakerMatched` is sent by the matchmaker processing loop: `server/matchmaker.go` after `matchedEntries` are formed; routed via `router.SendToPresenceIDs(...)`.
- EVR flow calls `matchmaker.Add(...)` directly in `server/evr_lobby_matchmake.go` (tickets created server-side) and consumes `OnMatchedEntries` to start join flow.
- EVR client join is primarily via EVR protocol (`LobbySessionSuccess`, and `LobbySessionSuccessV5` to game server) in `server/evr_lobby_joinentrant.go`.

### Query parsing semantics
- `ParseQueryString` lives in `server/match_common.go` and delegates to vendored `github.com/blugelabs/query_string` with a **keyword analyzer** by default.
- Operators map as:
  - `+` → `BooleanQuery.AddMust(...)`
  - `-` → `BooleanQuery.AddMustNot(...)`
  - none → `BooleanQuery.AddShould(...)`
- `/regex/` tokens become `bluge.NewRegexpQuery(pattern).SetField(field)`.
- Regex execution is via vellum automaton compilation (default compiled-size limit ~10MB); patterns like `/.*uuid.*/` can be expensive.

### Property indexing semantics
- `MapMatchmakerIndex` indexes `properties.*` via `BlugeWalkDocument`.
- `properties.*` strings are indexed as **KeywordField** (unless the string parses as datetime).
- Numeric values are indexed as **NumericField**.
- Arrays/slices are indexed as **multi-valued fields** (each element added under the same field).

### Candidate generation cost drivers
- `processCustom` and `processDefault` both call `bluge.NewTopNSearch(indexCount, ...)` where `indexCount = len(m.indexes)`.
- `IterateBlugeMatches` materializes **all returned hits** into a slice (no early stop).
- In `processCustom`, `hitIndexes` is sorted then capped: if `len(hitIndexes) > 24`, it keeps oldest 8 + newest 16.

### Bluge TopN semantics
- `NewTopNSearch(N, query)` sets the **number of hits to return** and also sizes the TopN collector; large `N` increases memory and per-hit bookkeeping.
- The collector still iterates all matches from the underlying searcher; with large `N` it maintains a large heap/slice of candidates.
- With `SortBy` set, Bluge loads doc values for sort fields and computes per-hit sort keys.

### EVR blocked_ids modeling
~(Removed per user request: do not focus on `blocked_ids` modeling/regex in this conversation.)~

## Hypotheses to validate
- H1: `indexCount` as TopN is an over-request; a smaller TopN (e.g., 100-500) would preserve behavior while avoiding O(active * total) result materialization.
- H2: The worst-case happens when queries are broad (e.g., `"*"` / match-all), making nearly all tickets candidates.
- H2b: Even without `"*"`, a shared `properties.group_id` + `properties.game_mode` across most tickets effectively produces a match-all within that partition.
- H3: Backlog size may be inflated by tickets not being removed (disconnect/abandon scenarios), not just by intentional “desperation matching”.

## Open questions
- What is the typical query string in production (often `"*"` or selective)?
- Should tickets that reach `MaxIntervals` remain matchable forever, or should there be an expiry/eviction policy?
- Are the ~510k “inactive backlog” tickets truly abandoned/stale (no connected sessions), or legitimate waiting pool?

### Replacement design questions (new)
- Should EVR replacement still emit RTAPI `MatchmakerMatched` / `MatchmakerTicket` exactly as today, or is EVR-only client signaling sufficient? (Note: RTAPI `MatchmakerTicket` is only emitted via `pipeline_matchmaker.go`; EVR does not go through that path.)
- Do we need to preserve `MatchmakerStats` semantics and `Extract()` contents for the existing EVR RPC (`evr_runtime_rpc_matchmaker.go`), or can those be narrowed?
- Preferred approach for decoupling EVR from LocalMatchmaker:
  - Keep a global, but store an interface (e.g., `atomic.Value` holding `Matchmaker` or a narrower `MatchmakerStateProvider`), OR
  - Remove globals and inject matchmaker/state provider + interval config into `LobbyBuilder` and RPC registration.

## Constraints (new)
- User prefers solutions that only modify `server/evr_*.go` (avoid touching core Nakama matchmaker). Considering alternative: replace/bypass Nakama matchmaker with an EVR-owned equivalent.

## Direction (user decision)
- User wants an **EVR-owned matchmaker loop** (not just a runtime override).

## Protocol constraint (user decision)
- Keep existing RTAPI message semantics: clients still receive `MatchmakerTicket` ack and `MatchmakerMatched` notifications (no protocol changes).

### Clarification (user decision)
- RTAPI compatibility level for replacement: **Keep as-is** (do not change RTAPI behavior; EVR can continue relying on EVR messages).

## Current usage clarification (user report)
- User believes `pipeline_matchmaker.go` / `pipeline_party.go` RTAPI matchmaker handlers are not used in production; tickets are entered via EVR `LobbyFindSessionRequest` flow only.

## Deployment constraint (user decision)
- Replacement is **single-node only** (no cluster/distributed coordination required initially).

## Implementation direction (user decision)
- Decoupling approach: **Keep global, store interface** (replace `globalMatchmaker` concrete pointer + type assertion; use interface storage + pass timing/config separately).

## Design option under consideration: Bluge reuse
- Question: Can/should the EVR-owned matchmaker still use Bluge for indexing/search?
- Option A (reuse Bluge): maintain a Bluge index in the EVR matchmaker, reuse existing query parsing/indexing helpers to preserve query semantics.
- Option B (no Bluge): use in-memory matching + partitioning on known EVR fields to avoid broad searches.
- Key trade-off: Bluge preserves Nakama query language but risks repeating the current TopN/materialization cost if not carefully bounded.

---

## Status: Paused / Tabled
- User requested to table this work and revisit later.

## Current decisions (so far)
- Goal: Replace Nakama `LocalMatchmaker` with an EVR-owned single-node matchmaker loop (interface-compatible swap).
- Do not focus on `blocked_ids`.
- RTAPI compatibility: keep as-is (do not change RTAPI behavior; EVR can continue relying on EVR messages).
- Decoupling approach: keep a global but store an interface (remove `*LocalMatchmaker` cast and `.config` access).

## Immediate next steps when resuming
1) Decide query semantics for EVR matchmaker:
   - Preserve full Nakama query-string semantics (likely via Bluge + existing parsing helpers), OR
   - EVR-structured matching (limit/ignore query language).
2) Plan the minimal refactor to remove EVR hard coupling to `*LocalMatchmaker`:
   - `server/evr_pipeline.go`: global matchmaker storage -> interface
   - `server/evr_lobby_builder.go`: stop `.config...IntervalSec`, pass interval from config
   - `server/evr_runtime_rpc_matchmaker.go`: read interface state provider
3) Only after decoupling: introduce `EvrMatchmaker` implementing `server.Matchmaker` and swap constructor in `main.go`.

## Pointers / key references
- Matchmaker interface contract: `server/matchmaker.go:175-192`
- EVR couplings: `server/evr_pipeline.go`, `server/evr_lobby_builder.go`, `server/evr_runtime_rpc_matchmaker.go`
- RTAPI sends:
  - `MatchmakerTicket`: `server/pipeline_matchmaker.go` (RTAPI MatchmakerAdd path)
  - `MatchmakerMatched`: `server/matchmaker.go` (after matches formed)
- EVR match found/join: `OnMatchedEntries` -> `server/evr_lobby_builder.go` -> `server/evr_lobby_joinentrant.go`
