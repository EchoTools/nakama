package evr

import (
	"encoding/binary"
	"fmt"
	"net"
)

// BroadcasterRegistrationSuccess is a message from server to game server, indicating a game server registration request had succeeded.
type BroadcasterRegistrationSuccess struct {
	ServerId        uint64
	ExternalAddress net.IP
	Unk0            uint64
}

func (m BroadcasterRegistrationSuccess) Symbol() Symbol {
	return SymbolOf(&m)
}

func (m *BroadcasterRegistrationSuccess) Token() string {
	return "SNSLobbyRegistrationSuccess"
}
func (m *BroadcasterRegistrationSuccess) String() string {
	return fmt.Sprintf("%s(server_id=%d, external_ip=%s)", m.Token(), m.ServerId, m.ExternalAddress)
}

// NewLobbyRegistrationSuccessWithArgs initializes a new LobbyRegistrationSuccess with the provided arguments.
func NewBroadcasterRegistrationSuccess(serverId uint64, externalAddr net.IP) *BroadcasterRegistrationSuccess {
	return &BroadcasterRegistrationSuccess{
		ServerId:        serverId,
		ExternalAddress: externalAddr,
		Unk0:            0,
	}
}

func (m *BroadcasterRegistrationSuccess) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.ServerId) },
		func() error { return s.StreamIpAddress(&m.ExternalAddress) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unk0) },
	})
}
