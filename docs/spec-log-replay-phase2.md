# Log-Replay Testing Phase 2: Pipeline Outcome Verification

## 1. What Phase 2 Tests Verify That Phase 1 Does Not

Phase 1 replays lifecycle state transitions through `PlayerMatchLifecycle` in
isolation. It proves the state machine accepts the same transition sequence as
production. But it cannot catch the class of bugs where the pipeline silently
drops a player: `lobbyFind` returns nil (no error), no `LobbySessionSuccess` is
sent, and the player is stuck in limbo with a clean state machine history.

Phase 2 feeds actual messages through the pipeline handlers and verifies
**outcomes**:

| Check                                          | Phase 1 | Phase 2 |
| ---------------------------------------------- | ------- | ------- |
| Lifecycle transition sequence matches          | Yes     | Yes     |
| `LobbySessionSuccess` sent to client           | No      | Yes     |
| Player placed in a match (JoinAttempt called)  | No      | Yes     |
| `lobbyFind` returned nil vs error vs success   | No      | Yes     |
| Follower follow-path entered an infinite loop  | No      | Yes     |
| Player stuck with no response for >30 seconds  | No      | Yes     |
| Reservation created for party follower         | No      | Yes     |
| Follower redirected to social when appropriate | No      | Yes     |

The bigduckii bug is the canonical example: Phase 1 shows the illegal
`Matchmaking -> SocialReady` transitions (the symptom), but does not test
whether the player _ever received a `LobbySessionSuccess`_. Phase 2 replays
the `LobbyFindSessionRequest` through `lobbyFind` and asserts the player
ends up in a match.

---

## 2. Test Architecture: Option Evaluation

### Option A: Mock at the boundary of `lobbyFind`

Feed `lobbyFind` a `*sessionWS`, `*LobbySessionParameters`, and verify the
return value and side effects (messages sent, matches joined).

**Dependencies that must be mocked or stubbed:**

| Dependency                         | Current access path                                      | Existing mock?                                         |
| ---------------------------------- | -------------------------------------------------------- | ------------------------------------------------------ |
| `session.pipeline.tracker`         | `p.nk.tracker` via `session.pipeline.tracker`            | Yes: `mockMatchmakingTracker` (full Track/Untrack/Get) |
| `p.nk.matchRegistry`               | `p.nk.matchRegistry.JoinAttempt`, `GetMatch`             | Yes: `mockFollowMatchRegistry` (GetMatch only)         |
| `p.nk.sessionRegistry`             | `p.nk.sessionRegistry.Get(sid)` for session lookups      | Partial: `testSessionRegistry` (exists, returns nil)   |
| `p.nk.tracker`                     | direct tracker reads (`GetLocalBySessionIDStreamUserID`) | Same as above                                          |
| `session.SendEvr` / `session.Send` | sends `LobbySessionSuccess` to client                    | No: needs capture mock on `sessionWS`                  |
| `p.nk.metrics`                     | `CustomCounter`, `CustomTimer`                           | No: panics on nil; needs no-op stub                    |
| `p.lobbyAuthorize`                 | guild authorization                                      | Needs stub (return nil)                                |
| `p.LobbyJoinEntrants`              | match join + session success send                        | Needs mock (record join attempts)                      |
| `p.guildGroupRegistry`             | guild lookups                                            | No: needs stub                                         |
| `p.appBot`                         | audit logging                                            | No: needs stub                                         |
| `MatchLabelByID`                   | label lookup via `p.nk`                                  | Via `mockFollowMatchRegistry.GetMatch`                 |
| `PrepareEntrantPresences`          | session registry lookups                                 | Needs mock session registry that returns test sessions |
| `globalAppBot`, `globalMatchmaker` | global singletons                                        | Must be set during test init                           |

**Pros:** Tests the exact function that dropped bigduckii. Moderate mock
surface. Existing `followTestEnv` provides 80% of the scaffolding.

**Cons:** `lobbyFind` is 400+ lines with many branches. Mocking everything
it touches is substantial work. Some branches (matchmaker ticket submission,
social lobby find-or-create) call deeply into subsystems that are hard to
mock at this level.

### Option B: Mock at the boundary of `lobbySessionRequest`

Feed `lobbySessionRequest` an `evr.LobbyFindSessionRequest` message and
verify the full pipeline including `NewLobbyParametersFromRequest` ->
`handleLobbySessionRequest` -> `lobbyFind`.

**Additional dependencies beyond Option A:**

| Dependency                      | Why                                             |
| ------------------------------- | ----------------------------------------------- |
| `NewLobbyParametersFromRequest` | Reads user profile, guild membership, ping data |
| `p.db` (SQL database)           | Profile lookups, group membership checks        |
| `LoadParams` / context values   | Login session, lobby params from auth pipeline  |

**Pros:** Tests parameter construction (a real bug surface -- wrong mode
detection, wrong group ID). Tests error handling and `LobbySessionFailure`
responses.

**Cons:** Requires a database mock or an in-memory SQLite. The parameter
construction calls `CheckSystemGroupMembership`, reads storage objects,
queries guilds. Mock surface roughly doubles. Not worth the coverage gain
for the specific bugs we are targeting.

### Option C: Full pipeline replay via `processRequest`

Create a real `EvrPipeline` with mock backends and feed raw binary messages
through `ProcessRequestEVR`.

**Additional dependencies beyond Option B:**

| Dependency                          | Why                                         |
| ----------------------------------- | ------------------------------------------- |
| `EvrPipeline` constructor           | Requires ~20 parameters including runtime   |
| `Runtime` / `RuntimeGoNakamaModule` | The full Nakama runtime                     |
| Login/auth pipeline                 | `ProcessRequestEVR` checks auth state first |
| WebSocket framing                   | Message deserialization from binary         |

**Pros:** Tests the complete path from network to outcome. Catches bugs in
the dispatch table and auth checks.

**Cons:** Mock complexity is extreme. The `NewEvrPipeline` constructor
initializes Discord bots, guild registries, XP tables, global settings.
Standing this up for tests is a multi-week project. The Phase 1 spec
estimated this as Phase 5; it remains premature.

### Recommendation: Option A with selective boundary adjustments

Option A is the clear winner. It tests the function that contains the bug
(`lobbyFind`) with manageable mock complexity. The existing
`followTestEnv` pattern provides the template: create a minimal
`EvrPipeline` struct with mock tracker and registry, wire up the session,
call the function under test, assert side effects.

The boundary adjustment: instead of testing `lobbyFind` as a pure function
call, wrap it in a thin test harness (`replayTestEnv`) that:

1. Constructs `LobbySessionParameters` from the fixture (bypassing
   `NewLobbyParametersFromRequest` which needs the DB).
2. Creates `sessionWS` instances with message capture buffers (instead of
   real WebSocket connections).
3. Provides a `mockMatchRegistry` that supports `JoinAttempt` (extending
   the existing `mockFollowMatchRegistry`).
4. Provides a `mockSessionRegistry` that returns the test sessions.
5. Provides no-op stubs for metrics, appBot, guildGroupRegistry.
6. Calls `lobbyFind` and captures:
   - Return value (nil = success, error = failure)
   - Messages sent to the session (via captured `SendEvr` calls)
   - Match join attempts (via `mockMatchRegistry.JoinAttempt` recording)
   - Lifecycle transitions (via `getMatchLifecycle(session).History()`)

---

## 3. Test Harness Design: `replayTestEnv`

### 3.1 Structure

```go
type replayTestEnv struct {
    t *testing.T

    // Pipeline under test
    pipeline *EvrPipeline

    // Sessions keyed by fixture player identifier (e.g., "player_A")
    sessions map[string]*mockReplaySession

    // Mock components
    tracker       *mockMatchmakingTracker       // existing
    matchRegistry *mockReplayMatchRegistry       // extends mockFollowMatchRegistry
    sessionReg    *mockReplaySessionRegistry     // returns test sessions by SID
    metrics       *noopMetrics                   // no-op counter/timer

    // Recorded outcomes
    joinAttempts  []recordedJoinAttempt
    sentMessages  map[string][]evr.Message  // playerID -> messages sent
}
```

### 3.2 Mock Session Requirements

The existing `sessionWS` struct is used directly in `lobbyFind` (not via an
interface). This means Phase 2 tests must construct real `sessionWS`
instances, but with a mock `conn` that captures outbound messages instead
of writing to a WebSocket.

Approach: inject a `sendOverride` hook on `sessionWS` that captures calls
to `SendEvr`. This requires a small, backward-compatible change to
`sessionWS`:

```go
// In session_ws.go, add to sessionWS struct:
sendEvrHook func(messages ...evr.Message) error // nil in production; test-only

// In SendEvr, check the hook:
func (s *sessionWS) SendEvr(messages ...evr.Message) error {
    if s.sendEvrHook != nil {
        return s.sendEvrHook(messages...)
    }
    // existing implementation...
}
```

This is the minimal refactoring needed. The hook is nil in production code.

### 3.3 Mock Match Registry Extension

The existing `mockFollowMatchRegistry` implements `GetMatch` but panics on
`JoinAttempt`. Phase 2 needs `JoinAttempt` to work:

```go
type mockReplayMatchRegistry struct {
    mockFollowMatchRegistry
    joinAttempts []recordedJoinAttempt
}

type recordedJoinAttempt struct {
    MatchID   MatchID
    UserID    uuid.UUID
    SessionID uuid.UUID
    Username  string
    Metadata  map[string]string
}

func (r *mockReplayMatchRegistry) JoinAttempt(...) (bool, bool, bool, string, string, []*MatchPresence) {
    // Record the attempt
    r.joinAttempts = append(r.joinAttempts, recordedJoinAttempt{...})
    // Return success: found=true, allowed=true, isNew=true
    // Return the presence JSON in reason field (matches production behavior)
    return true, true, true, presenceJSON, labelJSON, nil
}
```

### 3.4 Mock Session Registry

`PrepareEntrantPresences` calls `sessionRegistry.Get(sid)` for each party
member. The mock must return the test sessions:

```go
type mockReplaySessionRegistry struct {
    testSessionRegistry // embed existing stub
    sessions map[uuid.UUID]Session
}

func (r *mockReplaySessionRegistry) Get(sessionID uuid.UUID) Session {
    return r.sessions[sessionID]
}
```

### 3.5 Global Singletons

`lobbyFind` accesses:

- `globalAppBot.Load()` in `LobbyJoinEntrants` for suspension enforcement
- `globalMatchmaker.Load()` in matchmaker ticket submission
- `ServiceSettings()` for encoder flags

Tests must set these to safe defaults before running:

```go
globalAppBot.Store(nil) // skips enforceJoinSuspension
// globalMatchmaker only needed if testing matchmaker ticket path
```

---

## 4. Fixture Format for Phase 2

### 4.1 Structure

```json
{
  "name": "bigduckii-follower-social-convergence",
  "description": "Follower in duo party joins social lobby via reservation system",
  "source_log": "nakama-2026-06-19T17-48-09.533.log",
  "time_window": "2026-06-18T20:14:45Z/2026-06-18T20:23:18Z",
  "phase": 2,

  "config": {
    "matchmaking_timeout_secs": 600,
    "reservation_expiry_secs": 15,
    "crash_reconnect_window_secs": 27
  },

  "players": {
    "player_A": {
      "uid": "uid-00001",
      "sid": "sid-00001",
      "evr_id": "OVR-ORG-10001",
      "username": "player_A",
      "is_pcvr": false
    },
    "player_B": {
      "uid": "uid-00002",
      "sid": "sid-00002",
      "evr_id": "OVR-ORG-10002",
      "username": "player_B",
      "is_pcvr": false
    }
  },

  "party": {
    "id": "party-00001",
    "leader": "player_B",
    "members": ["player_A", "player_B"],
    "group_name": "code noodle"
  },

  "matches": {
    "match-00001": {
      "mode": "echo_arena",
      "open": false,
      "player_limit": 8,
      "players": ["player_A", "player_B"]
    },
    "match-00002": {
      "mode": "social_2.0",
      "open": true,
      "player_limit": 12,
      "players": []
    }
  },

  "initial_state": {
    "player_A": {
      "current_match": "match-00001",
      "lifecycle_state": "InMatch"
    },
    "player_B": {
      "current_match": "match-00001",
      "lifecycle_state": "InMatch",
      "is_matchmaking": false
    }
  },

  "messages": [
    {
      "ts": "2026-06-18T20:14:45.210Z",
      "player": "player_A",
      "type": "LobbyFindSessionRequest",
      "fields": {
        "mode": "social_2.0",
        "level": "0xffffffffffffffff",
        "group_id": "group-00001",
        "role": -1
      }
    },
    {
      "ts": "2026-06-18T20:14:45.253Z",
      "player": "player_A",
      "type": "GameServerPlayerRemoved",
      "fields": {
        "match_id": "match-00001",
        "reason": 3
      }
    }
  ],

  "expected_outcomes": {
    "player_A": {
      "final_match": "match-00002",
      "received_lobby_session_success": true,
      "lobby_find_returned_nil": true,
      "stuck_duration_max_secs": 5,
      "lifecycle_transitions": [
        {
          "from": "InMatch",
          "to": "Holding",
          "reason": "waiting for leader's ticket"
        },
        {
          "from": "Holding",
          "to": "SocialReady",
          "reason": "joined social lobby"
        }
      ]
    }
  }
}
```

### 4.2 Key Differences from Phase 1 Fixtures

| Aspect              | Phase 1             | Phase 2                                         |
| ------------------- | ------------------- | ----------------------------------------------- |
| What's replayed     | State transitions   | Client messages through pipeline handlers       |
| Players             | Single player       | Multi-player (party members)                    |
| Initial state       | Implicit (Idle)     | Explicit: current match, lifecycle state, party |
| Match state         | Not tracked         | Pre-configured match labels with player lists   |
| Expected output     | Transition sequence | Outcomes: match placement, messages sent        |
| Party configuration | Not needed          | Leader, members, group name                     |
| Config values       | Not needed          | Timeouts, reservation expiry                    |

### 4.3 Field Semantics

- **`messages`**: The client-to-server messages to feed through the pipeline,
  in chronological order. Each message is reconstructed into an `evr.Message`
  before being passed to `lobbyFind`.

- **`expected_outcomes`**: Per-player assertions. `final_match` is the match ID
  the player should end up in (verified via the service stream tracker
  presence). `received_lobby_session_success` asserts that `SendEvr` was
  called with a `LobbySessionSuccessv5`. `stuck_duration_max_secs` asserts
  the pipeline returned within that time (catches infinite loops).

- **`matches`**: Pre-configured match state. The test harness loads these
  into the mock match registry before replay starts. Match labels can be
  mutated during replay (e.g., a match closes mid-scenario).

---

## 5. The BigDuckII Test Case

### 5.1 Scenario Reconstruction

**Timeline** (anonymized, timestamps preserved):

```
20:14:45.210  player_A (bigduckii, follower) sends LobbyFindSessionRequest (social_2.0)
20:14:45.210  player_A joins party group (party_size=2, is_leader=false, leader=player_B)
20:14:45.253  player_A leaves match-00001 (arena, reason=3)

             The follower is now in the follow path inside lobbyFind.
             In the OLD code:
               - TryFollowPartyLeader returns false (leader matchmaking)
               - headingToSocial=true, so follower goes to lobbyFindOrCreateSocial
               - But the leader's matchmaking presence causes "falling through"
               - 8 iterations of Matchmaking -> SocialReady -> Matchmaking

             In the NEW code (with reservations):
               - findReservation checks leader's match for a reservation
               - If reservation found: direct join to leader's lobby
               - If not: follower goes to lobbyFindOrCreateSocial independently
               - Party reservations ensure convergence without polling
```

### 5.2 Test Structure

```go
func TestPhase2_BigDuckII_FollowerConverges(t *testing.T) {
    fix := loadPhase2Fixture(t, "testdata/replay/phase2-bigduckii-follower.json")
    env := newReplayTestEnv(t, fix)

    // Feed the LobbyFindSessionRequest for player_A through lobbyFind.
    err := env.RunLobbyFind("player_A")

    // OUTCOME 1: lobbyFind returned nil (no error = player was placed)
    require.NoError(t, err, "lobbyFind should return nil for follower convergence")

    // OUTCOME 2: Player received LobbySessionSuccess
    successMsgs := env.SentMessagesOfType("player_A", "*evr.LobbySessionSuccessv5")
    assert.GreaterOrEqual(t, len(successMsgs), 1,
        "follower should receive LobbySessionSuccess")

    // OUTCOME 3: Player ended up in a social lobby
    finalMatch := env.CurrentMatch("player_A")
    assert.NotNil(t, finalMatch, "follower should be in a match")
    if finalMatch != nil {
        assert.Equal(t, evr.ModeSocialPublic, finalMatch.Mode,
            "follower should end up in a social lobby")
    }

    // OUTCOME 4: No infinite loop (test completed within timeout)
    // The test framework enforces a timeout via t.Deadline() or context.

    // OUTCOME 5: Lifecycle transitions are clean
    lc := env.GetLifecycle("player_A")
    history := lc.History()
    for _, tr := range history {
        if tr.From == StateMatchmaking && tr.To == StateSocialReady {
            t.Error("NEW CODE BUG: follower still bouncing Matchmaking->SocialReady")
        }
    }
}
```

### 5.3 What Must Be Set Up

1. **Party state**: `PartyHandler` with player_B as leader, player_A as
   follower, in party "code noodle".

2. **Match state**: `match-00001` (arena, closed, both players listed).
   A social lobby (`match-00002`) must be available or creatable.

3. **Tracker state**: Both players have service streams pointing to
   `match-00001`. Player_B may or may not be on the matchmaking stream
   (depends on the exact moment in the scenario).

4. **Reservation state**: In the new code, when the leader joined a social
   lobby, the reservation system should have created a placeholder for
   player_A. The mock match registry must have this reservation in the
   match label's `Players` slice.

5. **Session registry**: Both sessions must be retrievable by SID.

---

## 6. Practical Assessment: What Needs Refactoring

### 6.1 Dependencies and Their Mock Status

| Dependency                  | Current mock              | What Phase 2 needs          | Gap      |
| --------------------------- | ------------------------- | --------------------------- | -------- |
| `Tracker`                   | `mockMatchmakingTracker`  | Same (fully functional)     | None     |
| `MatchRegistry.GetMatch`    | `mockFollowMatchRegistry` | Same                        | None     |
| `MatchRegistry.JoinAttempt` | Panics                    | Record + return success     | **New**  |
| `SessionRegistry.Get`       | Returns nil               | Returns test sessions       | **New**  |
| `session.SendEvr`           | Real WebSocket write      | Capture buffer              | **New**  |
| `session.Send`              | Real WebSocket write      | Capture buffer              | **New**  |
| `p.nk.metrics`              | Nil (panics)              | No-op stub                  | **New**  |
| `p.appBot`                  | Nil (panics on log call)  | No-op stub                  | **New**  |
| `p.guildGroupRegistry`      | Nil                       | No-op stub                  | **New**  |
| `p.lobbyAuthorize`          | Not mockable (method)     | Needs override or bypass    | **New**  |
| `globalAppBot`              | Nil                       | Must set to nil before test | Trivial  |
| `globalMatchmaker`          | Nil                       | Only if testing ticket path | Deferred |
| `createLobbyMu`             | Global mutex              | Works as-is                 | None     |

### 6.2 Required Refactoring

**Refactoring 1: `sessionWS.sendEvrHook` (small)**

Add a test-only hook to `sessionWS` that intercepts `SendEvr` calls. This
is the same pattern used in `evr_pipeline_test.go` where `SessionMeta` is
set (line 25). The hook is nil in production; test code sets it to capture
messages.

Scope: ~10 lines in `session_ws.go`. Backward-compatible.

**Refactoring 2: `mockReplayMatchRegistry` (small)**

Extend `mockFollowMatchRegistry` with a `JoinAttempt` implementation that
records calls and returns configurable success/failure. The existing struct
already has the `matches` map and `getMatchCalls` counter; add a similar
pattern for join attempts.

Scope: ~50 lines in a test file. No production code changes.

**Refactoring 3: `mockReplaySessionRegistry` (small)**

Create a session registry mock that returns pre-configured sessions by SID.
The existing `testSessionRegistry` returns nil for everything; Phase 2 needs
it to return the test `sessionWS` instances so `PrepareEntrantPresences`
can resolve party members.

Scope: ~30 lines in a test file. No production code changes.

**Refactoring 4: No-op metrics (small)**

The existing `testMetrics` struct (used in `evr_lobby_find_test.go` line 39)
may already satisfy this. If not, add `CustomCounter` and `CustomTimer`
methods that do nothing.

Scope: ~10 lines. No production code changes.

**Refactoring 5: `lobbyAuthorize` bypass (small)**

`lobbyAuthorize` checks guild membership and feature flags. In tests, it
should be a no-op. Options:

A. Make `lobbyAuthorize` a field on `EvrPipeline` (function pointer).
Tests set it to `func(...) error { return nil }`.

B. Check for a nil `guildGroupRegistry` at the top of `lobbyAuthorize`
and return nil early. This is already partially true -- several paths
check for nil components.

Option B is simpler and already partially implemented. Verify the existing
nil checks cover the test path.

Scope: 0-5 lines in production code.

### 6.3 No Refactoring Needed

The following can be handled without production code changes:

- **`p.appBot`**: Set to nil on the test `EvrPipeline`. The `appBot.LogUserErrorMessage`
  call in `lobbySessionRequest` is in the error path; if `lobbyFind` succeeds,
  `appBot` is never accessed. For error-path tests, guard with `if p.appBot != nil`.

- **`p.nk` construction**: Create a minimal `RuntimeGoNakamaModule` with just
  `tracker`, `matchRegistry`, `sessionRegistry`, and `metrics` fields set.
  Other fields (DB, runtime, storage) are only accessed in code paths not
  exercised by Phase 2 tests.

- **`globalMatchmaker`**: Only needed if testing the `lobbyMatchMakeWithFallback`
  path. Phase 2 focuses on the follow path and social lobby convergence,
  which do not submit matchmaker tickets. Defer matchmaker replay to Phase 3.

### 6.4 Risk Assessment

| Risk                                                      | Severity | Mitigation                                        |
| --------------------------------------------------------- | -------- | ------------------------------------------------- |
| `sessionWS` constructor requires fields not easily mocked | Medium   | Use the `newFollowTestEnv` pattern (partial init) |
| `lobbyFindOrCreateSocial` calls `ListMatchStates`         | Medium   | Mock `MatchRegistry.ListMatches` in the registry  |
| `PrepareEntrantPresences` reads session context values    | Medium   | Set `ctxLobbyParametersKey` on session context    |
| Race conditions from goroutines in `lobbyFind`            | Low      | Run with `-race`; existing tests handle this      |
| `createLobbyMu` global lock contention                    | Low      | Tests are sequential per fixture                  |

---

## 7. Implementation Build Order

### Phase 2a: Harness scaffolding (2-3 days)

1. Add `sendEvrHook` to `sessionWS` (Refactoring 1).
2. Create `mockReplayMatchRegistry` with `JoinAttempt` (Refactoring 2).
3. Create `mockReplaySessionRegistry` (Refactoring 3).
4. Verify `testMetrics` has the needed methods (Refactoring 4).
5. Verify `lobbyAuthorize` nil-safety (Refactoring 5).
6. Build `replayTestEnv` constructor that wires everything together.
7. Write one smoke test: solo player `LobbyFindSessionRequest` for social
   mode -> `lobbyFind` -> `lobbyFindOrCreateSocial` -> verify
   `LobbySessionSuccess` sent.

### Phase 2b: Duo party follow tests (2-3 days)

1. Load the bigduckii Phase 2 fixture.
2. Set up party state (leader + follower).
3. Set up tracker state (both in arena match, leader matchmaking or not).
4. Call `lobbyFind` for the follower.
5. Assert: follower receives `LobbySessionSuccess`, ends up in social lobby.
6. Assert: no `Matchmaking -> SocialReady` bounce loop.
7. Assert: `lobbyFind` returns nil within 10 seconds.

### Phase 2c: Negative / regression tests (1-2 days)

1. Replay the old-code scenario: verify the loop IS reproduced when the
   reservation system is not wired up (baseline regression).
2. Replay lethal_zed16: follower with stale match reference.
3. Replay newfishkeep: duplicate authorization.

### Phase 2d: Additional coverage (1-2 days)

1. Healthy party: leader + follower both converge to social lobby.
2. Solo player: lobby find for social, arena (matchmaker path -- deferred
   unless matchmaker mock is ready).
3. Leader in arena match: follower redirected to social lobby (not stuck
   in poll).

---

## 8. Fixture Extraction from Production Logs

Phase 2 fixtures require more data than Phase 1. The extractor must capture:

### 8.1 Additional Fields

- **Message type and structured payload**: For `LobbyFindSessionRequest`,
  capture `mode`, `level`, `group_id`, `role`. For `GameServerPlayerRemoved`,
  capture `match_id`, `reason`.

- **Party state at event time**: `party_id`, `party_size`, `is_leader`,
  `leader_sid`, `leader_uid`. Captured from `"Joined party group"` log
  entries.

- **Match state snapshots**: When a player joins or leaves a match, capture
  the match label (mode, open, player_count, player_limit). Captured from
  `"Joined entrant."` and `"Player leaving the match."` entries.

- **Decision-path messages**: `"Leader is currently matchmaking, falling
through"`, `"Follower already in leader's match"`, etc. These are not fed
  to the replay engine but stored as the expected decision sequence for
  comparison.

### 8.2 Multi-Player Trace Merging

Party scenarios require merged traces from all party members. The extractor:

1. Finds the party ID from the target player's trace.
2. Finds all session IDs in that party (from `"Joined party group"` entries).
3. Extracts each member's trace.
4. Merges by timestamp.
5. Captures the game server's perspective (match joins/leaves from the
   match handler logs, keyed by match ID).

### 8.3 Fixture Validation

Before a fixture is committed, validate:

- All referenced match IDs have corresponding match state entries.
- All party members have player entries.
- The `initial_state` is consistent with the first events in the trace
  (e.g., if a player is listed as InMatch at match-00001, the first event
  should not be a `LobbyFindSessionRequest` from a different match).
- The `expected_outcomes` are manually verified against the production logs.

---

## 9. What Phase 2 Does NOT Cover

Phase 2 focuses on the `lobbyFind` follow path and social lobby convergence.
It does NOT cover:

- **Matchmaker ticket submission and matching.** The matchmaker is a Nakama
  internal system (`LocalMatchmaker`) with complex state. Mocking it for
  replay would require either (a) replaying matchmaker events from logs
  (which don't contain enough internal state), or (b) running a real
  matchmaker with seeded inputs. Both are Phase 3+ scope.

- **Game server simulation.** Phase 2 does not inject `GameServerJoinAttempt`
  or verify the game server's response to `JoinAttempt`. The mock match
  registry accepts all joins. Game server integration replay is Phase 3.

- **Login / authentication pipeline.** Phase 2 bypasses `lobbySessionRequest`
  and feeds `lobbyFind` directly with pre-constructed parameters.

- **Network-layer issues.** WebSocket disconnects, reconnects, message
  ordering. These require the full `ProcessRequestEVR` path (Option C).

---

## 10. Success Criteria

Phase 2 is complete when:

1. The bigduckii scenario produces a `LobbySessionSuccess` for the follower
   (player_A) within 10 seconds of the `LobbyFindSessionRequest`.

2. The same fixture, run against old code (pre-reservation), reproduces the
   infinite `Matchmaking -> SocialReady` loop (baseline regression).

3. At least 3 additional fixtures pass: healthy party convergence,
   lethal_zed16 stale skip, and solo social lobby join.

4. All tests run in CI via `go test ./server/ -run TestPhase2` in under
   30 seconds total.

5. No production code changes beyond the `sendEvrHook` addition to
   `sessionWS` (all other mocking is in test files).
