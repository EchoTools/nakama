package evr

import (
	"encoding/binary"
	"fmt"

	"github.com/gofrs/uuid/v5"
)

const (
	HeadsetTypePCVR       = 0
	HeadsetTypeStandalone = 1
)

// LobbySessionSuccess represents a message from server to client indicating that a request to create/join/find a game server session succeeded.
type LobbySessionSuccess struct {
	GameMode           Symbol
	LobbyID            uuid.UUID
	GroupID            uuid.UUID // V5 only
	Endpoint           Endpoint
	TeamIndex          int16
	Unk1               uint32
	HeadsetType        int
	ServerEncoderFlags uint64
	ClientEncoderFlags uint64
	ServerSequenceId   uint64
	ServerMacKey       []byte
	ServerEncKey       []byte
	ServerRandomKey    []byte
	ClientSequenceId   uint64
	ClientMacKey       []byte
	ClientEncKey       []byte
	ClientRandomKey    []byte
}

// NewLobbySessionSuccessv5 initializes a new LobbySessionSuccessv5 message.
func NewLobbySessionSuccess(gameTypeSymbol Symbol, matchingSession uuid.UUID, channelUUID uuid.UUID, endpoint Endpoint, role int16, isPCVR bool, disableEncryption bool, disableMAC bool) *LobbySessionSuccess {

	clientSettings := &PacketEncoderSettings{
		isPCVR:                  isPCVR,
		EncryptionEnabled:       !disableEncryption,
		MACEnabled:              !disableMAC,
		MACDigestSize:           0x20,
		MACPBKDF2IterationCount: 0x00,
		MACKeySize:              0x20,
		EncryptionKeySize:       0x20,
		RandomKeySize:           0x20,
	}
	serverSettings := &PacketEncoderSettings{
		isPCVR:                  isPCVR,
		EncryptionEnabled:       !disableEncryption,
		MACEnabled:              !disableMAC,
		MACDigestSize:           0x20,
		MACPBKDF2IterationCount: 0x00,
		MACKeySize:              0x20,
		EncryptionKeySize:       0x20,
		RandomKeySize:           0x20,
	}
	return &LobbySessionSuccess{
		GameMode:           gameTypeSymbol,
		LobbyID:            matchingSession,
		GroupID:            channelUUID,
		Endpoint:           endpoint,
		TeamIndex:          role,
		Unk1:               0,
		ServerEncoderFlags: serverSettings.ToFlags(),
		ClientEncoderFlags: clientSettings.ToFlags(),
		ServerSequenceId:   binary.LittleEndian.Uint64(GetRandomBytes(0x08)),
		ServerMacKey:       GetRandomBytes(serverSettings.MACKeySize),
		ServerEncKey:       GetRandomBytes(serverSettings.EncryptionKeySize),
		ServerRandomKey:    GetRandomBytes(serverSettings.RandomKeySize),
		ClientSequenceId:   binary.LittleEndian.Uint64(GetRandomBytes(0x08)),
		ClientMacKey:       GetRandomBytes(clientSettings.MACKeySize),
		ClientEncKey:       GetRandomBytes(clientSettings.EncryptionKeySize),
		ClientRandomKey:    GetRandomBytes(clientSettings.RandomKeySize),
	}
}

func (m LobbySessionSuccess) Version4() *LobbySessionSuccessv4 {
	s := LobbySessionSuccessv4(m)
	return &s
}

func (m LobbySessionSuccess) Version5() *LobbySessionSuccessv5 {
	s := LobbySessionSuccessv5(m)
	return &s
}

type LobbySessionSuccessv4 LobbySessionSuccess // LobbSessionSuccessv4 is v5 without the channel UUID.

func (m LobbySessionSuccessv4) Token() string {
	return "SNSLobbySessionSuccessv4"
}

func (m *LobbySessionSuccessv4) Symbol() Symbol {
	return SymbolOf(m)
}

// ToString returns a string representation of the LobbySessionSuccessv5 message.
func (m *LobbySessionSuccessv4) String() string {
	return fmt.Sprintf("%s(game_type=%d, matching_session=%s, endpoint=%v, team_index=%d)",
		m.Token(),
		m.GameMode,
		m.LobbyID,
		m.Endpoint,
		m.TeamIndex,
	)
}

func (m *LobbySessionSuccessv4) Stream(s *EasyStream) error {
	var se *PacketEncoderSettings
	var ce *PacketEncoderSettings

	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.GameMode) },
		func() error { return s.StreamGUID(&m.LobbyID) },
		func() error { return s.StreamStruct(&m.Endpoint) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.TeamIndex) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unk1) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.ServerEncoderFlags) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.ClientEncoderFlags) },
		func() error { se = PacketEncoderSettingsFromFlags(m.ServerEncoderFlags); return nil },
		func() error { ce = PacketEncoderSettingsFromFlags(m.ClientEncoderFlags); return nil },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.ServerSequenceId) },
		func() error { return s.StreamBytes(&m.ServerMacKey, se.MACKeySize) },
		func() error { return s.StreamBytes(&m.ServerEncKey, se.EncryptionKeySize) },
		func() error { return s.StreamBytes(&m.ServerRandomKey, se.RandomKeySize) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.ClientSequenceId) },
		func() error { return s.StreamBytes(&m.ClientMacKey, ce.MACKeySize) },
		func() error { return s.StreamBytes(&m.ClientEncKey, ce.EncryptionKeySize) },
		func() error { return s.StreamBytes(&m.ClientRandomKey, ce.RandomKeySize) },
	})
}

type LobbySessionSuccessv5 LobbySessionSuccess

func (m LobbySessionSuccessv5) Token() string {
	return "SNSLobbySessionSuccessv5"
}

func (m *LobbySessionSuccessv5) Symbol() Symbol {
	return SymbolOf(m)
}

// ToString returns a string representation of the LobbySessionSuccessv5 message.
func (m *LobbySessionSuccessv5) String() string {
	return fmt.Sprintf("%s(game_type=%d, matching_session=%s, channel=%s, endpoint=%v, team_index=%d)",
		m.Token(),
		m.GameMode,
		m.LobbyID,
		m.GroupID,
		m.Endpoint,
		m.TeamIndex,
	)
}
func (m *LobbySessionSuccessv5) Stream(s *EasyStream) error {
	var se *PacketEncoderSettings
	var ce *PacketEncoderSettings
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.GameMode) },
		func() error { return s.StreamGUID(&m.LobbyID) },
		func() error { return s.StreamGUID(&m.GroupID) },
		func() error { return s.StreamStruct(&m.Endpoint) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.TeamIndex) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unk1) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.ServerEncoderFlags) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.ClientEncoderFlags) },
		func() error { se = PacketEncoderSettingsFromFlags(m.ServerEncoderFlags); return nil },
		func() error { ce = PacketEncoderSettingsFromFlags(m.ClientEncoderFlags); return nil },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.ServerSequenceId) },
		func() error { return s.StreamBytes(&m.ServerMacKey, se.MACKeySize) },
		func() error { return s.StreamBytes(&m.ServerEncKey, se.EncryptionKeySize) },
		func() error { return s.StreamBytes(&m.ServerRandomKey, se.RandomKeySize) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.ClientSequenceId) },
		func() error { return s.StreamBytes(&m.ClientMacKey, ce.MACKeySize) },
		func() error { return s.StreamBytes(&m.ClientEncKey, ce.EncryptionKeySize) },
		func() error { return s.StreamBytes(&m.ClientRandomKey, ce.RandomKeySize) },
	})
}

func DefaultClientEncoderSettings() *PacketEncoderSettings {
	return &PacketEncoderSettings{
		EncryptionEnabled:       true,
		MACEnabled:              true,
		MACDigestSize:           0x40,
		MACPBKDF2IterationCount: 0x00,
		MACKeySize:              0x20,
		EncryptionKeySize:       0x20,
		RandomKeySize:           0x20,
	}
}
func DefaultServerEncoderSettings() *PacketEncoderSettings {
	return &PacketEncoderSettings{
		EncryptionEnabled:       true,
		MACEnabled:              true,
		MACDigestSize:           0x20,
		MACPBKDF2IterationCount: 0x00,
		MACKeySize:              0x20,
		EncryptionKeySize:       0x20,
		RandomKeySize:           0x20,
	}
}

// PacketEncoderSettings describes packet encoding settings for one party in a game server <-> client connection.
type PacketEncoderSettings struct {
	isPCVR                  bool // The headset type of the client. (0: PCVR, 1: Standalone)
	EncryptionEnabled       bool // Indicates whether encryption should be used for each packet.
	MACEnabled              bool // Indicates whether MACs should be attached to each packet.
	MACDigestSize           int  // The byte size (<= 512bit) of the MAC output packets should use. (cut from the front of the HMAC-SHA512)
	MACPBKDF2IterationCount int  // The iteration count for PBKDF2 HMAC-SHA512.
	MACKeySize              int  // The byte size of the HMAC-SHA512 key.
	EncryptionKeySize       int  // The byte size of the AES-CBC key. (default: 32/AES-256-CBC)
	RandomKeySize           int  // The byte size of the random key for the RNG.
}

// NOTE on Keysize:
// RandomKeySize represents the byte size of the random key used by the RNG to seed itself in the packet encoding process.
// The Keccak-F permutation (1600-bit) is used as a random number generator.
// Both parties exchange their packet encoding settings.
// Each packet is encrypted/decrypted using the party's encryption key.
// The 16-byte initialization vector (IV) is generated by the RNG for each step in the sequence ID.

// NewPacketEncoderSettings creates a new PacketEncoderSettings with the provided values.
func NewPacketEncoderSettings(encryptionEnabled, macEnabled bool, macDigestSize, macPBKDF2IterationCount, macKeySize, encryptionKeySize, randomKeySize int) *PacketEncoderSettings {
	return &PacketEncoderSettings{
		EncryptionEnabled:       encryptionEnabled,
		MACEnabled:              macEnabled,
		MACDigestSize:           macDigestSize,
		MACPBKDF2IterationCount: macPBKDF2IterationCount,
		MACKeySize:              macKeySize,
		EncryptionKeySize:       encryptionKeySize,
		RandomKeySize:           randomKeySize,
	}
}

func PacketEncoderSettingsFromFlags(flags uint64) *PacketEncoderSettings {
	return &PacketEncoderSettings{
		EncryptionEnabled:       flags&1 != 0,
		MACEnabled:              flags&2 != 0,
		MACDigestSize:           int((flags >> 2) & 0xFFF),
		MACPBKDF2IterationCount: int((flags >> 14) & 0xFFF),
		MACKeySize:              int((flags >> 26) & 0xFFF),
		EncryptionKeySize:       int((flags >> 38) & 0xFFF),
		RandomKeySize:           int((flags >> 50) & 0xFFF),
	}
}

func (p *PacketEncoderSettings) ToFlags() uint64 {
	flags := uint64(0)
	if p.EncryptionEnabled {
		flags |= 1
	}
	if p.MACEnabled {
		flags |= 2
	}
	flags |= uint64(p.MACDigestSize&0xFFF) << 2
	flags |= uint64(p.MACPBKDF2IterationCount&0xFFF) << 14
	flags |= uint64(p.MACKeySize&0xFFF) << 26
	flags |= uint64(p.EncryptionKeySize&0xFFF) << 38
	flags |= uint64(p.RandomKeySize&0xFFF) << 50
	return flags
}
