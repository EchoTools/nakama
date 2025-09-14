# Competitive Match Telemetry and KPI Specification

Purpose
- Define the KPIs used to monitor and reduce disruptive mid‑match departures.
- Specify the exact telemetry events, fields, and metric values required to compute each KPI.
- Standardize units, naming, and dimensions to enable consistent aggregation.

Status
- Version: 1.0
- Owner: Game Services / Matchmaking
- Scope: Public competitive modes with backfill

---

## 0) Conventions

- Time units
  - game_time_s: seconds since kickoff/loading complete (server-authoritative).
  - wall_time: RFC3339 timestamp, UTC.

- Metric types
  - Counter: monotonically increasing integer (e.g., events, matches).
  - Gauge: point-in-time value (e.g., team size at t).
  - Histogram: distribution of durations/latencies (e.g., backfill time).

- Dimensions (tags) — attach to all events when applicable
  - match_id, player_id_hash, team_id, mode, map, region, datacenter, build, platform, device_model
  - mmr_bucket (e.g., D1–D10 deciles), party_size, is_ranked, team_size_max
  - ping_band (e.g., <40, 40–80, 80–120, >120), is_backfill (on player connect), is_server_fault

- Grace windows and thresholds (configurable; include values in telemetry config event if needed)
  - leave_grace_s: 120
  - team_wipe_window_s: 30
  - goal_hazard_window_s: 45
  - early_leave_time_s: 180 (or “before N goals,” see below)
  - reconnect_valid_s: 300
  - disadvantage_threshold: 1 (players down)

---

## 1) Event Schema

Emit the following events server-authoritatively. All payload fields listed are required unless noted. Metrics to emit are listed under “Metrics.”

### 1.1 match_started
Describes a match at kickoff.

Fields
- match_id, mode, map, region
- team_size_max (int), mmr_bucket (coarse decile), started_at (wall time)
- party_distribution: optional summary (e.g., per-team party sizes)

Metrics
- Counter: matches_started{mode,map,region,mmr_bucket,team_size_max} += 1

---

### 1.2 player_connected
Player joins or spawns into the match.

Fields
- match_id, player_id_hash, team_id, is_backfill (bool), joined_game_time_s (int), joined_at (wall)
- party_size, platform, device_model, ping_ms

Metrics
- Counter: player_match_joins_total{is_backfill}
- Gauge sample: team_size{team_id}=current_headcount (server emits or derives via state tick)

---

### 1.3 score_event
A goal/point is scored.

Fields
- match_id, scoring_team_id, new_score_home, new_score_away, game_time_s

Metrics
- Marker for hazard alignment: goal_event_markers_total += 1

---

### 1.4 player_disconnected
Player leaves or is disconnected.

Fields
- match_id, player_id_hash, team_id, game_time_s, reason
  - reason enum: client_exit, crash, net_drop, afk_kick, votekick, kick_other
- explicit_quit (bool): true if client sent explicit leave
- server_fault (bool): true if within a known incident window
- seq_id (int): to pair with reconnected

Metrics
- Counter: disconnects_total{reason,explicit_quit}
- For abandon logic, do not increment “abandon” yet; wait for grace window or match end.

---

### 1.5 player_reconnected
Player re-enters same match after a disconnect.

Fields
- match_id, player_id_hash, team_id, game_time_s, reconnect_after_s (int), paired_seq_id (int)

Metrics
- Counter: reconnects_total
- Histogram: reconnect_after_seconds_bucket

---

### 1.6 match_ended
Match finished.

Fields
- match_id, duration_s, winner_team_id, final_score_home, final_score_away, ended_at
- integrity_minutes (optional precomputed), disadvantage_minutes_by_team (optional)
- incident_flags: e.g., was_server_fault (bool)

Metrics
- Counter: matches_ended_total
- Histogram: match_duration_seconds_bucket

Post-processing
- Mark disconnects that did NOT reconnect within leave_grace_s as “abandon events.”
- Mark early/mid/late categories based on thresholds.

---

### 1.7 backfill_requested
A vacancy is created and backfill matchmaking starts.

Fields
- match_id, team_id_needing_backfill, vacancy_size (int), game_time_s, score_home, score_away

Metrics
- Counter: backfill_requests_total{vacancy_size}
- Gauge sample: team_size_delta = team_size_max − current_team_size

---

### 1.8 backfill_filled
A backfill player (or players) accepted and spawned.

Fields
- match_id, team_id, filled_count (int), requested_at_game_time_s, filled_at_game_time_s

Metrics
- Counter: backfill_fills_total{filled_count}
- Histogram: backfill_fill_seconds_bucket (filled_at − requested_at)

---

### 1.9 queue_event
Queue timeline for players.

Fields
- player_id_hash, queued_at (wall), dequeued_at (wall), is_low_priority (bool), queue_type

Metrics
- Histogram: queue_wait_seconds_bucket
- Counter: low_priority_queue_entries_total{is_low_priority}

---

### 1.10 penalty_applied
A penalty or enforcement tied to behavior.

Fields
- player_id_hash, penalty_type, magnitude, start_at, end_at, reason_code

Metrics
- Counter: penalties_applied_total{penalty_type,reason_code}

---

### 1.11 server_fault
Operational incident affecting matches.

Fields
- match_id (optional, if scoped), fault_code, started_at, ended_at

Metrics
- Counter: server_faults_total{fault_code}

---

### 1.12 periodic_team_state (optional tick)
Lightweight periodic emission of team sizes.

Fields
- match_id, game_time_s, teamA_size, teamB_size

Metrics
- Gauge sample: team_size{team_id}
- Derived: integrity/disadvantage minutes

---

## 2) KPI Definitions and Required Telemetry

For each KPI: what it measures, formula, event and metric requirements, and notes.

### 2.1 Match Abandon Rate (MAR)
Measures
- Fraction of matches with ≥1 abandon event (player left and did not return within leave_grace_s before match end), excluding server faults.

Formula
- MAR = matches_with_one_or_more_abandons / matches_started

Events and metrics
- match_started → matches_started Counter
- player_disconnected / player_reconnected → determine abandon events with grace pairing
- match_ended → close out and attribute abandons to the match
- server_fault → exclude faulted matches (flag on match_ended)

Dimensions
- mode, map, region, mmr_bucket, is_ranked, team_size_max, time_of_day

Notes
- Abandon determination occurs at or after match_ended using disconnects without qualifying reconnection.

---

### 2.2 Player-Abandon Rate (PAR) per player-match
Measures
- Frequency of abandons normalized by player-match participations.

Formula
- PAR = total_abandon_events / total_player_matches
- total_player_matches = player_match_joins_total counting first spawn per match

Events and metrics
- player_connected → player_match_joins_total Counter (first join per match per player)
- player_disconnected / player_reconnected → abandon events Counter (computed)
- match_ended → finalize

Dimensions
- player_cohorts (account_age_band, mmr_bucket), mode, region, platform

---

### 2.3 Early Leave Rate (ELR)
Measures
- Propensity of early abandons (e.g., before early_leave_time_s or before N opponent goals).

Formula
- ELR = matches_with_early_abandon / matches_started
- Early abandon: abandon_event.game_time_s < early_leave_time_s OR before opponent goals_threshold

Events and metrics
- player_disconnected/reconnected → abandon with timestamp
- score_event → to support “before N goals” rule
- match_started / match_ended

Dimensions
- mode, mmr_bucket, party_size

---

### 2.4 Team-Wipe Leave Rate (TWLR)
Measures
- Share of matches where ≥K players from one team abandon within team_wipe_window_s.

Formula
- TWLR = matches_with_team_wipe_abandon / matches_started

Events and metrics
- player_disconnected/reconnected → abandon events with timestamps per team
- match_started / match_ended

Dimensions
- K (e.g., 3 for 4v4), window_s, mode, mmr_bucket

---

### 2.5 Post-Goal Team-Wipe Rate (PG-TWR)
Measures
- Team wipes that occur within goal_hazard_window_s after conceding a goal.

Formula
- PG-TWR = matches_with_team_wipe_within_goal_window / matches_started

Events and metrics
- score_event → goal timestamps and which team conceded
- player_disconnected/reconnected → abandon events with timestamps and teams
- match_started / match_ended

---

### 2.6 Leaver Hazard h(t)
Measures
- Instantaneous risk of a leave at time t (bin-based), overall and aligned to goals.

Formula
- h_bin = leaves_in_bin / exposed_players_in_bin
- Goal-proximate lift = h_goal_window / h_non_goal_window

Events and metrics
- player_disconnected → leave timestamp binning
- periodic_team_state or derived exposure from active players per bin
- score_event → to mark goal windows

Dimensions
- time_bin_s (e.g., 10s bins), mode, mmr_bucket

Notes
- Exposed players include all active players not yet left/ended in that bin.

---

### 2.7 Integrity Minutes % (IM%)
Measures
- Proportion of match time where both teams at full strength.

Formula
- IM% = integrity_minutes / match_duration_minutes
- integrity_minutes = sum over ticks: 1{teamA_size==teamB_size==team_size_max} × dt

Events and metrics
- periodic_team_state (preferred) or reconstruct from connect/disconnect streams
- match_ended → duration_s

Dimensions
- mode, map, mmr_bucket

---

### 2.8 Disadvantage Minutes (DM, DM2+)
Measures
- Time a team plays short-handed by ≥1 (DM) or ≥2 (DM2+) players.

Formula
- DM = sum over ticks: 1{abs(teamA_size − teamB_size) ≥ 1} × dt
- DM2+ similarly with ≥2

Events and metrics
- periodic_team_state or connect/disconnect derivation
- match_ended

Dimensions
- disadvantaged_team_id, mode

---

### 2.9 Backfill Fill Time (BFT)
Measures
- Time from vacancy to backfill player spawning into the match.

Formula
- BFT = filled_at_game_time_s − requested_at_game_time_s

Events and metrics
- backfill_requested → request timestamp
- backfill_filled → fill timestamp
- Histogram: backfill_fill_seconds_bucket
- Counter: backfill_requests_total, backfill_fills_total

Dimensions
- region, mmr_bucket, time_of_day, vacancy_size, score_diff_band, game_time_band

---

### 2.10 Backfill Outcome Win Rate (BOWR)
Measures
- Probability that the team requiring backfill eventually wins, conditional on score/time at backfill.

Formula
- BOWR = wins_after_backfill / backfill_events
- Conditional stratification by score_diff and time_remaining

Events and metrics
- backfill_filled → anchor event with score_home/away and game_time_s (include if not in request)
- match_ended → winner
- score_event → if score at backfill needs reconstruction

Dimensions
- bins: score_diff_band (e.g., −3 or worse, −2, −1, 0, +1+), time_remaining_band

---

### 2.11 Reconnection Rate (RR)
Measures
- Fraction of disconnects that successfully reconnect within reconnect_valid_s.

Formula
- RR = reconnects_within_R / disconnects_total (excluding server_fault)

Events and metrics
- player_disconnected → baseline count
- player_reconnected → paired by seq_id within reconnect_valid_s

Dimensions
- reason, platform, ping_band

---

### 2.12 AFK Rate
Measures
- Frequency of AFK kicks relative to player-matches.

Formula
- AFK Rate = afk_kicks_total / player_matches

Events and metrics
- player_disconnected{reason=afk_kick} → count
- player_connected (first per match) → player_matches

Dimensions
- mode, map, mmr_bucket, party_size

---

### 2.13 Cascade Index
Measures
- Average number of additional abandons within 60s after the first abandon in a match.

Formula
- Cascade Index = (abandons_within_60s_after_first) / (matches_with ≥1 abandon)

Events and metrics
- player_disconnected/reconnected → abandon timestamps
- match_ended → finalize

Dimensions
- mode, mmr_bucket

---

### 2.14 Queue Guardrails
Measures
- Player queue wait and low-priority impact.

Formula
- p50/p95 queue_wait_seconds; low_priority_queue_entries_total / queue_entries_total

Events and metrics
- queue_event → queued_at, dequeued_at, is_low_priority
- Histogram: queue_wait_seconds_bucket

Dimensions
- region, mmr_bucket, is_low_priority

---

### 2.15 Retention Guardrails (high level)
Measures
- D1/D7 return of players exposed to penalties vs not.

Formula
- Standard cohort retention curves; not computed from match events directly.

Events and metrics
- penalty_applied
- account_login (not defined here; assumed available)

Dimensions
- penalty_tier, exposure_window

---

## 3) Metric Name Registry (Prometheus-style examples)

Counters
- matches_started
- matches_ended_total
- player_match_joins_total{is_backfill}
- disconnects_total{reason,explicit_quit}
- reconnects_total
- abandon_events_total{early,mid,late,post_goal,team_wipe_member}
- backfill_requests_total{vacancy_size}
- backfill_fills_total{filled_count}
- goals_total{scoring_team_id}
- penalties_applied_total{penalty_type,reason_code}
- server_faults_total{fault_code}
- low_priority_queue_entries_total

Histograms
- match_duration_seconds_bucket
- backfill_fill_seconds_bucket
- reconnect_after_seconds_bucket
- queue_wait_seconds_bucket

Gauges (sampled)
- team_size{team_id}
- team_size_delta
- active_players_exposed (for hazard denominator; can be derived)

Notes
- abandon_events_total is not emitted directly by the server during play; compute in a stream/batch job when leave_grace_s expires or at match end.

---

## 4) Field Requirements by KPI (Checklist)

- MAR: match_started, player_disconnected, player_reconnected, match_ended, server_fault
- PAR: player_connected (first per match), player_disconnected, player_reconnected, match_ended
- ELR: player_disconnected (time), player_reconnected, score_event (optional for “before N goals”), match_started/ended
- TWLR: player_disconnected/reconnected with timestamps and team, match_started/ended
- PG-TWR: score_event, player_disconnected/reconnected with timestamps and teams, match_started/ended
- Hazard h(t): player_disconnected timestamps; active players per time bin (periodic_team_state or derived); score_event for goal-aligned windows
- IM%: periodic_team_state (or reconstruct from connect/disconnect), match_ended.duration_s
- DM/DM2+: periodic_team_state (or reconstruct), match_ended
- BFT: backfill_requested, backfill_filled
- BOWR: backfill_filled (with score/time context), match_ended (winner), score_event if extra context needed
- RR: player_disconnected (seq), player_reconnected (paired), server_fault
- AFK Rate: player_disconnected{reason=afk_kick}, player_connected (first per match)
- Cascade Index: player_disconnected/reconnected abandon timestamps, match_ended
- Queue Guardrails: queue_event
- Retention Guardrails: penalty_applied, account_login (external)

---

## 5) Edge Cases and Exclusions

- Server/platform incidents
  - Exclude matches overlapping server_fault windows from MAR, PAR, RR numerators and denominators.
- Reconnects
  - A disconnect within leave_grace_s that later reconnects within reconnect_valid_s is not an abandon.
- Pre-match dodges
  - Exclude any exits before match_started (e.g., in lobby).
- Client crashes
  - If verified crash signature and reconnection within leave_grace_s, classify as no-fault disconnect; exclude from abandon.

---

## 6) Minimal Implementation Plan

Phase A (Week 1–2)
- Implement: match_started, player_connected, player_disconnected, player_reconnected, match_ended, score_event.
- Compute: MAR, PAR, ELR, TWLR, PG-TWR.

Phase B (Week 3–4)
- Add: periodic_team_state or reliable reconstruction to derive IM% and DM.
- Add: backfill_requested, backfill_filled → BFT and BOWR.

Phase C (Week 5+)
- Add: queue_event, penalty_applied, server_fault, AFK reason fidelity.
- Add: hazard curves and cascade index visualizations.

---

## 7) Example Event Payloads (JSON)

match_started
```json
{
  "match_id": "m_123",
  "mode": "competitive_4v4",
  "map": "arena_a",
  "region": "us-east",
  "datacenter": "iad-1",
  "build": "1.12.0",
  "is_ranked": true,
  "team_size_max": 4,
  "team_count": 2,
  "mmr_bucket": "D6",
  "started_at": "2025-09-14T04:05:00Z"
}
```

player_disconnected
```json
{
  "match_id": "m_123",
  "player_id_hash": "p_ab12",
  "team_id": "A",
  "game_time_s": 215,
  "reason": "client_exit",
  "explicit_quit": true,
  "server_fault": false,
  "seq_id": 7
}
```

player_reconnected
```json
{
  "match_id": "m_123",
  "player_id_hash": "p_ab12",
  "team_id": "A",
  "game_time_s": 332,
  "reconnect_after_s": 117,
  "paired_seq_id": 7
}
```

score_event
```json
{
  "match_id": "m_123",
  "scoring_team_id": "B",
  "new_score_home": 0,
  "new_score_away": 1,
  "game_time_s": 190
}
```

backfill_requested
```json
{
  "match_id": "m_123",
  "team_id_needing_backfill": "A",
  "vacancy_size": 1,
  "game_time_s": 220,
  "score_home": 0,
  "score_away": 1
}
```

backfill_filled
```json
{
  "match_id": "m_123",
  "team_id": "A",
  "filled_count": 1,
  "requested_at_game_time_s": 220,
  "filled_at_game_time_s": 400
}
```

match_ended
```json
{
  "match_id": "m_123",
  "duration_s": 600,
  "winner_team_id": "B",
  "final_score_home": 1,
  "final_score_away": 3,
  "ended_at": "2025-09-14T04:15:00Z",
  "incident_flags": { "was_server_fault": false }
}
```

---

## 8) Data Quality Requirements

- Event ordering: allow out-of-order but require eventual consistency within 5 minutes.
- Uniqueness: enforce idempotency keys per event instance.
- Clock: server authoritative game_time_s; do not trust client clocks.
- Cardinality: hash player_id; avoid raw IDs in metrics labels.

---

## 9) Deliverables for Engineering

- Add events as above to server pipeline.
- Emit metrics names and units exactly as specified.
- Provide a daily batch job to:
  - Resolve abandons by pairing disconnects/reconnects with grace window.
  - Compute team-wipe windows and cascade counts.
  - Materialize per-match aggregates for IM%, DM, BFT, BOWR.
