package evr

import (
	"fmt"

	"github.com/gofrs/uuid/v5"
)

// LobbyPendingSessionCancel represents a message from client to the server, indicating intent to cancel pending matchmaker operations.
type LobbyPendingSessionCancel struct {
	Session uuid.UUID // The user's session token.
}

func (m *LobbyPendingSessionCancel) Token() string {
	return "SNSLobbyPendingSessionCancelv2"
}

func (m *LobbyPendingSessionCancel) Symbol() Symbol {
	return SymbolOf(m)
}

// ToString returns a string representation of the LobbyPendingSessionCancel message.
func (m *LobbyPendingSessionCancel) String() string {
	return fmt.Sprintf("%s(session=%v)", m.Token(), m.Session)
}

// NewLobbyPendingSessionCancelWithSession initializes a new LobbyPendingSessionCancel message with the provided session token.
func NewLobbyPendingSessionCancel(session uuid.UUID) *LobbyPendingSessionCancel {
	return &LobbyPendingSessionCancel{
		Session: session,
	}
}

func (m *LobbyPendingSessionCancel) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGuid(&m.Session) },
	})
}

func (m *LobbyPendingSessionCancel) GetSessionID() uuid.UUID {
	return m.Session
}
