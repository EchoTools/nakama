# EVR PROTOCOL PACKAGE

**Scope:** `/server/evr` (99 files - binary protocol parsers)

## OVERVIEW

Binary protocol for EchoVR. 90+ message types, custom codec, symbol routing.

## WHERE TO LOOK

| Task | Files |
|------|-------|
| **Codec** | `core_packet.go`, `core_stream.go` |
| **Messages** | All `*_*.go` (~90 types) |
| **Login** | `login_*.go` |
| **Match** | `match_*.go` |
| **Protobuf** | `core_protobuf.go` |

## CONVENTIONS

- **Symbol routing**: 64-bit hash (e.g., `0xbdb41ea9e67b200a`), not numeric IDs
- **Message interface**: `Symbol() Symbol` + `Stream(s *Stream) error`
- **Packet format**: `[Marker(8)][Symbol(8)][Length(8)][Payload(n)]` little-endian
- **Max**: 10MB packet, 32KB message
- **Compression**: Optional zlib/zstd per field

## PATTERNS

```go
// Message
type LoginRequest struct { EvrID EvrId; Token string }
func (m *LoginRequest) Symbol() Symbol { return SymbolLoginRequest }
func (m *LoginRequest) Stream(s *Stream) error {
    return RunErrorFunctions([]ErrorFunction{
        func() error { return s.Stream(&m.EvrID) },
        func() error { return s.Stream(&m.Token) },
    })
}

// Symbols
const (
    SymbolLoginRequest Symbol = 0xbdb41ea9e67b200a
    SymbolLoginSuccess Symbol = 0x1ca6e60ac605d000
)

// Codec
stream := NewEncodingStream()
msg.Stream(stream)
bytes := stream.Bytes()
```

## CATEGORIES

Login (10+), Lobby (15+), GameServer (8+), Match (12+), Config (5+), IAP (2+), Protobuf (2), Utility (10+)

## VS STANDARD NAKAMA

| EVR | Nakama |
|-----|--------|
| 64-bit symbol hash | Numeric type codes |
| Custom binary | Protobuf-first |
| EvrID strings | User IDs |
| Symbol dispatch | Protobuf unmarshal |
| Per-message compress | Transport gzip |
| Versioned (v1-v5) | Single version |

## TESTING

```bash
go test -short -vet=off ./server/evr/...
go test -v -vet=off ./server/evr -run TestLoginRequest
go test -race -vet=off ./server/evr/...
```

## NOTES

- **Hybrid protobuf**: `NEVRProtobufMessageV1` wrapper for migration
- **Cross-repo**: `github.com/echotools/nevr-common` protobufs
- **Flat structure**: All files at depth 2
- **Test cohabitation**: Every `*_test.go` next to source
- **Legacy compat**: Echo VR game client (closed-source)
