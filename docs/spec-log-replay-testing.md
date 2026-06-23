# Log-Replay Testing Framework

## Purpose

Use production log traces to create deterministic system tests that verify the
party/matchmaking/lifecycle state machine behaves correctly after refactoring.
A divergence between production behavior and replay behavior means the refactoring
changed semantics.

---

## 1. Log Format Reference

Production logs live at `/var/log/nakama-logs/`. Each file is newline-delimited
JSON, ~1 GB, covering roughly 24 hours. The files relevant to the known bug cases
(2026-06-18) are:

- `nakama-2026-06-18T18-20-01.575.log` (Jun 17 19:37 -- Jun 18 18:20)
- `nakama-2026-06-19T17-48-09.533.log` (Jun 18 18:20 -- Jun 19 17:48)

### 1.1 Common Fields

Every log line contains:

```json
{
  "level": "debug|info|warn|error",
  "ts": "2026-06-18T20:14:45.210Z",
  "caller": "server/<file>.go:<line>",
  "msg": "<human-readable message>"
}
```

### 1.2 Identity Fields

| Field                    | Meaning                             | Example                                |
| ------------------------ | ----------------------------------- | -------------------------------------- |
| `uid`                    | Nakama user ID                      | `ecc390b5-2cc9-48c8-9f4a-876425a494a2` |
| `sid`                    | WebSocket session ID                | `530a6a81-6b42-11f1-8d31-383ec87527a1` |
| `login_sid` / `loginsid` | Login session (parent auth session) | UUID                                   |
| `evrid`                  | EchoVR platform ID                  | `OVR-ORG-18764`                        |
| `username`               | Display name                        | `bigduckii`                            |
| `mid`                    | Match ID (short)                    | UUID                                   |
| `match_id`               | Match ID (with node)                | `<uuid>.nakama2_us-east`               |
| `party_id`               | Party ID (with node)                | `<uuid>.nakama2_us-east`               |
| `operator_id`            | Game server operator UID            | UUID                                   |
| `server_id`              | Game server numeric ID              | uint64 as string                       |

Note: `login_sid` and `loginsid` are used inconsistently across log sites.
The extractor must normalize to one form.

### 1.3 Message Types of Interest

The following message types form the replay-relevant event stream for a single
player session. Grouped by source direction:

**Client --> Server (received):**

| `msg` value          | `request_type`                      | Source file         |
| -------------------- | ----------------------------------- | ------------------- |
| `"Received message"` | `*evr.LobbyFindSessionRequest`      | `session_ws.go:470` |
| `"Received message"` | `*evr.LobbyMatchmakerStatusRequest` | `session_ws.go:470` |
| `"Received message"` | `*evr.LobbyPingResponse`            | `session_ws.go:470` |
| `"Received message"` | `*evr.LobbyPendingSessionCancel`    | `session_ws.go:470` |
| `"Received message"` | `*evr.LoginRequest`                 | `session_ws.go:470` |
| `"Received message"` | `*evr.SNSParty*Request`             | `session_ws.go:470` |

**Server --> Client (sent):**

| `msg` value                                    | Key fields                                | Source file                      |
| ---------------------------------------------- | ----------------------------------------- | -------------------------------- |
| `"Sending messages."`                          | `message: ["*evr.LobbySessionSuccessv5"]` | `evr_pipeline_matchmaker.go:202` |
| `"Sending *evr.LobbyMatchmakerStatus message"` |                                           | `session_ws.go`                  |
| `"Sending *evr.LobbyEntrantsV3 message"`       | `team_index`, `entrant_id`                | `session_ws.go:694`              |
| `"Sending *evr.LobbyPingRequest message"`      |                                           | `session_ws.go`                  |

**Game server --> Nakama:**

| `msg` value          | `request_type`                 | Source file         |
| -------------------- | ------------------------------ | ------------------- |
| `"Received message"` | `*evr.GameServerJoinAttempt`   | `session_ws.go:470` |
| `"Received message"` | `*evr.GameServerPlayerRemoved` | `session_ws.go:470` |

**Internal state transitions:**

| `msg` value                                   | Key fields                                                   | Source file                    |
| --------------------------------------------- | ------------------------------------------------------------ | ------------------------------ |
| `"Player match lifecycle transition"`         | `from`, `to`, `reason`, `legal`                              | `evr_match_lifecycle.go:238`   |
| `"Illegal player match lifecycle transition"` | `from`, `to`, `reason`, `legal`, `anomaly`                   | `evr_match_lifecycle.go:247`   |
| `"Player match lifecycle reset"`              | `from`                                                       | `evr_match_lifecycle.go`       |
| `"Player joining the match."`                 | `uid`, `sid`, `role`                                         | `evr_match.go:623`             |
| `"Player leaving the match."`                 | `uid`, `sid`, `reason`, `duration`                           | `evr_match.go:687`             |
| `"Joined party group"`                        | `party_id`, `party_size`, `is_leader`, `leader_sid`          | `evr_lobby_find.go:398`        |
| `"Party is ready"`                            | `leader`, `size`, `members`                                  | `evr_lobby_find.go:514`        |
| `"Lobby find complete"`                       | `mode`, `party_size`, `duration`                             | `evr_lobby_find.go:300`        |
| `"Matchmaking ticket added"`                  | `ticket`, `query`, `string_properties`, `numeric_properties` | `evr_lobby_matchmake.go:363`   |
| `"Social lobby search"`                       | `candidates`, `query`                                        | `evr_lobby_find.go:747`        |
| `"Authorized access to lobby session"`        | `gid`, `display_name`                                        | `evr_lobby_joinentrant.go:621` |
| `"Joined entrant."`                           | `mid`, `role`                                                | `evr_lobby_joinentrant.go:324` |

**Decision-path messages (critical for bug reproduction):**

| `msg` value                                                                     | Source file              |
| ------------------------------------------------------------------------------- | ------------------------ |
| `"Leader is currently matchmaking, falling through"`                            | `evr_lobby_find.go:1507` |
| `"Follower already in leader's match, skipping follow path"`                    | `evr_lobby_find.go:73`   |
| `"Already in leader's match"`                                                   | `evr_lobby_find.go:1538` |
| `"Follower and leader share the match being left, not skipping"`                | `evr_lobby_find.go:1277` |
| `"Failed to fetch leader's match label for priority join"`                      | `evr_lobby_find.go`      |
| `"Social lobby guard: current lobby differs from intended target, not a no-op"` | `evr_lobby_find.go`      |

### 1.4 Connection Lifecycle

```json
// Connect:
{"msg": "New WebSocket session connected", "sid": "<uuid>", "client_ip": "..."}
// Disconnect:
{"msg": "Closed client connection", "sid": "<uuid>"}
```

---

## 2. Lifecycle State Machine

9 states, no terminal state (the lifecycle loops):

```
Idle(0) --> SocialConverging(1) --> SocialReady(2) --> Holding(3) --> Matchmaking(4)
                                                   \-> Matchmaking(4)
Matchmaking(4) --> Joining(5) --> InMatch(6) --> Returning(7) --> SocialReady(2)
                                             \-> Crashed(8) --> InMatch(6) [reconnect]
                                                            \-> Idle(0) [timeout]
```

### 2.1 Legal Transitions

```
Idle            --> SocialConverging
SocialConverging --> SocialReady
SocialReady     --> Holding           (non-leader sent LobbyFindSessionRequest)
SocialReady     --> Matchmaking       (leader submits ticket)
SocialReady     --> Idle              (left party)
Holding         --> Matchmaking       (leader's ticket includes this member)
Holding         --> SocialReady       (matchmaking cancelled)
Matchmaking     --> Joining           (match found)
Matchmaking     --> SocialReady       (ticket cancelled / late arrival rebuild)
Joining         --> InMatch           (connected to game server)
Joining         --> SocialReady       (join failed)
InMatch         --> Returning         (match ended naturally)
InMatch         --> Crashed           (client disconnected)
Returning       --> SocialReady       (back in social lobby)
Crashed         --> InMatch           (reconnected within 27s)
Crashed         --> Idle              (reconnect failed)
```

### 2.2 Observer Mode

Currently (Issue #456), illegal transitions are logged at WARN level with
`anomaly: "transition not in legal set"` but are **applied anyway**. The replay
framework must replicate this behavior -- capture all transitions including
illegal ones, and compare the full sequence.

Production stats from one 24-hour log file: **35,958 illegal** transitions vs
**23,658 legal** transitions. The high illegal count is the signal the lifecycle
enforcement is needed, and the bug cases are drawn from these.

---

## 3. Log Extraction

### 3.1 Single-Player Trace

Extract all log lines for a single user within a time window:

```bash
# By username (case-insensitive, covers all log messages):
grep -i 'bigduckii' nakama-2026-06-19T17-48-09.533.log \
  | grep -E '2026-06-18T20:1[4-9]|2026-06-18T20:2[0-3]' \
  > traces/bigduckii-raw.jsonl

# By UID (catches match-side events that log uid but not username):
grep 'ecc390b5-2cc9-48c8-9f4a-876425a494a2' nakama-2026-06-19T17-48-09.533.log \
  | grep -E '2026-06-18T20:1[4-9]|2026-06-18T20:2[0-3]' \
  >> traces/bigduckii-raw.jsonl

# By session ID (catches WebSocket-level events):
grep '2f5cca32-6b58-11f1-8d31-383ec87527a1' nakama-2026-06-19T17-48-09.533.log \
  | grep -E '2026-06-18T20:1[4-9]|2026-06-18T20:2[0-3]' \
  >> traces/bigduckii-raw.jsonl

# Deduplicate and sort by timestamp:
sort -t'"' -k4 traces/bigduckii-raw.jsonl | uniq > traces/bigduckii-sorted.jsonl
```

### 3.2 Multi-Player Trace (Party Scenarios)

Party scenarios require traces from all party members plus the game server.
The party ID links them:

```bash
# Find all sessions in a party:
grep 'party_id.*<party-uuid>' <logfile> | jq -r '.sid' | sort -u

# Extract each member's trace using the session IDs found above.
# Merge and sort by timestamp for the complete party timeline.
```

### 3.3 Fields to Capture Per Event

The extractor produces a normalized `TraceEvent` for each log line:

```go
type TraceEvent struct {
    Timestamp   time.Time          `json:"ts"`
    Level       string             `json:"level"`
    Caller      string             `json:"caller"`
    Message     string             `json:"msg"`
    Direction   string             `json:"direction"`   // "in", "out", "internal"
    MessageType string             `json:"message_type"` // e.g. "LobbyFindSessionRequest"

    // Identity
    UID         string             `json:"uid,omitempty"`
    SID         string             `json:"sid,omitempty"`
    LoginSID    string             `json:"login_sid,omitempty"`
    EvrID       string             `json:"evrid,omitempty"`
    Username    string             `json:"username,omitempty"`

    // Match/Party context
    MatchID     string             `json:"match_id,omitempty"`
    PartyID     string             `json:"party_id,omitempty"`

    // Lifecycle-specific
    FromState   string             `json:"from,omitempty"`
    ToState     string             `json:"to,omitempty"`
    Reason      string             `json:"reason,omitempty"`
    Legal       *bool              `json:"legal,omitempty"`

    // Raw fields for anything not captured above
    Raw         map[string]any     `json:"raw,omitempty"`
}
```

### 3.4 Identifying the Known Bug Cases

**BigDuckII** (2026-06-18 20:14-20:22 UTC):

- Grep: `bigduckii` in `nakama-2026-06-19T17-48-09.533.log`
- Time window: `20:14` through `20:23`
- Key signature: 8 iterations of `"Leader is currently matchmaking, falling through"`
  with `"Social lobby guard"` bounces between lobbies ca27e8ca, 4183e88a, c5f97f5c, 7d87b3b7
- Party leader: `the_noodle_of_doom` (uid `411f8903`)

**lethal_zed16** (2026-06-18 20:15-20:17 UTC):

- Grep: `lethal_zed16` in same log file
- Time window: `20:15` through `20:17`
- Key signature: `"Follower already in leader's match, skipping follow path"` at
  `evr_lobby_find.go:73` referencing match `61e81b72` that the player already left
  34 seconds earlier

**newfishkeep** (2026-06-18 22:22 UTC):

- Grep: `newfishkeep` in same log file
- Time window: `22:22` through `22:23`
- Key signature: Two `"Authorized access to lobby session"` events 9 seconds apart
  for the same match `693b9651`, both followed by `"Already in leader's match"`

**Healthy case**: Any party session where the lifecycle transitions follow the
happy path: `Idle -> SocialConverging -> SocialReady -> Matchmaking -> Joining
-> InMatch -> Returning -> SocialReady`. Grep for a user who has only legal
transitions in a time window.

---

## 4. Anonymization

### 4.1 Fields Requiring Anonymization

| Field                               | Strategy                                                            |
| ----------------------------------- | ------------------------------------------------------------------- |
| `uid`                               | Deterministic hash map: same UID always maps to the same fake UUID  |
| `sid`                               | Deterministic hash map                                              |
| `login_sid` / `loginsid`            | Deterministic hash map                                              |
| `evrid`                             | Map `OVR-ORG-<N>` to `OVR-ORG-<sequential>` (e.g., `OVR-ORG-10001`) |
| `username`                          | Map to `player_A`, `player_B`, etc.                                 |
| `match_id` / `mid`                  | Deterministic hash map (preserve `.nakama2_us-east` suffix)         |
| `party_id`                          | Deterministic hash map (preserve `.nakama2_us-east` suffix)         |
| `operator_id`                       | Deterministic hash map                                              |
| `server_id`                         | Map to sequential integers                                          |
| `client_ip`                         | Replace with `10.0.0.<N>`                                           |
| `display_name` (in message strings) | Replace inline using the username map                               |
| Discord IDs                         | Strip entirely                                                      |
| `ticket` (matchmaker ticket UUID)   | Deterministic hash map                                              |

### 4.2 Deterministic Mapping

```go
type Anonymizer struct {
    mu       sync.Mutex
    maps     map[string]map[string]string // field_type -> original -> fake
    counters map[string]int               // field_type -> next sequential value
}

func (a *Anonymizer) Anonymize(fieldType, original string) string {
    a.mu.Lock()
    defer a.mu.Unlock()
    m := a.maps[fieldType]
    if fake, ok := m[original]; ok {
        return fake
    }
    a.counters[fieldType]++
    fake := fmt.Sprintf("%s-%05d", fieldType, a.counters[fieldType])
    m[original] = fake
    return fake
}
```

The key property: if `bigduckii`'s UID appears 200 times across 200 log lines,
all 200 get the same fake UUID. If `the_noodle_of_doom` appears as `leader_uid`
in BigDuckII's trace, it maps to the same fake UUID as in noodle's own trace.

### 4.3 What to Preserve

- Timestamps (exact, not shifted -- timing matters for bug reproduction)
- Message types and `msg` values
- Lifecycle states (`from`, `to`)
- Game modes (`social_2.0`, `echo_arena`)
- Team assignments (`team_index`, `role`)
- `caller` source locations
- Boolean/numeric fields (`legal`, `party_size`, `is_leader`, `attempt`, `candidates`)
- Matchmaker properties (`rating_mu`, `rating_sigma`, `division`, `min_team_size`, etc.)

### 4.4 Inline String Anonymization

Some fields embed identity data in Go `String()` output:

```
"LobbyFindSessionRequest{Mode: echo_arena, Entrants: [Entrant(evr_id=OVR-ORG-18764, ...)]}"
```

The anonymizer must regex-replace known patterns within these strings:

- `OVR-ORG-<digits>` and `DMO-<digits>`
- UUIDs in standard format
- Known display names (from the username map)

---

## 5. Replay Engine

### 5.1 Architecture Decision: Go Test, Not Standalone Tool

The replay engine runs as `go test` in `server/`:

```
server/
  evr_replay_test.go          -- test harness, TestReplay* functions
  evr_replay_engine.go        -- ReplayEngine struct, event feeding, state capture
  evr_replay_fixtures_test.go -- fixture loading from testdata/
  testdata/
    replay/
      bigduckii-infinite-matchmaking.jsonl
      lethal-zed16-stale-fastpath.jsonl
      newfishkeep-duplicate-join.jsonl
      healthy-party-arena.jsonl
```

Rationale:

- Runs in CI with `go test ./server/ -run TestReplay`
- Direct access to all server types and interfaces -- no IPC boundary
- Follows the project's existing pattern of 148 EVR test files in `server/`
- Can use testify assertions and table-driven test patterns already established
- Can be run against both old and new code by checking out different commits

### 5.2 What to Mock vs What Runs Real

**Runs real (the code under test):**

- `EvrPipeline.ProcessRequestEVR` -- the full message dispatch
- `EvrPipeline.lobbySessionRequest` -- lobby find logic
- `PlayerMatchLifecycle` -- the state machine itself
- `MatchLabel` / `EvrMatch` -- match join/leave handling
- `isLegalTransition` -- transition validation

**Must be mocked:**

| Component               | Interface                                          | Mock behavior                                                                       |
| ----------------------- | -------------------------------------------------- | ----------------------------------------------------------------------------------- |
| `Session`               | `Session` (interface in `session_registry.go`)     | Returns canned identity fields (UID, SID, Username, EvrID). Captures sent messages. |
| `SessionRegistry`       | `SessionRegistry`                                  | Stores mock sessions. `Get(sid)` returns them.                                      |
| `Tracker`               | `Tracker` (interface in `tracker.go`)              | In-memory presence tracking. Tracks join/leave without a real match registry.       |
| `MatchRegistry`         | `MatchRegistry` (interface in `match_registry.go`) | Returns pre-configured `MatchLabel` instances for known match IDs.                  |
| `Database`              | `*sql.DB`                                          | No-op or in-memory (SQLite). Most replay paths don't hit the DB.                    |
| `RuntimeGoNakamaModule` | `nk` runtime                                       | Stub for storage/leaderboard/notification calls.                                    |
| `WebSocket send`        | `session.Send()`                                   | Captures messages into a buffer for later assertion.                                |

### 5.3 Event Feeding

The `ReplayEngine` processes trace events sequentially:

```go
type ReplayEngine struct {
    pipeline    *EvrPipeline
    sessions    map[string]*MockSession  // sid -> mock session
    matches     map[string]*MatchLabel   // match_id -> match state
    transitions []LifecycleTransition    // captured transitions
    sentMsgs    []SentMessage            // captured outbound messages
    clock       time.Time                // current replay time
}

func (r *ReplayEngine) Feed(event TraceEvent) error {
    r.clock = event.Timestamp

    switch {
    case event.Direction == "in":
        // Deserialize the message from the trace and feed it to the pipeline
        msg := r.deserializeMessage(event)
        session := r.getOrCreateSession(event.SID, event)
        r.pipeline.ProcessRequestEVR(session.Logger(), session, msg)

    case event.Message == "Player match lifecycle transition" ||
         event.Message == "Illegal player match lifecycle transition":
        // Don't feed -- this is an output event. Capture it for comparison.
        r.transitions = append(r.transitions, LifecycleTransition{
            Timestamp: event.Timestamp,
            From:      event.FromState,
            To:        event.ToState,
            Reason:    event.Reason,
            Legal:     event.Legal,
        })

    case event.Direction == "out":
        // Outbound messages from game server (GameServerJoinAttempt, etc.)
        // These simulate game server behavior -- feed as if received from server session
        r.feedGameServerEvent(event)
    }
    return nil
}
```

### 5.4 Message Deserialization

The trace stores the Go `String()` representation of messages, not their binary
encoding. The replay engine needs a parser that reconstructs `evr.Message`
instances from these strings, OR the extractor should store enough structured
fields to reconstruct the message:

**Option A: Structured extraction (recommended).** The extractor parses the
`request` string and stores structured fields:

```json
{
  "msg": "Received message",
  "message_type": "LobbyFindSessionRequest",
  "structured": {
    "mode": "echo_arena",
    "level": "0xffffffffffffffff",
    "channel": "<group-uuid>",
    "entrants": [{ "evr_id": "OVR-ORG-10001", "role": -1 }]
  }
}
```

**Option B: Binary recording.** Instrument production to also log the raw
hex-encoded binary alongside the `String()` form. More accurate but requires
a production code change.

Start with Option A. The structured fields for each message type are well-defined
in the `evr` package. If edge cases emerge where the `String()` representation
loses information, switch to Option B for those message types.

### 5.5 Game Server Simulation

Game server messages (`GameServerJoinAttempt`, `GameServerPlayerRemoved`,
`GameServerJoinAllowed`) appear in the production logs with their own session IDs.
The replay engine handles them by:

1. **Injecting from the trace.** When the trace contains a `GameServerJoinAttempt`,
   the engine feeds it through a mock game-server session. The response
   (`GameServerJoinAllowed`) is captured and compared against the trace.

2. **Not simulating.** The engine does not run a real game server. It replays
   the exact game-server messages from the production trace at the recorded
   timestamps. This means the replay tests the Nakama server's response to
   a known game-server event sequence, not the game server itself.

### 5.6 Replay Speed

As fast as possible. No `time.Sleep`. The clock is set from trace timestamps
but execution is not gated to wall time. For timeout-dependent behavior
(e.g., 27-second crash reconnect window, 15-second reservation expiry),
the mock clock must be injectable so time-sensitive code uses the replay
clock rather than `time.Now()`.

```go
type ReplayClock struct {
    now time.Time
}

func (c *ReplayClock) Now() time.Time { return c.now }
func (c *ReplayClock) Set(t time.Time) { c.now = t }
```

This requires the production code to accept a `Clock` interface for
`time.Now()` calls in timeout paths. This is a small, safe refactoring
prerequisite.

---

## 6. State Comparison

### 6.1 Primary Comparison: Lifecycle Transitions

The replay engine captures lifecycle transitions from the `PlayerMatchLifecycle`
instance. The trace extractor captures them from the log. Compare:

```go
type LifecycleTransition struct {
    From   string
    To     string
    Reason string
    Legal  bool
}

func CompareTransitions(production, replay []LifecycleTransition) []Divergence {
    var divergences []Divergence
    maxLen := max(len(production), len(replay))

    for i := 0; i < maxLen; i++ {
        if i >= len(production) {
            divergences = append(divergences, Divergence{
                Index:   i,
                Type:    "extra_replay",
                Replay:  replay[i],
            })
        } else if i >= len(replay) {
            divergences = append(divergences, Divergence{
                Index:      i,
                Type:       "missing_replay",
                Production: production[i],
            })
        } else if production[i] != replay[i] {
            divergences = append(divergences, Divergence{
                Index:      i,
                Type:       "mismatch",
                Production: production[i],
                Replay:     replay[i],
            })
        }
    }
    return divergences
}
```

### 6.2 What Constitutes a Pass

**Regression test (same code version):** Exact match. Every transition in the
production trace must appear in the same order in the replay. Zero divergences.

**Refactoring validation (new code version):** The test specifies expected
behavior changes:

```go
func TestReplay_BigDuckII_NewCode(t *testing.T) {
    trace := loadFixture(t, "bigduckii-infinite-matchmaking.jsonl")
    engine := NewReplayEngine(t)
    result := engine.Replay(trace)

    // The old code produced 8 iterations of Matchmaking -> SocialReady -> Matchmaking
    // The new code should produce Matchmaking -> Joining -> InMatch (no loop)
    assert.Equal(t, 0, countTransitions(result, "Matchmaking", "SocialReady"),
        "new code should not bounce followers back to SocialReady during leader matchmaking")
    assert.GreaterOrEqual(t, countTransitions(result, "Matchmaking", "Joining"), 1,
        "follower should eventually transition to Joining")
}
```

### 6.3 Divergence Report

```
DIVERGENCE at transition #3:
  Production: Matchmaking -> SocialReady (reason: "leader matchmaking, falling through") [legal=false]
  Replay:     Matchmaking -> Joining     (reason: "match found")                          [legal=true]
  Context:    ts=2026-06-18T20:16:38.218Z, sid=player_A-00001, match_id=match-00003

SUMMARY: 5 divergences in 23 transitions
  3x mismatch (transitions #3, #5, #7)
  2x missing_replay (transitions #19, #21 -- the loop iterations that no longer happen)
```

### 6.4 Secondary Comparisons

Beyond lifecycle transitions, optionally compare:

- **Sent messages**: Did the server send the same `LobbySessionSuccessv5` / `LobbyEntrantsV3` messages?
- **Match joins/leaves**: Did the player join/leave the same matches in the same order?
- **Decision-path messages**: Did the server hit the same code paths (same `caller` locations)?

These are informational, not pass/fail. The lifecycle transitions are the
ground truth for behavioral equivalence.

---

## 7. Known Bug Case Fixtures

### 7.1 BigDuckII: Infinite Matchmaking Loop

**Time window:** 2026-06-18 20:14:45 -- 20:23:08 UTC
**Players:** bigduckii (follower), the_noodle_of_doom (leader)
**Bug:** Follower bounces between social lobbies while leader is matchmaking

**Production lifecycle transitions (bigduckii):**

```
InMatch       -> Returning       (match ended)               legal
Returning     -> SocialReady     (back in social lobby)       legal
SocialReady   -> Matchmaking     (lobby find triggered)       -- but then:
Matchmaking   -> SocialReady     (leader matchmaking, fall through) illegal
  [repeat 8 times -- the infinite loop]
SocialReady   -> Matchmaking     (lobby find triggered)
Matchmaking   -> SocialReady     (leader matchmaking, fall through)
  ...
  [client disconnects and reconnects]
SocialReady   -> Matchmaking     (leader submits ticket)      legal
Matchmaking   -> Joining         (match found)                legal
Joining       -> InMatch         (connected)                  legal
```

**Anonymized fixture sample** (`testdata/replay/bigduckii-infinite-matchmaking.jsonl`):

```json
{"ts":"2026-06-18T20:14:45.210Z","level":"debug","caller":"evr_lobby_find.go:398","msg":"Joined party group","sid":"sid-00001","uid":"uid-00001","username":"player_A","evrid":"OVR-ORG-10001","party_id":"party-00001.nakama2_us-east","party_size":2,"is_leader":false,"leader_sid":"sid-00002","leader_uid":"uid-00002"}
{"ts":"2026-06-18T20:14:45.253Z","level":"info","caller":"evr_match.go:687","msg":"Player leaving the match.","mid":"match-00001","uid":"uid-00001","sid":"sid-00001","reason":3,"duration":"312.5s","username":"player_A"}
{"ts":"2026-06-18T20:14:49.083Z","level":"debug","caller":"evr_lobby_find.go:747","msg":"Social lobby search","sid":"sid-00001","uid":"uid-00001","username":"player_A","attempt":0,"candidates":3}
{"ts":"2026-06-18T20:14:49.150Z","level":"debug","caller":"evr_lobby_joinentrant.go:324","msg":"Joined entrant.","sid":"sid-00001","uid":"uid-00001","mid":"match-00002","role":3}
{"ts":"2026-06-18T20:16:38.218Z","level":"debug","caller":"evr_lobby_find.go:398","msg":"Joined party group","sid":"sid-00001","uid":"uid-00001","username":"player_A","party_id":"party-00001.nakama2_us-east","party_size":2,"is_leader":false,"leader_sid":"sid-00002","leader_uid":"uid-00002"}
{"ts":"2026-06-18T20:16:38.220Z","level":"debug","caller":"evr_lobby_find.go:83","msg":"Leader is heading to a social lobby, forcing social mode for follower","sid":"sid-00001"}
{"ts":"2026-06-18T20:16:38.221Z","level":"debug","caller":"evr_lobby_find.go:1489","msg":"User is member of party, attempting to follow leader","sid":"sid-00001"}
{"ts":"2026-06-18T20:16:38.222Z","level":"debug","caller":"evr_lobby_find.go:1507","msg":"Leader is currently matchmaking, falling through","sid":"sid-00001"}
{"ts":"2026-06-18T20:16:38.223Z","level":"debug","caller":"evr_lobby_find.go:197","msg":"Follower in social mode, finding social lobby independently (party reservations will converge)","sid":"sid-00001"}
{"ts":"2026-06-18T20:16:38.300Z","level":"warn","caller":"evr_match_lifecycle.go:247","msg":"Illegal player match lifecycle transition","sid":"sid-00001","from":"Matchmaking","to":"SocialReady","reason":"leader matchmaking, falling through","legal":false,"anomaly":"transition not in legal set"}
```

**Test structure:**

```go
func TestReplay_BigDuckII_InfiniteMatchmaking(t *testing.T) {
    trace := loadFixture(t, "replay/bigduckii-infinite-matchmaking.jsonl")
    engine := NewReplayEngine(t)

    // Replay against current (old) code -- should reproduce the bug
    result := engine.Replay(trace)

    // Verify the loop exists in the replay (regression baseline)
    loopCount := countTransitionPairs(result.Transitions, "Matchmaking", "SocialReady")
    assert.GreaterOrEqual(t, loopCount, 5,
        "old code should produce the infinite matchmaking loop")

    illegalCount := countIllegalTransitions(result.Transitions)
    assert.Greater(t, illegalCount, 0,
        "old code produces illegal transitions during the loop")
}
```

### 7.2 lethal_zed16: Stale Fast-Path Skip

**Time window:** 2026-06-18 20:15:06 -- 20:17:13 UTC
**Bug:** `"Follower already in leader's match, skipping follow path"` fires with a
match ID the player left 34 seconds earlier

**Key transition sequence:**

```
InMatch       -> Returning       (early quit from match-00005)
  -- 34 seconds pass --
SocialReady   -> [no transition] (fast-path fires: "already in leader's match" for match-00005)
```

The server short-circuits the lobby find because it thinks the player is still in
match-00005, but the player already left. No lifecycle transition is emitted because
the fast-path returns early before reaching the transition logic.

### 7.3 newfishkeep: Duplicate Authorization

**Time window:** 2026-06-18 22:22:04 -- 22:23:17 UTC
**Bug:** `"Authorized access to lobby session"` fires twice for the same match,
9 seconds apart

**Key event sequence:**

```
22:22:04  LobbyFindSessionRequest -> "Authorized access" -> "Already in leader's match"
22:22:13  LobbyFindSessionRequest -> "Authorized access" -> "Already in leader's match"  [DUPLICATE]
22:23:08  Matchmaker builds new match -> player joins normally
22:23:17  Player leaves the old match (late cleanup)
```

### 7.4 Healthy Case: Clean Party Arena Session

Extract from any party session with only legal transitions:

```bash
# Find sessions with zero illegal transitions in a time window:
grep 'lifecycle transition' <logfile> \
  | grep -v 'Illegal' \
  | jq -r '.sid' \
  | sort | uniq -c | sort -rn \
  | head -20
# Pick one with a high count (many transitions = full lifecycle)
# Cross-reference to verify no illegal transitions for that sid:
grep '<chosen-sid>' <logfile> | grep 'Illegal' | wc -l  # should be 0
```

The healthy fixture should show the complete happy path:
`Idle -> SocialConverging -> SocialReady -> Matchmaking -> Joining -> InMatch
-> Returning -> SocialReady` and the replay should produce an exact match.

---

## 8. Practical Considerations

### 8.1 Log Volume

A single player generates 200-500 log lines per session (login through disconnect).
The bug case traces are 100-300 lines for the relevant time window. Fixtures are
manageable at this scale -- each is a few KB of JSONL.

**Trimming strategy:** The extractor filters to only replay-relevant message types
(Section 1.3). Internal debug logging (e.g., Discord updates, stat tracking,
remote logs) is excluded.

### 8.2 Multi-Player Coordination

Party scenarios need synchronized traces from 2+ players. The replay engine
must handle interleaved events from multiple sessions:

```go
func (r *ReplayEngine) ReplayMulti(traces map[string][]TraceEvent) *ReplayResult {
    // Merge all traces into a single timeline, sorted by timestamp
    merged := mergeTraces(traces)
    for _, event := range merged {
        r.Feed(event)
    }
    return r.Result()
}
```

The merged timeline preserves causal ordering because production timestamps
are monotonic from a single server. For the BigDuckII case, both bigduckii's
and the_noodle_of_doom's events interleave naturally by timestamp.

### 8.3 Game Server Simulation

The replay engine does **not** simulate a game server. Instead:

1. Game server registration (`BroadcasterRegistrationRequest`) is pre-configured
   in the mock match registry -- the game server "exists" at replay start.
2. `GameServerJoinAttempt` events from the trace are injected through a mock
   game-server session. The server's response (`GameServerJoinAllowed`) is
   captured and compared.
3. `GameServerPlayerRemoved` events are injected at the recorded timestamps.
4. Match state updates (`Received match update message`) are injected to
   advance game clock if needed.

This means the tests verify Nakama's behavior given a known game-server event
sequence, not the game server's behavior itself.

### 8.4 Determinism

**Timestamps:** Preserved from production. The injectable `ReplayClock` ensures
time-dependent code (reservation expiry, crash reconnect window) uses the trace
clock.

**UUIDs:** The production code generates UUIDs for `EntrantID`, session IDs, etc.
For comparison purposes, UUID generation should be injectable:

```go
type ReplayUUIDGenerator struct {
    counter uint64
}

func (g *ReplayUUIDGenerator) New() uuid.UUID {
    g.counter++
    return uuid.FromBytes(binary.BigEndian.AppendUint64(
        make([]byte, 8), g.counter,
    ))
}
```

This makes replays produce the same UUIDs on every run. Comparison ignores
generated UUIDs and focuses on structural equivalence (same transitions,
same code paths, same accept/reject decisions).

**Random selection:** Social lobby search picks from candidates. If the selection
is random, inject a seeded `math/rand` source.

### 8.5 Incremental Build Order

**Phase 1: Single-player social lobby** (1-2 days)

- Mock Session, SessionRegistry, Tracker
- Feed a single `LobbyFindSessionRequest` for `social_2.0`
- Capture the lifecycle transition and compare
- Test: healthy social lobby join produces `Idle -> SocialConverging -> SocialReady`

**Phase 2: Single-player lifecycle loop** (1-2 days)

- Add MatchJoin/MatchLeave handling
- Feed a complete single-player lifecycle (login -> social -> arena -> social)
- Test: healthy player produces the full legal transition loop

**Phase 3: Party follow path** (2-3 days)

- Add multi-session support (leader + follower)
- Mock party tracking (leader session lookup, `current_match_id`)
- Replay the BigDuckII trace -- verify the loop is reproduced
- This is the first real regression test

**Phase 4: Game server integration** (1-2 days)

- Add mock game server session
- Inject `GameServerJoinAttempt` / `GameServerPlayerRemoved` from traces
- Complete the lethal_zed16 and newfishkeep test cases

**Phase 5: Extraction tooling** (1 day)

- Build `cmd/logreplay/extract.go` -- reads raw logs, outputs anonymized JSONL fixtures
- Build `cmd/logreplay/anonymize.go` -- standalone anonymizer
- Document the workflow for adding new fixtures from future production issues

### 8.6 Prerequisites (Small Refactorings)

Before the replay engine works, two small changes to production code:

1. **Clock interface.** Wrap `time.Now()` calls in reservation-expiry and
   crash-reconnect-window code behind a `Clock` interface. Default
   implementation returns `time.Now()`. Replay injects `ReplayClock`.

2. **UUID generator interface.** Wrap `uuid.Must(uuid.NewV4())` calls behind
   a `UUIDGenerator` interface. Default uses real random UUIDs. Replay
   injects deterministic generator.

Both are backward-compatible -- existing code behavior is unchanged. The
interfaces are added alongside the existing calls.

---

## 9. File Layout

```
server/
  evr_replay_engine.go            -- ReplayEngine, event feeding, state capture
  evr_replay_engine_test.go       -- TestReplay_* functions
  evr_replay_extractor.go         -- TraceEvent struct, log line parser
  evr_replay_anonymizer.go        -- Anonymizer, field mapping
  evr_replay_comparison.go        -- CompareTransitions, Divergence report
  evr_replay_mocks_test.go        -- MockSession, MockTracker, MockMatchRegistry
  testdata/
    replay/
      bigduckii-infinite-matchmaking.jsonl
      lethal-zed16-stale-fastpath.jsonl
      newfishkeep-duplicate-join.jsonl
      healthy-party-arena.jsonl

cmd/
  logreplay/
    main.go                        -- CLI: extract, anonymize, replay
```

---

## 10. Open Questions

1. **String() reconstruction accuracy.** The log `request` field is a Go
   `String()` representation, not the original binary. Some fields may be
   lossy (e.g., `Level: 0xffffffffffffffff`). Need to verify that all
   replay-relevant fields survive the `String()` round-trip for each message
   type, or add structured logging to production.

2. **Matchmaker state.** The matchmaker itself (ticket submission, matching
   algorithm, match building) is a Nakama-internal system. Replaying
   `LobbyFindSessionRequest` will submit a real matchmaker ticket, which
   needs a matchmaker mock that produces the same match assignments as
   production. Alternative: treat match assignments as injected events
   (from the trace) rather than computed.

3. **Tracker consistency.** The `Tracker` tracks presences across all matches.
   A full mock must handle presence joins/leaves for multiple simultaneous
   matches. The mock complexity scales with the number of concurrent matches
   in the trace.

4. **Config values.** Reservation timeout (15s/5min), crash reconnect window
   (27s/60s), matchmaker priority threshold -- these affect behavior and must
   match the production config at the time of the log capture. Include a
   config snapshot in the fixture.
