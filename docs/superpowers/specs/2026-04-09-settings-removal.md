# Matchmaker Settings Removal Spec

## Goal

Remove 9 configurable settings from `GlobalMatchmakingSettings` that are either redundant with the new accumulation system, have a single correct value, or work against the accumulation system by splitting the player pool before our pipeline sees it.

## Settings to Remove

### Pipeline settings (hardcode in processPotentialMatches)

1. **`WaitTimePriorityThresholdSecs`** (int, default 120)
   - Used at: `server/evr_matchmaker_process.go` — flips sort between "size first" and "wait time first"
   - Hardcode behavior: always sort by size first, then wait time (the `prioritizeWaitTime = false` branch)
   - Rationale: accumulation handles starving tickets directly; the sort priority flip was a workaround for the same problem

2. **`EnableRosterVariants`** (bool, default false)
   - Used at: `server/evr_matchmaker_process.go` — passed to `PredictionConfig.EnableRosterVariants`
   - Hardcode behavior: always `true` — generate both sequential and snake draft variants
   - Rationale: generating both is cheap and the best variant wins in assembly

3. **`UseSnakeDraftTeamFormation`** (bool, default false)
   - Used at: `server/evr_matchmaker_process.go` — passed to `PredictionConfig.UseSnakeDraftFormation`
   - Hardcode behavior: always `true` — snake draft is the primary variant
   - Rationale: snake draft consistently produces better team balance; with `EnableRosterVariants` always true, sequential is generated as the secondary variant anyway

4. **`PartySkillBoostPercent`** (float64, default 0.10)
   - Used at: `server/evr_matchmaker_process.go` — passed to `PredictionConfig.PartyBoostPercent`
   - Hardcode behavior: always `0.10`
   - Rationale: stable value, never changed in production

### Ticket creation settings (remove Nakama pre-filter)

5. **`SBMMMinPlayerCount`** (int, default 24)
   - Used at: `server/evr_lobby_matchmake.go:171` — when player count < threshold, sets `IncludeSBMMRanges = false`
   - Removal behavior: always set `IncludeSBMMRanges = false` — never use Nakama's range pre-filter
   - Rationale: the Nakama pre-filter splits the pool before our pipeline sees it, working against accumulation. Our pipeline handles skill matching; Nakama should send us everyone.

6. **`EnableOrdinalRange`** (bool, default false)
   - Used at: `server/evr_lobby_parameters.go:394,656` — gates whether rating range query is added to matchmaking tickets
   - Removal behavior: never add rating range queries to tickets
   - Rationale: same as SBMMMinPlayerCount — pre-filtering by rating range at the Nakama level prevents our pipeline from seeing the full pool

7. **`RatingRange`** (float64, default 0)
   - Used at: `server/evr_lobby_parameters.go:396,505,659` — base rating range for ticket queries
   - Removal behavior: no range queries on tickets
   - Rationale: consumed only by EnableOrdinalRange logic, which is being removed

8. **`RatingRangeExpansionPerMinute`** (float64, default 0.5)
   - Used at: `server/evr_lobby_parameters.go:447` — widens rating range over time for ticket queries
   - Removal behavior: no range expansion needed
   - Rationale: consumed by `calculateExpandedRatingRange`, which is called from two sites: (a) matchmaking ticket creation gated by `EnableOrdinalRange` (line 659), and (b) `BackfillSearchQuery` gated by `includeMMR` (line 505). Both are being removed — backfill should also see the full pool.

9. **`MaxRatingRangeExpansion`** (float64, default 5.0)
   - Used at: `server/evr_lobby_parameters.go:451` — caps rating range expansion
   - Removal behavior: no range cap needed
   - Rationale: consumed only by `calculateExpandedRatingRange`, which is being deleted

## Files Modified

- `server/evr_global_settings.go` — remove 9 fields from `GlobalMatchmakingSettings`, remove defaults from `FixDefaultServiceSettings`
- `server/evr_matchmaker_process.go` — hardcode PredictionConfig values (always snake draft, always both variants, PartyBoostPercent=0.10), remove sort priority branching (always size-first)
- `server/evr_lobby_parameters.go` — remove `EnableOrdinalRange`, `RatingRange`, `MatchmakingRatingRange` from `LobbySessionParameters`, delete `calculateExpandedRatingRange` function, remove MMR range filtering from both `MatchmakingParameters` (line 656) and `BackfillSearchQuery` (line 502-511), remove `includeMMR` parameter from `BackfillSearchQuery` signature (becomes dead after MMR block removal; callers at `evr_lobby_status.go:85` and `evr_lobby_find.go:461` updated to drop the arg)
- `server/evr_lobby_matchmake.go` — remove `SBMMMinPlayerCount` check. Remove `IncludeSBMMRanges` field from `MatchmakingTicketParameters` struct. Update `MarshalText`/`UnmarshalText` methods (lines 80-117) which encode `IncludeSBMMRanges` as the 4th of 5 slash-delimited fields — drop the field from the format and adjust `UnmarshalText` to expect 4 parts instead of 5.
- `server/evr_lobby_status.go` — remove `IncludeSBMMRanges: true` at line 64 (field no longer exists on struct).
- `server/evr_matchmaker_test.go` — remove `IncludeSBMMRanges: true` at line 604 (field no longer exists on struct).
- `server/evr_runtime_rpc_service_settings.go` — remove validation for deleted settings
- `server/evr_runtime_rpc_matchmaker_config.go` — remove deleted settings from config RPC response struct and mapping
- `server/evr_matchmaker_multicycle_test.go` — remove references to deleted settings from test presets
- `server/evr_matchmaker_waittime_test.go` — update or remove tests that exercise `WaitTimePriorityThresholdSecs` branching
- `server/evr_matchmaker_prediction_test.go` — tests using `EnableRosterVariants: false` become dead code paths; update to always use `true`
- `server/evr_matchmaker_balance_test.go` — update `EnableRosterVariants` references
- `server/evr_lobby_find.go` — update `BackfillSearchQuery` call site to drop `includeMMR` arg

Note: `PredictionConfig.EnableRosterVariants`, `PredictionConfig.UseSnakeDraftFormation`, and `PredictionConfig.PartyBoostPercent` struct fields remain in `evr_matchmaker_prediction.go` — only the global settings that populate them are removed. The fields are hardcoded in `processPotentialMatches`.

## Guardrails

- `EnableSBMM` is explicitly kept — it remains the kill switch for skill-based matchmaking
- `rating_mu` and `rating_sigma` are still added to ticket properties when `EnableSBMM` is true — only the Nakama-level range pre-filter is removed
- The matchmaker pipeline (`processPotentialMatches`) still does full skill-based team formation, accumulation, and balance scoring — this change only removes knobs that toggle between "always correct" values

## Testing

- Run `go test -run "TestAccumulateForStarving|TestMulticycle|TestCharacterization|TestBalanced|TestPartySkill|TestDraw|TestOldest|TestDynamic|TestGroupEntries|TestProcessPotentialMatches|TestHasEligible|TestFilter|TestVariant" -v ./server/...` — all must pass
- Run `go build ./server/...` — clean build
- Verify the multicycle sweep still shows accumulation working: `go test -run TestMulticycle_Sweep -v ./server/...`
