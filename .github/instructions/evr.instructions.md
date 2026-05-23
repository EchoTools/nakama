---
description: 'EVR binary protocol, pipeline, and matchmaking patterns for the EchoVR Nakama fork.'
applyTo: 'server/evr_*.go,server/evr/**'
---

> **REFACTOR ALERT:** This repo is undergoing EVR module extraction. Before
> starting any work, read `REFACTOR.yaml` and
> `project-management.instructions.md` to orient in the plan.
> **Do not make unrelated changes outside the current phase's tasks.**

# EVR Module — Protocol & Pipeline Patterns

The EVR module extends Nakama with EchoVR game server functionality. It handles binary protocol messages from EVR game clients, manages matchmaking, and orchestrates game server allocation.

## Binary Protocol Message Pattern

All EVR messages in `server/evr/` use binary encoding with a standard interface:

```go
// Every EVR message implements this
type Message interface {
    Symbol() Symbol            // Unique uint64 message identifier
    Stream(s *EasyStream) error // Binary encode/decode
}
```

### Message Structure

Each message type has its own file in `server/evr/`:

```go
type MyMessage struct {
    Field1 string
    Field2 int32
}

func (m *MyMessage) Symbol() Symbol { return SymbolMyMessage }

func (m *MyMessage) Stream(s *EasyStream) error {
    return RunErrorFunctions([]func() error{
        func() error { return s.StreamString(&m.Field1) },
        func() error { return s.StreamInt32(&m.Field2) },
    })
}
```

- Use `Symbol()` for message routing — switch on the uint64 hash.
- Use `Stream()` for both encoding and decoding (the `EasyStream` mode determines direction).
- Register new message types in `NewMessageFromHash()` in `server/evr/core_packet_types.go`.

### Stream Helper Pattern

The `EasyStream` type handles binary read/write with mode switching:

```go
if s.Mode == DecodeMode {
    // Allocate slices based on s.Len() in decode mode
    m.Items = make([]Item, s.Len())
}
return RunErrorFunctions([]func() error{
    func() error { return s.StreamBytes(&m.Payload, len(m.Payload)) },
})
```

## Pipeline Handler Pattern

The EVR pipeline in `server/evr_pipeline*.go` routes messages to typed handlers:

```go
func (p *EVRPipeline) handleMyMessage(ctx context.Context, msg *evr.MyMessage) error {
    // Access session, database, logger from p.*
    // Return nil on success, error to signal failure
}
```

- Pipeline handlers receive a decoded message type — the switch/case in the pipeline dispatcher handles the routing.
- Handlers have access to the full pipeline context: `p.session`, `p.db`, `p.logger`, `p.router`.
- Use `p.logger` (slog) for logging, not standalone `slog` calls.

## Match Handler Pattern

Match handlers in `server/evr_match*.go` implement the Nakama match interface:

```go
func (m *Match) MatchInit(ctx context.Context, logger *slog.Logger, db *sql.DB, nk runtime.NakamaModule, params map[string]interface{}) (interface{}, int, error) {
    // Initialize match state, return tick rate
}

func (m *Match) MatchJoin(ctx context.Context, logger *slog.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state interface{}, presences []runtime.Presence) interface{} {
    // Handle player joining
}

func (m *Match) MatchLoop(ctx context.Context, logger *slog.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state interface{}, messages []runtime.MatchData) interface{} {
    // Process match messages, return state or signal end
}

func (m *Match) MatchTerminate(ctx context.Context, logger *slog.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state interface{}, graceSeconds int) interface{} {
    // Cleanup on match end
}
```

## Matchmaker Pattern

Custom EVR matchmaking in `server/evr_matchmaker*.go` handles skill-based team assignment:

- Uses OpenSkill ratings for player skill estimation
- Team balancing through configurable algorithms
- Reservation system for allocated matches
- Priority queue handling

Look for key types: `Matchmaker`, `MatchmakerEntry`, `MatchmakerResult`.

## Discord Bot

The Discord integration in `server/evr_discord_*.go` provides:

- **App bot** (`evr_discord_appbot*.go`): Slash commands, mod panels, server management
- **Linked roles** (`evr_discord_linked_roles*.go`): Discord linked role verification
- **Integrator** (`evr_discord_integrator*.go`): Platform role assignment and pruning

Uses `github.com/bwmarrin/discordgo`. Pattern: register handlers, respond to interactions.

## Runtime RPCs

EVR runtime RPCs in `server/evr_runtime_rpc*.go` use the standard Nakama RPC pattern:

```go
func rpcMyFunction(ctx context.Context, logger *slog.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
    // Parse payload, do work, return response
}
```

Registered via `initialiseEVRRuntime()` in `server/evr_runtime.go`.

## Key Types

| Type | Package | Purpose |
|---|---|---|
| `Symbol` | `server/evr` | uint64 message identifier |
| `EasyStream` | `server/evr` | Binary stream encoder/decoder |
| `EVRPipeline` | `server` | Main message pipeline |
| `Match` | `server` | EVR match handler state |
| `EvrId` | `server/evr` | EchoVR player identifier |
| `LoginIdentifier` | `server/evr` | Interface for session-linked messages |

## Common Mistakes

- Forgetting to register new message types in `NewMessageFromHash()`
- Using `Stream()` methods that don't handle both encode and decode modes
- Not checking `s.Mode == DecodeMode` before allocating slices in `Stream()`
- Adding business logic in protocol type files (keep `Stream()` focused on serialization)
- Mixing EVR and non-EVR dependencies in the same file
- Using `context.Background()` instead of the context passed to pipeline handlers
