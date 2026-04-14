package evr

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
)

const (
	MaxPacketLength      = 256 * 1024 // 256KB
	MaxMessageLength     = 0x8000     // 32KB
	MaxMessagesPerPacket = 32         // Upper bound on concatenated messages in a single packet
)

var (
	MessageMarker     = []byte{246, 64, 187, 120, 162, 231, 140, 187}
	ErrInvalidPacket  = errors.New("invalid packet")
	ErrSymbolNotFound = errors.New("symbol not found")
	ErrParseError     = errors.New("parse error")

	SymbolTypes = map[uint64]Message{
		// This is the complete list of implemented message types.
		0x013e99cb47eb3669: (*GenericMessage)(nil),
		0x35d810572a230837: (*GenericMessageNotify)(nil),
		0x0dabc24265508a82: (*ReconcileIAPResult)(nil),
		0x1225133828150da3: (*OtherUserProfileFailure)(nil),
		0x1230073227050cb5: (*OtherUserProfileSuccess)(nil),
		0x1231172031050cb2: (*OtherUserProfileRequest)(nil),
		0x128b777ae0ebb650: (*LobbyMatchmakerStatusRequest)(nil),
		0x1bd0fc454c85573c: (*ReconcileIAP)(nil),
		0x244b47685187eae1: (*RemoteLogSet)(nil),
		0x2f03468f77ffb211: (*LobbyJoinSessionRequest)(nil),
		0x312c2a01819aa3f5: (*LobbyFindSessionRequest)(nil),
		0x43e6963ac76beee4: (*STcpConnectionUnrequireEvent)(nil),
		0xb99f11d6ea5cb1f1: (*LobbySessionFailurev1)(nil),
		0x4ae8365ebc45f96a: (*LobbySessionFailurev2)(nil),
		0x4ae8365ebc45f96b: (*LobbySessionFailurev3)(nil),
		0x4ae8365ebc45f96c: (*LobbySessionFailurev4)(nil),
		0x599a6b1bbda3cc13: (*LobbyCreateSessionRequest)(nil),
		0xfabf5f8719bfebf3: (*LobbyPingRequest)(nil),
		0x6047d0043033ae4f: (*LobbyPingResponse)(nil),
		0x6c8f16cd9f8964c5: (*ChannelInfoResponse)(nil),
		0x6d4de3650ee3110f: (*LobbySessionSuccessv5)(nil),
		0x6d54a19a3ff24415: (*UpdateClientProfile)(nil),
		0x7777777777770000: (*GameServerSessionStart)(nil),            // Legacy message
		0x7777777777770200: (*BroadcasterSessionEnded)(nil),           // Legacy message
		0x7777777777770300: (*BroadcasterPlayerSessionsLocked)(nil),   // Legacy message
		0x7777777777770400: (*BroadcasterPlayerSessionsUnlocked)(nil), // Legacy message
		0x7777777777770500: (*GameServerJoinAttempt)(nil),             // Legacy message
		0x7777777777770600: (*GameServerJoinAllowed)(nil),             // Legacy message
		0x7777777777770700: (*GameServerJoinRejected)(nil),            // Legacy message
		0x7777777777770800: (*GameServerPlayerRemoved)(nil),           // Legacy message
		0x7777777777777777: (*BroadcasterRegistrationRequest)(nil),    // Legacy message
		0xa0687d9799640878: (*SNSLobbySetSpawnBotOnServer)(nil),       // Bot spawn command
		0x82869f0b37eb4378: (*ConfigRequest)(nil),
		0xb9cdaf586f7bd012: (*ConfigSuccess)(nil),
		0x9e687a63dddd3870: (*ConfigFailure)(nil),
		0x8d5ad3c4f2166c6c: (*FindServerRegionInfo)(nil),
		0x8da9eb83ffee9fd6: (*LobbyPendingSessionCancel)(nil),
		0x8f28cf33dabfbecb: (*LobbyMatchmakerStatus)(nil),
		0x90758e58515724e0: (*ChannelInfoRequest)(nil),
		0x9af2fab2a0c81a05: (*LobbyPlayerSessionsRequest)(nil),
		0xa1b9cae1f8588968: (*LobbyEntrantsV2)(nil),
		0xa1b9cae1f8588969: (*LobbyEntrantsV3)(nil),
		0xbdb41ea9e67b200a: (*LoginRequest)(nil),
		0xa5acc1a90d0cce47: (*LoginSuccess)(nil),
		0xa5b9d5a3021ccf51: (*LoginFailure)(nil),
		0xb56f25c7dfe6ffc9: (*BroadcasterRegistrationFailure)(nil),
		0xb57a31cdd0f6fedf: (*BroadcasterRegistrationSuccess)(nil),
		0xd06ae97220a7b41f: (*DocumentFailure)(nil),
		0xd07ffd782fb7b509: (*DocumentSuccess)(nil),
		0xd2986849b36b9c72: (*UserServerProfileUpdateRequest)(nil),
		0xd299785ba56b9c75: (*UserServerProfileUpdateSuccess)(nil),
		0xe4b9b1cab57e8988: (*LobbyStatusNotify)(nil),
		0xed5be2c3632155f1: (*GameSettings)(nil),
		0xf24185da0edef641: (*UpdateProfileFailure)(nil),
		0xf25491d001cef757: (*UpdateProfileSuccess)(nil),
		0xfb632e5a38ec8c61: (*LoggedInUserProfileFailure)(nil),
		0xfb763a5037fc8d77: (*LoggedInUserProfileSuccess)(nil),
		0xfb772a4221fc8d70: (*LoggedInUserProfileRequest)(nil),
		0xfcced6f169822bb8: (*DocumentRequest)(nil),
		0xff71856af7e0fbd9: (*LobbyEntrantsV0)(nil),
		0x080495a43a6b7251: (*SNSEarlyQuitConfig)(nil),
		0x1f81b54c35788eaa: (*SNSEarlyQuitUpdateNotification)(nil),
		0xd9a955895caccac3: (*SNSEarlyQuitFeatureFlags)(nil),
		// SNS Party messages — client requests
		0xb57b22cc5352e00c: (*SNSPartyJoinRequest)(nil),
		0xb77b0be7a94a9fb6: (*SNSPartyLeaveRequest)(nil),
		0xcf13f934540b5f5e: (*SNSPartySendInviteRequest)(nil),
		0xc2478aa479f3e16a: (*SNSPartyLockRequest)(nil),
		0x5a4e99802fa3d704: (*SNSPartyUnlockRequest)(nil),
		0xfaf57beb59917d64: (*SNSPartyKickRequest)(nil),
		0x518543cd886a6946: (*SNSPartyPassOwnershipRequest)(nil),
		0xe3654a09203555a3: (*SNSPartyRespondToInviteRequest)(nil),
		// SNS Party messages — server responses/notifications
		0xb57a32de4552e00b: (*SNSPartyJoinSuccess)(nil),
		0xb56f26d44a42e11d: (*SNSPartyJoinFailure)(nil),
		0xb77a1bf5bf4a9fb1: (*SNSPartyLeaveSuccess)(nil),
		0x05315abefc8f804b: (*SNSPartyLeaveNotify)(nil),
		0x28cb04891f93dc81: (*SNSPartyKickNotify)(nil),
		0x9d946c88d5a8aca5: (*SNSPartyPassNotify)(nil),
		0x93a6b1a6cd4ef8dd: (*SNSPartyLockNotify)(nil),
		0xd8cfd3795010481f: (*SNSPartyUnlockNotify)(nil),
		// SNS Party messages — additional requests
		0x0b7bd21332523994: (*SNSPartyCreateRequest)(nil),
		0xdee761a021a5278a: (*SNSPartyUpdateRequest)(nil),
		0x4edeeb8ddecc8736: (*SNSPartyUpdateMemberRequest)(nil),
		0xd8cbc44959e25da8: (*SNSPartyInviteListRefreshRequest)(nil),
		// SNS Party messages — additional responses/notifications
		0x0b7ac20124523993: (*SNSPartyCreateSuccess)(nil),
		0x0b6fd60b2b423885: (*SNSPartyCreateFailure)(nil),
		0xcc38103e64879e53: (*SNSPartyJoinNotify)(nil),
		0xb76f0fffb05a9ea7: (*SNSPartyLeaveFailure)(nil),
		0xfaf46bf94f917d63: (*SNSPartyKickSuccess)(nil),
		0xfae17ff340817c75: (*SNSPartyKickFailure)(nil),
		0x518453df9e6a6941: (*SNSPartyPassSuccess)(nil),
		0x519147d5917a6857: (*SNSPartyPassFailure)(nil),
		0xc2469ab66ff3e16d: (*SNSPartyLockSuccess)(nil),
		0xc2538ebc60e3e07b: (*SNSPartyLockFailure)(nil),
		0x5a4f899239a3d703: (*SNSPartyUnlockSuccess)(nil),
		0x5a5a9d9836b3d615: (*SNSPartyUnlockFailure)(nil),
		0x218f721f09026dab: (*SNSPartyInviteNotify)(nil),
		0x685a5fb8447b1155: (*SNSPartyInviteListResponse)(nil),
		0xdee671b237a5278d: (*SNSPartyUpdateSuccess)(nil),
		0xdef365b838b5269b: (*SNSPartyUpdateFailure)(nil),
		0x23c834cb3bc6ecf5: (*SNSPartyUpdateNotify)(nil),
		0x4edffb9fc8cc8731: (*SNSPartyUpdateMemberSuccess)(nil),
		0x4ecaef95c7dc8627: (*SNSPartyUpdateMemberFailure)(nil),
		0x451eb6ca40dde289: (*SNSPartyUpdateMemberNotify)(nil),
		// SNS Friends messages — client requests
		0xcdc02fd1dbee3aaa: (*SNSFriendListSubscribeRequest)(nil),
		0xdcfa94680e8d19fc: (*SNSFriendListRefreshRequest)(nil),
		0x7f0d7a28de3c6f70: (*SNSFriendInviteRequest)(nil),
		0x1bbcb7e810af4620: (*SNSFriendAcceptRequest)(nil),
		0x78908988b7fe6db4: (*SNSFriendRemoveRequest)(nil),
		// SNS Friends messages — server responses/notifications
		0xa78aeb2a4e89b10b: (*SNSFriendListResponse)(nil),
		0x26a19dc4d2d5579d: (*SNSFriendStatusNotify)(nil),
		0x7f0c6a3ac83c6f77: (*SNSFriendInviteSuccess)(nil),
		0x7f197e30c72c6e61: (*SNSFriendInviteFailure)(nil),
		0xca09b0b36bd981b7: (*SNSFriendInviteNotify)(nil),
		0x1bbda7fa06af4627: (*SNSFriendAcceptSuccess)(nil),
		0x1ba8b3f009bf4731: (*SNSFriendAcceptFailure)(nil),
		0xc237c84c31d3ae05: (*SNSFriendAcceptNotify)(nil),
		0xc2bf83a08ea3a955: (*SNSFriendRemoveResponse)(nil),
		0xe06972f49cd72265: (*SNSFriendRemoveNotify)(nil),
		0x191aa30801ec6d03: (*SNSFriendWithdrawnNotify)(nil),
		0xb9b86c0ce8e8d0c1: (*SNSFriendRejectNotify)(nil),
		// Custom messages
		0x9ee5107d9e29fd63: (*NEVRProtobufMessageV1)(nil),
		0xc6b3710cd9c4ef47: (*NEVRProtobufJSONMessageV1)(nil),
	}

	// Create a reverse lookup map for the symbol types.
	reverseSymbolTypes = make(map[string]uint64, len(SymbolTypes))
)

type Symbol uint64

func (s Symbol) HexString() string {
	return fmt.Sprintf("0x%016x", uint64(s))
}

// A symbol token is a symbol converted to a string.
// It either uses the cache to convert back to a string,
// or returns the hex string representation of the token.
// ToSymbol will detect 0x prefixed hex strings.
func (s Symbol) Token() SymbolToken {
	t, ok := SymbolCache[s]
	if !ok {
		// If it's not found, just return the number as a hex string
		t = SymbolToken(s.HexString())
	}
	return t
}

func (s Symbol) MarshalText() ([]byte, error) {
	v := s.Token().String()
	return []byte(v), nil
}

func (s *Symbol) UnmarshalText(data []byte) error {
	v := string(data)
	*s = ToSymbol(v)
	return nil
}

func (s Symbol) String() string {
	return s.Token().String()
}

func (s Symbol) IsNil() bool {
	return s == 0
}

func (s Symbol) Int64() int64 {
	// Convert from uint64 to int64 correctly handling overflow.
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, uint64(s))
	return int64(binary.LittleEndian.Uint64(b))
}

// A symbol token is a symbol converted to a string.
// It either uses the cache to convert back to a string,
// or returns the hex string representation of the token.
// ToSymbol will detect 0x prefixed hex strings.
type SymbolToken string

func (t SymbolToken) Symbol() Symbol {
	return ToSymbol(t)
}
func (t SymbolToken) String() string {
	return string(t)
}

// ToSymbol converts a string value to a symbol.
func ToSymbol(v any) Symbol {
	// if it's a number, return it as an uint64
	switch t := v.(type) {
	case Symbol:
		return t
	case int:
		return Symbol(t)
	case int64:
		return Symbol(t)
	case uint64:
		return Symbol(t)
	case SymbolToken:
		return ToSymbol(string(t))
	case []uint8:
		return ToSymbol(string(t))
	case string:
		str := t
		// Empty string returns 0
		if len(str) == 0 {
			return Symbol(0)
		}
		// if it's a hex represenatation, return it's value
		if len(str) == 18 && str[:2] == "0x" {
			if s, err := strconv.ParseUint(string(str[2:]), 16, 64); err == nil {
				return Symbol(s)
			}
		}
		return Symbol(CalculateSymbolValue(str, 0xFFFFFFFFFFFFFFFF, hashLookupArray, 0))
	default:
		return Symbol(0)
	}
}

// Message is a Evr message that can be sent over the network.
type Message interface {
	Stream(s *EasyStream) error
}

// Marshal returns the wire-format encoding of multiple messages.
func Marshal(msgs ...Message) ([]byte, error) {
	var errs error
	b := make([]byte, 0)
	for _, m := range msgs {
		// Encode the message.
		s := NewEasyStream(EncodeMode, []byte{})
		if err := m.Stream(s); err != nil {
			errs = errors.Join(fmt.Errorf("could not stream message:%s", err), errs)
			continue
		}
		// Write the message type symbol.
		typ := reflect.TypeOf(m).String()
		sym, ok := reverseSymbolTypes[typ]
		if !ok {
			errs = errors.Join(ErrSymbolNotFound, fmt.Errorf("message type %T", m), errs)
			continue
		}
		// Write the Header (Marker + Symbol + Data Length)
		b = append(b, MessageMarker...)
		b = appendUint64(b, uint64(sym))
		b = appendUint64(b, uint64(s.Len()))
		// Write the message data.
		b = append(b, s.Bytes()...)
	}
	return b, errs
}

func WrapBytes(symbol Symbol, data []byte) ([]byte, error) {
	b := make([]byte, 0)

	// Write the Header (Marker + Symbol + Data Length)
	b = append(b, MessageMarker...)
	b = appendUint64(b, uint64(symbol))
	b = appendUint64(b, uint64(len(data)))
	// Write the message data.
	b = append(b, data...)
	return b, nil
}

// SplitPacket splits the packet into individual messages.
func SplitPacket(data []byte) [][]byte {
	return bytes.Split(data, MessageMarker)
}

var ignoredSymbols = []uint64{
	0x80119c19ac72d695,
}

// ParsePacket parses the wire-format packet in data and places the result in m.
// The provided message must be mutable (e.g., a non-nil pointer to a slice).
func ParsePacket(data []byte) ([]Message, error) {
	var err error

	// Enforce packet size limit before any allocation.
	if len(data) > MaxPacketLength {
		return nil, fmt.Errorf("%w: packet too large (%d bytes, max %d)", ErrInvalidPacket, len(data), MaxPacketLength)
	}

	// Split the packet into individual messages.
	chunks := bytes.Split(data, MessageMarker)
	if len(chunks) > MaxMessagesPerPacket {
		return nil, fmt.Errorf("%w: too many messages in packet (%d, max %d)", ErrInvalidPacket, len(chunks), MaxMessagesPerPacket)
	}

	messages := make([]Message, 0, len(chunks))

	for _, b := range chunks {
		if len(b) == 0 {
			// Skip empty messages.
			continue
		}
		buf := bytes.NewBuffer(b)
		// Verify packet length.
		if buf.Len() < 16 {
			err = errors.Join(err, ErrInvalidPacket, fmt.Errorf("packet too short (%d bytes)", buf.Len()))
			break
		}
		// Read the message type and data length.
		sym := dUint64(buf.Next(8))

		// Ignore specific messages
		if slices.Contains(ignoredSymbols, sym) {
			continue
		}

		rawLen := dUint64(buf.Next(8))
		if rawLen > uint64(MaxMessageLength) {
			err = errors.Join(err, ErrInvalidPacket, fmt.Errorf("message too large (%d bytes, max %d)", rawLen, MaxMessageLength))
			break
		}
		l := int(rawLen)
		// Enforce per-message size limit before allocating the message struct.
		if l > MaxMessageLength {
			err = errors.Join(err, ErrInvalidPacket, fmt.Errorf("message too large (%d bytes, max %d)", l, MaxMessageLength))
			break
		}
		// Verify the message data can be read from the rest of the packet.
		if buf.Len() != l {
			err = errors.Join(err, ErrInvalidPacket, fmt.Errorf("truncated packet (expected %d bytes, got %d)", l, buf.Len()))
			break
		}
		// Read the payload.
		b = buf.Next(l)

		// Unmarshal the message.
		typ, ok := SymbolTypes[sym]
		if !ok || typ == nil {
			err = errors.Join(err, ErrSymbolNotFound, fmt.Errorf("unknown symbol: 0x%016x", sym))
			break
		}

		// Create a new message of the correct type and unmarshal the data into it.
		newVal := reflect.New(reflect.TypeOf(typ).Elem()).Interface()
		message, ok := newVal.(Message)
		if !ok {
			err = errors.Join(err, fmt.Errorf("registered type %T does not implement Message", newVal))
			break
		}
		if streamErr := message.Stream(NewEasyStream(DecodeMode, b)); streamErr != nil {
			return nil, fmt.Errorf("Stream error: %T: %w", typ, streamErr)
		}
		messages = append(messages, message)
	}
	return messages, err
}

// AppendUint64 appends the (little-endian) byte representation of v to b and returns the resulting slice.
func appendUint64(b []byte, v uint64) []byte {
	return append(b,
		byte(v),
		byte(v>>8),
		byte(v>>16),
		byte(v>>24),
		byte(v>>32),
		byte(v>>40),
		byte(v>>48),
		byte(v>>56),
	)
}

// Uint64 decodes a little-endian uint64 from the provided byte slice.
func dUint64(b []byte) uint64 {
	_ = b[7] // bounds check hint to compiler; see golang.org/issue/14808
	return uint64(b[0]) |
		uint64(b[1])<<8 |
		uint64(b[2])<<16 |
		uint64(b[3])<<24 |
		uint64(b[4])<<32 |
		uint64(b[5])<<40 |
		uint64(b[6])<<48 |
		uint64(b[7])<<56
}

func init() {
	// Populate the new map
	for key, value := range SymbolTypes {
		typeName := reflect.TypeOf(value).String()
		reverseSymbolTypes[typeName] = key
	}
}

// SymbolOf returns the type symbol of the message.
func SymbolOf(m Message) Symbol {
	typ := reflect.TypeOf(m).String()
	sym, ok := reverseSymbolTypes[typ]
	if !ok {
		return Symbol(0)
	}
	return Symbol(sym)
}

// MessageTypeOf returns a new instance of the message type.
func MessageTypeOf(s Symbol) Message {
	if m, ok := SymbolTypes[uint64(s)]; ok {
		newVal := reflect.New(reflect.TypeOf(m).Elem()).Interface()
		if msg, ok := newVal.(Message); ok {
			return msg
		}
	}
	return nil
}
