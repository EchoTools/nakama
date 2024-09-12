package evr

import (
	"encoding/binary"
	"fmt"
	"net"
)

const (
	VersionLockPreFarewell uint64 = 0xC62F01D78F77910D
)

// BroadcasterRegistrationRequest is a message from game server to server, requesting game server registration so clients can match with it.
// NOTE: This is an unofficial message created for Echo Relay.
type BroadcasterRegistrationRequest struct {
	ServerId    uint64
	InternalIP  net.IP
	Port        uint16
	Region      Symbol
	VersionLock uint64
}

func (m BroadcasterRegistrationRequest) Symbol() Symbol {
	return 0x7777777777777777
}
func (m BroadcasterRegistrationRequest) Token() string {
	return "ERGameServerRegistrationRequest"
}

func NewBroadcasterRegistrationRequest(serverId uint64, internalAddress net.IP, port uint16, regionSymbol Symbol, versionLock uint64) *BroadcasterRegistrationRequest {
	return &BroadcasterRegistrationRequest{
		ServerId:    serverId,
		InternalIP:  internalAddress,
		Port:        port,
		Region:      regionSymbol,
		VersionLock: versionLock,
	}
}

func (m *BroadcasterRegistrationRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{

		func() error { return s.StreamNumber(binary.LittleEndian, &m.ServerId) },
		func() error { return s.StreamIpAddress(&m.InternalIP) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Port) },
		func() error {
			pad10 := make([]byte, 10) // Pad to 16 bytes
			for i := range pad10 {
				pad10[i] = 0xcc
			}
			return s.StreamBytes(&pad10, 10)
		},
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Region) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.VersionLock) },
	})
}
func (m BroadcasterRegistrationRequest) String() string {
	return fmt.Sprintf("%s(server_id=%d, internal_ip=%s, port=%d, region=%d, version_lock=%d)",
		m.Token(),
		m.ServerId,
		m.InternalIP,
		m.Port,
		m.Region,
		m.VersionLock,
	)
}
