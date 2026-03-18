package evr

import (
	"encoding/binary"
	"fmt"

	"github.com/gofrs/uuid/v5"
)

const PacketKeySize = 0x20

// LobbySessionSuccess represents a message from server to client indicating that a request to create/join/find a game server session succeeded.
type LobbySessionSuccess struct {
	GameMode           Symbol
	LobbyID            uuid.UUID
	GroupID            uuid.UUID // V5 only
	Endpoint           Endpoint
	TeamIndex          int16
	SessionFlags       uint8
	ServerEncoderFlags PacketEncoderConfig
	ClientEncoderFlags PacketEncoderConfig
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
func NewLobbySessionSuccessv5(gameTypeSymbol Symbol, matchingSession uuid.UUID, channelUUID uuid.UUID, endpoint Endpoint, role int16, _ bool, _ bool) *LobbySessionSuccess {

	clientConfig := DefaultClientPacketConfig()
	serverConfig := DefaultServerPacketConfig()
	cryptoMaterial := NewLobbySessionCryptoMaterial(serverConfig, clientConfig)
	return &LobbySessionSuccess{
		GameMode:           gameTypeSymbol,
		LobbyID:            matchingSession,
		GroupID:            channelUUID,
		Endpoint:           endpoint,
		TeamIndex:          role,
		ServerEncoderFlags: serverConfig,
		ClientEncoderFlags: clientConfig,
		ServerSequenceId:   cryptoMaterial.ServerSequenceId,
		ServerMacKey:       cryptoMaterial.ServerMacKey,
		ServerEncKey:       cryptoMaterial.ServerEncKey,
		ServerRandomKey:    cryptoMaterial.ServerRandomKey,
		ClientSequenceId:   cryptoMaterial.ClientSequenceId,
		ClientMacKey:       cryptoMaterial.ClientMacKey,
		ClientEncKey:       cryptoMaterial.ClientEncKey,
		ClientRandomKey:    cryptoMaterial.ClientRandomKey,
	}
}

func (m LobbySessionSuccess) Version5() *LobbySessionSuccessv5 {
	s := LobbySessionSuccessv5(m)
	return &s
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
	encoderConfig0 := m.ServerEncoderFlags.ToFlags()
	encoderConfig1 := m.ClientEncoderFlags.ToFlags()

	err := RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.GameMode) },
		func() error { return s.StreamGUID(&m.LobbyID) },
		func() error { return s.StreamGUID(&m.GroupID) },
		func() error { return s.StreamStruct(&m.Endpoint) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.TeamIndex) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.SessionFlags) },
		func() error { return s.Skip(3) }, // Padding
		func() error { return s.StreamNumber(binary.LittleEndian, &encoderConfig0) },
		func() error { return s.StreamNumber(binary.LittleEndian, &encoderConfig1) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.ServerSequenceId) },
		func() error { return s.StreamBytes(&m.ServerMacKey, int(m.ServerEncoderFlags.MacKeySize)) },
		func() error { return s.StreamBytes(&m.ServerEncKey, int(m.ServerEncoderFlags.EncryptionKeySize)) },
		func() error { return s.StreamBytes(&m.ServerRandomKey, int(m.ServerEncoderFlags.RandomKeySize)) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.ClientSequenceId) },
		func() error { return s.StreamBytes(&m.ClientMacKey, int(m.ClientEncoderFlags.MacKeySize)) },
		func() error { return s.StreamBytes(&m.ClientEncKey, int(m.ClientEncoderFlags.EncryptionKeySize)) },
		func() error { return s.StreamBytes(&m.ClientRandomKey, int(m.ClientEncoderFlags.RandomKeySize)) },
	})
	if err != nil {
		return err
	}

	m.ServerEncoderFlags = PacketEncoderConfigFromFlags(encoderConfig0)
	m.ClientEncoderFlags = PacketEncoderConfigFromFlags(encoderConfig1)
	return nil
}

type PacketEncoderConfig struct {
	EncryptionEnabled   bool
	MacEnabled          bool
	MacDigestSize       uint16
	MacPbkdf2Iterations uint16
	MacKeySize          uint16
	EncryptionKeySize   uint16
	RandomKeySize       uint16
}

func DefaultClientPacketConfig() PacketEncoderConfig {
	return PacketEncoderConfig{
		EncryptionEnabled:   true,
		MacEnabled:          true,
		MacDigestSize:       0x40,
		MacPbkdf2Iterations: 0x00,
		MacKeySize:          0x20,
		EncryptionKeySize:   0x20,
		RandomKeySize:       0x20,
	}
}

func DefaultServerPacketConfig() PacketEncoderConfig {
	return PacketEncoderConfig{
		EncryptionEnabled:   true,
		MacEnabled:          true,
		MacDigestSize:       0x20,
		MacPbkdf2Iterations: 0x00,
		MacKeySize:          0x20,
		EncryptionKeySize:   0x20,
		RandomKeySize:       0x20,
	}
}

func PacketEncoderConfigFromFlags(flags uint64) PacketEncoderConfig {
	return PacketEncoderConfig{
		EncryptionEnabled:   flags&1 != 0,
		MacEnabled:          (flags>>1)&1 != 0,
		MacDigestSize:       uint16((flags >> 2) & 0x0fff),
		MacPbkdf2Iterations: uint16((flags >> 14) & 0x0fff),
		MacKeySize:          uint16((flags >> 26) & 0x0fff),
		EncryptionKeySize:   uint16((flags >> 38) & 0x0fff),
		RandomKeySize:       uint16((flags >> 50) & 0x0fff),
	}
}

func (p PacketEncoderConfig) ToFlags() uint64 {
	flags := uint64(0)
	if p.EncryptionEnabled {
		flags |= 1
	}
	if p.MacEnabled {
		flags |= 1 << 1
	}
	flags |= uint64(p.MacDigestSize&0x0fff) << 2
	flags |= uint64(p.MacPbkdf2Iterations&0x0fff) << 14
	flags |= uint64(p.MacKeySize&0x0fff) << 26
	flags |= uint64(p.EncryptionKeySize&0x0fff) << 38
	flags |= uint64(p.RandomKeySize&0x0fff) << 50
	return flags
}

type LobbySessionCryptoMaterial struct {
	ServerSequenceId uint64
	ServerMacKey     []byte
	ServerEncKey     []byte
	ServerRandomKey  []byte
	ClientSequenceId uint64
	ClientMacKey     []byte
	ClientEncKey     []byte
	ClientRandomKey  []byte
}

func NewLobbySessionCryptoMaterial(serverConfig, clientConfig PacketEncoderConfig) LobbySessionCryptoMaterial {
	serverMacKeySize := int(serverConfig.MacKeySize)
	serverEncKeySize := int(serverConfig.EncryptionKeySize)
	serverRandomKeySize := int(serverConfig.RandomKeySize)
	clientMacKeySize := int(clientConfig.MacKeySize)
	clientEncKeySize := int(clientConfig.EncryptionKeySize)
	clientRandomKeySize := int(clientConfig.RandomKeySize)

	if serverMacKeySize <= 0 {
		serverMacKeySize = PacketKeySize
	}
	if serverEncKeySize <= 0 {
		serverEncKeySize = PacketKeySize
	}
	if serverRandomKeySize <= 0 {
		serverRandomKeySize = PacketKeySize
	}
	if clientMacKeySize <= 0 {
		clientMacKeySize = PacketKeySize
	}
	if clientEncKeySize <= 0 {
		clientEncKeySize = PacketKeySize
	}
	if clientRandomKeySize <= 0 {
		clientRandomKeySize = PacketKeySize
	}

	return LobbySessionCryptoMaterial{
		ServerSequenceId: binary.LittleEndian.Uint64(GetRandomBytes(0x08)),
		ServerMacKey:     GetRandomBytes(serverMacKeySize),
		ServerEncKey:     GetRandomBytes(serverEncKeySize),
		ServerRandomKey:  GetRandomBytes(serverRandomKeySize),
		ClientSequenceId: binary.LittleEndian.Uint64(GetRandomBytes(0x08)),
		ClientMacKey:     GetRandomBytes(clientMacKeySize),
		ClientEncKey:     GetRandomBytes(clientEncKeySize),
		ClientRandomKey:  GetRandomBytes(clientRandomKeySize),
	}
}
