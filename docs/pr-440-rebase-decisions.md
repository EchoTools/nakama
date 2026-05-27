# PR #440 Rebase Decisions

Rebased `pr-440-rebase-clean` onto `main` on 2026-05-27.
44 commits on main since the fork point (683ae97a7).
1 PR commit rebased. 4 files conflicted, 6 conflict regions total.

## Conflict 1: server/evr_lobby_find.go (Region 1 of 3) — lines ~117-136

**What conflicted:** The `shouldFollowerFindOrCreateSocial` branch condition.

- **Main** added `|| headingToSocial` to the condition and introduced a
  two-branch logger path: one for when the leader is heading to social (forcing
  the follower), and one for when the follower itself is in social mode. This
  was part of a TOCTOU fix that caches `headingToSocial` early and reuses it
  here to avoid a double-read race.
- **PR #440** kept the simpler `shouldFollowerFindOrCreateSocial(lobbyParams.Mode)`
  check with a single log line.

**Decision: KEEP MAIN.** Main's `headingToSocial` cache is genuinely new
functionality added across multiple commits to fix a TOCTOU race condition.
The early caching of `headingToSocial` (around line 68) was auto-merged from
main, so the branch condition here must reference it. The PR's simpler version
would leave the cached variable unused and miss the leader-heading-to-social
path.

## Conflict 2: server/evr_lobby_find.go (Region 2 of 3) — lines ~144-150

**What conflicted:** A comment block above the polling fallback for non-leader,
non-social followers.

- **Main** added a clarifying comment: "Still a non-leader in a non-social mode.
  Poll for the leader to settle into a match..."
- **PR #440** had no comment here.

**Decision: KEEP MAIN.** The comment is documentation-only and explains the
polling path. No behavioral change. Improves readability.

## Conflict 3: server/evr_lobby_find.go (Region 3 of 3) — lines ~172-185

**What conflicted:** The released-follower logic when a follower cannot join the
leader's match.

- **Main** added a guard: for social modes, do not set party size to 1 (so party
  members converge to the leader's lobby), and nil out `lobbyGroup` for
  non-social released followers (so `addTicket` does not enforce leader-only
  party submission).
- **PR #440** unconditionally set `lobbyParams.SetPartySize(1)`.

**Decision: KEEP MAIN.** Main's conditional logic (added in commit f6e6e127c
"fix(party): released followers must queue as solo") is a targeted fix for the
party split symptom where released followers in social mode lost convergence.
The PR's unconditional `SetPartySize(1)` would regress this fix. Main's
approach also nils `lobbyGroup` for non-social modes, which is a necessary
guard against the leader-only submission enforcement.

## Conflict 4: server/evr_lobby_parameters.go — lines ~887-909

**What conflicted:** `MatchmakingStream()` and `GuildGroupStream()` methods.

- **Main** changed the receiver from value (`LobbySessionParameters`) to pointer
  (`*LobbySessionParameters`) to match the rest of the file (10 pointer
  receivers vs 1 value receiver). No logic change.
- **PR #440** kept value receivers but added groupID normalization: for
  `ModeArenaPublic`, `ModeCombatPublic`, and `ModeSocialPublic`, set
  `groupID = uuid.Nil` before constructing the stream.

**Decision: MERGE BOTH.** Keep main's pointer receivers (consistency with the
rest of the file) AND incorporate PR #440's groupID normalization logic (the
structural redesign). The normalization ensures public mode streams use
`uuid.Nil` as the subject, which is the correct cross-guild matchmaking
behavior for public modes.

## Conflict 5: server/evr_matchmaker_process.go — lines ~410-413

**What conflicted:** A `foundTimestamp` sentinel variable assignment.

- **Main** added `foundTimestamp = true` inside the timestamp-scanning loop, plus
  a subsequent guard clause: if no valid timestamp was found, treat the entry as
  expired so players are not stuck waiting forever.
- **PR #440** did not have this sentinel.

**Decision: KEEP MAIN.** This is genuinely new defensive logic. Without it,
if no entries have valid timestamps, `oldestTimestamp` stays at `math.MaxFloat64`
and the function returns `true` (not expired), which can leave players stuck in
matchmaking indefinitely. The guard clause was already auto-merged; only the
`foundTimestamp = true` assignment was conflicted.

## Conflict 6: server/pipeline_party.go (2 regions) — lines ~57-62 and ~142-147

**What conflicted:** `tracker.Track()` return value handling in party create and
party join paths.

- **Main** kept `success, isNew := p.tracker.Track(...)` and uses `isNew` in a
  subsequent `if !isNew` warning log.
- **PR #440** changed to `success, _ := p.tracker.Track(...)` (discarding
  `isNew`) and added SMELL comments noting the synchronous assumption.

**Decision: MERGE BOTH.** Keep main's `isNew` variable (required by the
subsequent `if !isNew` check that was auto-merged) AND incorporate PR #440's
SMELL comments (they document a real architectural concern about `tracker.Track`
synchronicity). Discarding `isNew` would cause a compile error since the
variable is referenced on the next line.

## Build and Test Results

- **Compilation:** PASS (`go build ./...` succeeds with no errors)
- **Party unit tests:** PASS (all 15+ party-related test functions pass)
- **Full test suite:** Cannot run — requires `DISCORD_BOT_TOKEN` environment
  variable for runtime initialization (pre-existing requirement, unrelated to
  this rebase)
