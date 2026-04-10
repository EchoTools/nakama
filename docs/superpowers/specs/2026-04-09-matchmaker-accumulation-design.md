# Matchmaker Accumulation Design

## Problem

High-skill parties (e.g., 3-stack at mu=61) never get matched because the matchmaker greedily consumes the best available players into average-skill matches every cycle. By the time the high-skill party's match is assembled, only low-skill players remain. No settings combination fixes this — the test harness confirms identical outcomes across all presets.

## Solution

Pre-formation accumulation pools that reserve best-fit players for starving tickets across multiple matchmaker cycles, preventing greedy consumption.

## New State

Add to `SkillBasedMatchmaker` (replacing `starvingTickets`, `reservationMu`, and `StarvingTicket`):

```go
type AccumulationPool struct {
    StarvingTicket  string            // the ticket ID being accumulated for
    StarvingEntries []string          // session IDs of the starving ticket's players
    ReservedPlayers []string          // session IDs of accumulated best-fit players
    CenterMu        float64          // mean of all party members' rating_mu
    SkillRadius     float64          // current acceptable skill range (widens over time)
    CreatedAt       time.Time
    TargetSize      int              // read from max_team_size * 2 on the starving ticket's entries
}

accumulationPools   map[string]*AccumulationPool // ticket ID -> pool
accumulationMu      sync.Mutex                   // protects accumulationPools
```

`CenterMu` is the arithmetic mean of `rating_mu` across all entries in the starving ticket. `TargetSize` is read from `max_team_size * 2` on the first entry (same source as `groupEntriesSequentially`).

## Flow Change in `processPotentialMatches`

Current flow:

```
entries → groupEntriesSequentially → filterWithinMaxRTT → predict → sort → assemble
```

New flow:

```
entries → accumulateForStarving → groupEntriesSequentially → filterWithinMaxRTT → predict → sort → assembleUniqueMatches
```

### Step: `accumulateForStarving`

Runs before `groupEntriesSequentially`. Receives all entries, returns (pre-formed candidates, remaining entries).

1. **Prune stale state.** Remove accumulation pools whose starving ticket is no longer in the entry pool (they left the queue). Remove reserved player IDs who are no longer in the entry pool. Remove accumulation pools older than `AccumulationMaxAgeSecs` (safety valve).

2. **Identify starving tickets.** Any ticket with wait time >= `AccumulationThresholdSecs` (configurable, default 90s). Create an `AccumulationPool` if one doesn't exist. Compute `CenterMu` as the mean of all party members' `rating_mu`. Set `TargetSize` from `max_team_size * 2` on the entry.

3. **Widen skill radius.** Each cycle: `SkillRadius = InitialRadius + (poolAge * ExpansionPerCycle)`, capped at `AccumulationMaxRadius`. `poolAge` is seconds since the accumulation pool was created, divided by the matchmaker interval (~30s). The radius represents how far from `CenterMu` the system will look for players.

4. **Reserve best-fit players.** For each accumulation pool (oldest first), scan all non-reserved entries within `CenterMu +/- SkillRadius`. Sort by distance from `CenterMu` (closest first). Add to `ReservedPlayers` up to `TargetSize - len(StarvingEntries)`. A player can only be reserved by one accumulation pool.

5. **Promote full pools.** If `len(StarvingEntries) + len(ReservedPlayers) >= TargetSize`, build a pre-formed candidate from the starving entries + reserved players, remove the accumulation pool, and return the candidate alongside the remaining (unreserved) entries. If a promoted candidate later fails RTT filtering, the reserved players return to the general pool next cycle (the accumulation pool was already cleared).

6. **Return.** Pre-formed candidates go directly into the candidates list alongside those from `groupEntriesSequentially`. Reserved players are removed from the entries passed to `groupEntriesSequentially`.

## What This Replaces

The existing `buildReservations` / `assembleMatchesWithReservations` two-pass system. The `EnableTicketReservation` feature flag, `ReservationThresholdSecs`, `MaxReservationRatio`, and `ReservationSafetyValveSecs` settings are replaced by:

| Old Setting                  | New Setting                                         | Default                                    |
| ---------------------------- | --------------------------------------------------- | ------------------------------------------ |
| `ReservationThresholdSecs`   | `AccumulationThresholdSecs`                         | 90                                         |
| `MaxReservationRatio`        | Removed (accumulation is per-ticket, not pool-wide) | —                                          |
| `ReservationSafetyValveSecs` | `AccumulationMaxAgeSecs`                            | 300                                        |
| `EnableTicketReservation`    | `EnableAccumulation`                                | true                                       |
| —                            | `AccumulationInitialRadius`                         | 5.0 (mu units)                             |
| —                            | `AccumulationRadiusExpansionPerCycle`               | 1.0 (mu units per cycle)                   |
| —                            | `AccumulationMaxRadius`                             | 40.0 (mu units — effectively unrestricted) |

## Configurable Settings

Added to `GlobalMatchmakingSettings`:

- `EnableAccumulation` (bool, default true) — feature flag
- `AccumulationThresholdSecs` (int, default 90) — wait time before a ticket enters accumulation
- `AccumulationMaxAgeSecs` (int, default 300) — safety valve: after this, release reservations
- `AccumulationInitialRadius` (float64, default 5.0) — initial skill range around starving ticket's mu
- `AccumulationRadiusExpansionPerCycle` (float64, default 1.0) — how much the radius widens each cycle
- `AccumulationMaxRadius` (float64, default 40.0) — maximum skill radius

## Edge Cases

**Multiple starving tickets competing for the same players.** Earlier-created accumulation pools get priority. Once a player is reserved, they're unavailable to other pools. If a newer starving ticket needs the same player, it must wait for more players to enter the queue or for its radius to widen.

**All players become reserved.** The `AccumulationMaxAgeSecs` safety valve releases all reservations after 5 minutes, allowing normal matching to resume. Additionally, unreserved entries always flow through normal `groupEntriesSequentially`, so non-starving matches aren't blocked entirely.

**Starving ticket leaves the queue.** Pruning step removes the accumulation pool and releases all reserved players back to the general pool.

**Pool has fewer entries than target size.** Accumulation just waits. New players entering the queue get evaluated each cycle.

## Files Modified

- `server/evr_matchmaker.go` — Replace `starvingTickets`/`reservationMu`/`StarvingTicket` with `AccumulationPool` struct, `accumulationPools` map, and `accumulationMu` mutex. Update `NewSkillBasedMatchmaker`. Update logging block (lines 287-289) to log accumulation metrics instead of reservation filter counts.
- `server/evr_matchmaker_process.go` — Add `accumulateForStarving` method. Call it before `groupEntriesSequentially` in `processPotentialMatches`. Remove the `useReservations` branch and `buildReservations`/`assembleMatchesWithReservations` calls. Always use `assembleUniqueMatches`. Add `countModerators` check to `assembleUniqueMatches` (currently only in the deleted reservation path).
- `server/evr_matchmaker_reservation.go` — Delete entirely (replaced by accumulation).
- `server/evr_matchmaker_reservation_test.go` — Delete (tests for removed code).
- `server/evr_global_settings.go` — Replace reservation settings with accumulation settings in `GlobalMatchmakingSettings`. Add defaults in `FixDefaultServiceSettings`.
- `server/evr_runtime_rpc_service_settings.go` — Replace reservation setting validation (lines 209-217) with accumulation setting validation.
- `server/evr_matchmaker_multicycle_test.go` — Update settings presets to use new accumulation settings. Add accumulation-specific scenarios.

## Verification

Run the multi-cycle test harness:

```bash
cd /home/andrew/src/nakama
go test -run TestMulticycle_Sweep -v ./server/...
```

Success criteria from the test output:

- `highSkillParty`: target matches after accumulation (not cycle 0-1 with 35% imbalance). Match imbalance should decrease as the radius widens to include better-fit players.
- `healthyPool`: no regression — 100% match rate, low imbalance.
- `soloHighSkill`: target accumulates and matches with reasonable imbalance.
- `multipleStarving`: both parties eventually match.
