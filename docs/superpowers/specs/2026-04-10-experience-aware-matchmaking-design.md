# Experience-Aware Matchmaking

**Date:** 2026-04-10
**Branch:** `matchmaker-accumulation-and-cleanup`
**Status:** Design

## Problem

The EchoVR matchmaker produces stomps in 54% of completed matches (score diff >= 5). The team with the single best player wins 75% of the time. Even "well-balanced" matches (avg mu gap < 3) average a 4.6-point score differential because intra-team skill spread is extreme (median 21.6 mu gap between best and worst teammate).

Three compounding failures:

1. **Candidate pool mixing** — A mu-71 and mu-6 player end up in the same candidate. No team formation fixes this.
2. **Best-player-always-wins bias** — Snake draft gives the highest-ranked group first pick every time. That player's team wins 75% of matches.
3. **MMR death spiral** — A mu-40 player gets stomped 5 times, drops to mu-25, gets matched with players who can't execute fundamentals (regrab, passing). They lose more, drop further, and quit.

Source: Analysis of 1,734 valid matches from April 3-10 2026 (`~/src/muse/incoming/mm-analysis/`).

## Principles

- **Rating mu is sacred.** It reflects what actually happened. Never adjust a player's stored rating for matchmaking purposes. All adjustments are matchmaking-side only, computed at queue time, and exist solely on the ticket.
- **Optimize for experience over time, not single-match balance.** A player's recent history (stomps received, losing streaks, match count) matters as much as their current mu.
- **Session duration is the north star metric.** Players who have a good time keep queuing. Every feature ships with a measurable prediction about session duration impact.

## Architecture Overview

The system adds a **ticket enrichment** step at queue time and modifies **candidate scoring** and **team assignment** in the matchmaker pipeline.

```
Player queues for matchmaking
        ↓
TICKET ENRICHMENT (new) ← reads MongoDB + nakama storage
  - effective_mu (peak-blended)
  - experience_score (recent stomp/win/loss history)
  - match_count (total completed matches)
  - suspension_count (enforcement journal)
  - is_moderator (existing)
        ↓
Ticket submitted to nakama matchmaker with enriched properties
        ↓
MATCHMAKER PIPELINE (modified)
  1. Accumulation (existing, modified) — switch from rating_mu to effective_mu for skill radius
  2. Candidate formation (existing) — groupEntriesSequentially
  3. RTT filtering (existing)
  4. QUALITY FLOOR (new) — reject candidates below draw prob threshold
  5. Prediction + team formation (modified) — experience-aware team assignment
  6. CANDIDATE SCORING (modified) — affinity signals for new player protection
  7. Assembly (existing) — assembleUniqueMatches
```

## Feature 1: Effective Mu (Anti-Death-Spiral)

### What it does

At queue time, compute an `effective_mu` that blends the player's current rating mu with their peak mu over the last 30 days. This prevents players on losing streaks from falling into skill brackets where teammates can't execute basic game mechanics.

### How it works

```
effective_mu = current_mu + recovery_factor * max(0, peak_30d_mu - current_mu)
```

Where:

- `current_mu` — the player's actual OpenSkill mu (unchanged, read from nakama)
- `peak_30d_mu` — highest mu observed in match_summaries over the last 30 days
- `recovery_factor` — 0.5 (tunable). Pulls effective_mu halfway between current and peak.

Examples:

- Player at mu=25, peak=40 → effective_mu = 25 + 0.5 \* 15 = 32.5
- Player at mu=40, peak=42 → effective_mu = 40 + 0.5 \* 2 = 41.0 (negligible adjustment)
- Player at mu=40, peak=40 → effective_mu = 40.0 (no adjustment)

### Data source

MongoDB `nevr.match_summaries` collection. Query at queue time:

```
db.match_summaries.find(
  {"participants.user_id": <uid>, "end_time": {$gte: <30 days ago>}},
  {"participants.$": 1}
).sort({"end_time": -1})
```

The index on `participants.user_id` already exists (created by `EnsureMatchSummaryIndexes`).

Peak mu is not stored directly in match_summaries — it's the player's `rating_mu` at queue time, captured when the ticket was submitted. To compute peak_30d_mu, either:

**(A)** Add a `rating_mu` field to `PlayerParticipation` in match summaries (preferred — captures the rating at match time). Requires a schema addition.

**(B)** Use nakama storage to persist a rolling `peak_mu` value, updated after each match during the rating update step. No MongoDB schema change needed.

**Decision: (B).** Store `peak_mu` and `peak_mu_updated_at` in the player's nakama storage object alongside their existing rating data. Update it after each match if current_mu > peak_mu or if peak_mu_updated_at is older than 30 days (reset to current). This avoids scanning MongoDB at queue time and keeps the query path fast.

### Where it integrates

- **Queue time** (`evr_lobby_matchmake.go`): Read `peak_mu` from nakama storage, compute `effective_mu`, set it as a ticket numeric property `effective_mu`.
- **Accumulation** (`evr_matchmaker_accumulation.go`): Use `effective_mu` instead of `rating_mu` for `CenterMu` computation and skill radius matching.
- **Prediction** (`evr_matchmaker_prediction.go`): Use `effective_mu` for team formation ranking (PredictRank). Continue using actual `rating_mu` for draw probability calculation (PredictDraw) — draw prob must reflect reality, not the adjusted signal.

### Configuration

| Setting                     | Default | Description                                           |
| --------------------------- | ------- | ----------------------------------------------------- |
| `EffectiveMuRecoveryFactor` | 0.5     | How far between current and peak to pull effective_mu |
| `EffectiveMuPeakWindowDays` | 30      | Rolling window for peak mu tracking                   |

## Feature 2: Experience Score (Stomp Compensation)

### What it does

Track a rolling experience score per player based on their last N matches. Players on bad streaks (consecutive losses, repeated stomps) get placed on the predicted-stronger team. Players who've been dominating get placed on the predicted-weaker team. This distributes the stomp experience more fairly across all players rather than letting the same people get stomped repeatedly.

### How it works

After each match, compute a per-player match experience value:

| Outcome                 | Experience value |
| ----------------------- | ---------------- |
| Win, close (diff <= 2)  | +2               |
| Win, normal (diff 3-4)  | +1               |
| Win, stomp (diff >= 5)  | 0                |
| Loss, close (diff <= 2) | 0                |
| Loss, normal (diff 3-4) | -1               |
| Loss, stomp (diff >= 5) | -2               |

The experience score is the sum of the last 10 match experience values, clamped to [-20, +20].

- Score of -10 to -20: player has been getting destroyed. Place on stronger team.
- Score of -5 to +5: neutral. No adjustment.
- Score of +10 to +20: player has been dominating. Place on weaker team.

### Data source

Computed from MongoDB `nevr.match_summaries` at queue time. The query is the same as for effective_mu — last N matches for the user, reading `final_scores` and `participants.team`.

**Optimization:** Like peak_mu, persist the experience score in nakama storage and update it after each match. At queue time, just read the stored value. No MongoDB scan needed in the hot path.

### Where it integrates

- **Queue time**: Read `experience_score` from nakama storage, set as ticket numeric property.
- **Team assignment** (`evr_matchmaker_prediction.go`): After snake draft forms initial teams, apply experience-based swaps. If a player with experience_score <= -10 is on the predicted-weaker team, swap them with a neutral-experience player on the stronger team (if such a swap improves experience distribution without destroying team balance by more than a configurable mu threshold).

### Team assignment algorithm

After snake draft produces initial blue/orange rosters:

1. Compute predicted winner (team with higher avg effective_mu).
2. Identify players on the losing-predicted team with experience_score <= -10 ("suffering players").
3. For each suffering player, find a player on the winning-predicted team with experience_score >= 0 ("neutral player") and similar mu (within 5.0 mu).
4. If swapping them changes team balance by less than `MaxExperienceSwapMuDelta` (default 3.0), execute the swap.
5. Process at most `MaxExperienceSwaps` (default 2) swaps per candidate to avoid completely reorganizing teams.

### Configuration

| Setting                         | Default | Description                                     |
| ------------------------------- | ------- | ----------------------------------------------- |
| `ExperienceScoreWindowSize`     | 10      | Number of recent matches to consider            |
| `ExperienceScoreStompThreshold` | 5       | Score diff that counts as a stomp               |
| `MaxExperienceSwapMuDelta`      | 3.0     | Max team balance change from an experience swap |
| `MaxExperienceSwaps`            | 2       | Max swaps per candidate                         |
| `ExperienceBiasThreshold`       | -10     | Score at which team placement bias activates    |

## Feature 3: Dynamic Quality Floor

### What it does

Reject candidate matches whose predicted draw probability falls below a threshold. The threshold degrades per-player based on wait time, so players aren't held hostage waiting for a perfect match during low-population hours.

### How it works

For each candidate, compute a quality floor based on the oldest ticket in the candidate:

```
wait_secs = now - oldest_ticket_timestamp
base_floor = QualityFloorBase  (default 0.10)
decay_rate = QualityFloorDecayPerSec  (default 0.0005)
floor = max(0, base_floor - wait_secs * decay_rate)
```

With defaults: floor starts at 0.10 and reaches 0 after 200 seconds (3.3 minutes). A draw probability of 0.10 means the model gives the weaker team only ~40% win chance — matches worse than this are genuine stomps.

If a candidate's draw probability is below its computed floor, skip it during assembly. The players remain in the queue for the next cycle.

### Where it integrates

`assembleUniqueMatches` in `evr_matchmaker_process.go`, after sorting predictions. Add the quality floor check alongside the existing undersized-match and moderator-count checks:

```go
// In assembleUniqueMatches, inside the loop over sortedCandidates:
if r.DrawProb < computeQualityFloor(r.OldestTicketTimestamp, settings) {
    continue OuterLoop
}
```

### Interaction with accumulation

Accumulated candidates (pre-formed from starving tickets) bypass the quality floor. By the time a ticket enters accumulation (90s+), it has already waited long enough that the floor would be near zero anyway. Accumulation's own skill radius provides quality control for those matches.

### Configuration

| Setting                   | Default | Description                                 |
| ------------------------- | ------- | ------------------------------------------- |
| `QualityFloorBase`        | 0.10    | Starting draw probability threshold         |
| `QualityFloorDecayPerSec` | 0.0005  | How fast the floor drops per second of wait |
| `QualityFloorEnabled`     | true    | Feature flag                                |

## Feature 4: New Player Protection

### What it does

Players with fewer than a configurable number of completed matches get three protections:

1. **Team assignment bias** — Place new players on the predicted-stronger team (same mechanism as experience score, but unconditional for new players).
2. **Toxic player separation** — Avoid matching new players in candidates that contain players with suspension history.
3. **Moderator affinity** — Prefer candidates that pair new players with moderators.

### How it works

**Match count:** Persisted in nakama storage, incremented after each completed match. Read at queue time and set as ticket property `match_count`.

**Team assignment (1):** During the experience-based swap phase, new players (match_count < `NewPlayerThreshold`) are treated as if they have experience_score = -15 (always biased toward the stronger team), regardless of their actual experience score.

**Toxic separation (2):** During candidate scoring (the sort in `processPotentialMatches`), penalize candidates where a new player appears alongside a player with `suspension_count > 0`. Add a penalty term to the sort key:

```
toxic_penalty = count(new_players) * count(suspended_players) * ToxicSeparationWeight
```

This doesn't block the match — it pushes it down the priority list so the assembler picks cleaner candidates first. If no clean candidates exist (low population), the match still fires.

**Moderator affinity (3):** During candidate scoring, boost candidates where a new player appears alongside a moderator:

```
moderator_bonus = count(new_players) * count(moderators) * ModeratorAffinityWeight
```

### Data sources

- `match_count`: nakama storage (new field, persisted alongside rating data)
- `suspension_count`: nakama storage enforcement journals (`EnforcementJournalsLoad`), count of `GuildEnforcementRecord` entries where `IsSuspension() == true`. Read at queue time.
- `is_moderator`: already on tickets as a string property

### Configuration

| Setting                   | Default | Description                                        |
| ------------------------- | ------- | -------------------------------------------------- |
| `NewPlayerThreshold`      | 50      | Match count below which a player is "new"          |
| `ToxicSeparationWeight`   | 0.05    | Penalty weight for new+suspended in same candidate |
| `ModeratorAffinityWeight` | 0.02    | Bonus weight for new+moderator in same candidate   |

## Feature 5: Toxic Player Separation

### What it does

Players with suspension history get a negative affinity signal in candidate scoring. This applies to all players, not just new ones — suspended players are deprioritized in candidate selection for everyone.

### How it works

At queue time, set `suspension_count` as a ticket numeric property. In candidate scoring, apply a penalty:

```
candidate_toxic_score = sum(suspension_count for all players in candidate)
```

This becomes a tiebreaker in the prediction sort — candidates with lower toxic scores are preferred. It doesn't block suspended players from matching (they've served their suspension), but the system prefers to put them in candidates together rather than mixing them with clean players.

### Where it integrates

The `PredictedMatch` struct gains a `ToxicScore` field. The sort in `processPotentialMatches` adds toxic score as a priority level between division count and draw probability:

```
Current:  size > oldest_ticket > division_count > draw_prob
Proposed: size > oldest_ticket > division_count > toxic_score (lower better) > draw_prob
```

## Replay Harness

### Purpose

Validate that changes improve match quality before shipping to production. Feed real match data through the matchmaker pipeline and measure outcomes.

### How it works

The existing `evr_matchmaker_replay.go` provides state capture and loading. The replay harness extends this:

1. **Convert match_dataset.json to matchmaker entries** — Each match in the dataset becomes a set of mock `MatchmakerEntry` objects with the same `rating_mu`, `rating_sigma`, `timestamp`, and team properties as the real players. The new enrichment properties (`effective_mu`, `experience_score`, `match_count`, `suspension_count`) are computed from the MongoDB data and attached.

2. **Run entries through the pipeline** — Call `processPotentialMatches` with the mock entries. Capture which candidates were formed, which matches were assembled, and the predicted draw probabilities.

3. **Compare predicted outcome to actual outcome** — The dataset has actual final scores. Compute:
   - Did the predicted winner actually win?
   - What was the predicted draw prob vs actual score diff?
   - Were any matches rejected by the quality floor that would have been stomps?
   - How did experience-based swaps change team composition?

4. **Aggregate metrics** — Stomp rate, prediction accuracy, quality floor rejection rate, session duration impact (estimated from consecutive match patterns in the data).

### Implementation

A Go test file `evr_matchmaker_replay_test.go` that:

- Loads `match_dataset.json`
- Constructs mock entries using `addIntegrationProcessorTicketWithProps` pattern from integration tests
- Runs single-cycle and multi-cycle scenarios
- Reports quality metrics

This is a test, not production code. It runs with `go test` and produces a report.

## Success Metrics

| Metric                          | Current baseline | Target | Source                                       |
| ------------------------------- | ---------------- | ------ | -------------------------------------------- |
| Stomp rate (diff >= 5)          | 54%              | < 40%  | Match scores                                 |
| Blowout rate (diff >= 8)        | 35%              | < 20%  | Match scores                                 |
| Best-player-team-wins rate      | 75%              | < 65%  | Team assignment + scores                     |
| Median session duration         | (measure)        | +15%   | Match timestamps (gap > 30min = new session) |
| Matches per session             | (measure)        | +10%   | Match count per session                      |
| New player 7-day return rate    | (measure)        | +20%   | First match per day                          |
| Max consecutive stomps received | (measure)        | < 3    | Per-player match history                     |

Session duration is the north star. Measure baseline before deploying any changes.

### How to measure session duration

A "session" is a sequence of matches by a player where no gap between consecutive match end_time and next match start_time exceeds 30 minutes. Compute from MongoDB:

```javascript
// Per player: get all match end_times, sort, split into sessions at 30min gaps
db.match_summaries.aggregate([
  { $match: { mode: "echo_arena", match_over: true } },
  { $unwind: "$participants" },
  { $match: { "participants.was_present_at_end": true } },
  {
    $group: {
      _id: "$participants.user_id",
      match_times: { $push: { start: "$start_time", end: "$end_time" } },
    },
  },
]);
```

Then split `match_times` into sessions in application code (30-minute gap threshold).

## Data Flow Summary

### At queue time (per player)

1. Read from nakama storage: `rating_mu`, `rating_sigma`, `peak_mu`, `peak_mu_updated_at`, `experience_score`, `match_count`
2. Read from nakama storage: enforcement journal → `suspension_count`
3. Compute `effective_mu = current_mu + 0.5 * max(0, peak_mu - current_mu)`
4. Set ticket properties: `effective_mu`, `experience_score`, `match_count`, `suspension_count`

### After each match (per player)

1. Update `peak_mu`: if current_mu > peak_mu, set peak_mu = current_mu and peak_mu_updated_at = now. If peak_mu_updated_at > 30 days ago, reset peak_mu = current_mu.
2. Compute match experience value from final scores and team assignment.
3. Update `experience_score`: add new value, drop oldest if window > 10.
4. Increment `match_count`.

### In matchmaker pipeline (per cycle)

1. **Accumulation**: uses `effective_mu` for CenterMu and skill radius.
2. **Quality floor**: rejects candidates below per-ticket draw prob threshold in assembleUniqueMatches.
3. **Team formation**: snake draft uses `effective_mu` for ranking. Experience-based swaps applied after draft.
4. **Candidate scoring**: toxic_score and moderator affinity applied as sort tiebreakers.

## Files Modified

| File                                      | Change                                                                                                                   |
| ----------------------------------------- | ------------------------------------------------------------------------------------------------------------------------ |
| `evr_lobby_matchmake.go`                  | Ticket enrichment: read nakama storage, compute effective_mu, set new properties                                         |
| `evr_matchmaker_process.go`               | Quality floor in assembleUniqueMatches; toxic_score in prediction sort                                                   |
| `evr_matchmaker_prediction.go`            | PredictedMatch struct: add ToxicScore field; use effective_mu for ranking; experience-based team swaps after snake draft |
| `evr_matchmaker_accumulation.go`          | Use effective_mu for CenterMu and skill radius matching                                                                  |
| `evr_match.go` or `evr_runtime_events.go` | Post-match: update peak_mu, experience_score, match_count                                                                |
| `evr_global_settings.go`                  | New configuration fields for all features                                                                                |

## New Files

| File                                | Purpose                                                                       |
| ----------------------------------- | ----------------------------------------------------------------------------- |
| `evr_matchmaker_experience.go`      | Experience score computation, effective_mu calculation, team swap logic       |
| `evr_matchmaker_experience_test.go` | Unit tests for experience scoring and team swaps                              |
| `evr_matchmaker_replay_test.go`     | Replay harness: load match_dataset.json, run through pipeline, report metrics |

## Rollout

All features are independently flag-gated in `GlobalMatchmakingSettings`:

1. **Replay harness** — Build first. Establish baseline metrics.
2. **Quality floor** — Ship second. Lowest risk, highest immediate impact on worst matches.
3. **Effective mu** — Ship third. Prevents death spiral, works passively.
4. **Experience score + team swaps** — Ship fourth. Most complex, needs replay validation.
5. **New player protection + toxic separation** — Ship last. Requires match_count and suspension_count enrichment to be live.

Each feature ships behind a flag, runs for one week, and metrics are compared to baseline before enabling the next.

## Open Questions

None. All decisions made during design discussion.
