package evr

import (
	"encoding/binary"
	"fmt"
)

// LobbyMatchmakerStatus is a message from server to the client, providing the status of a previously sent
// LobbyMatchmakerStatusRequest.
type LobbyMatchmakerStatus struct {
	StatusCode uint32
}

func (m *LobbyMatchmakerStatus) Token() string {
	return "SNSLobbyMatchmakerStatus"
}

func (m *LobbyMatchmakerStatus) Symbol() Symbol {
	return SymbolOf(m)
}

// Stream streams the message data in/out based on the streaming mode set.
func (m *LobbyMatchmakerStatus) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.StatusCode) },
	})
}

func (m *LobbyMatchmakerStatus) String() string {
	return fmt.Sprintf("%s(unused=%d)",
		m.Token(),
		m.StatusCode,
	)
}

// NewLobbyMatchmakerStatusResponse initializes a new LobbyMatchmakerStatus message.
func NewLobbyMatchmakerStatusResponse() *LobbyMatchmakerStatus {
	return &LobbyMatchmakerStatus{
		StatusCode: 0,
	}
}
