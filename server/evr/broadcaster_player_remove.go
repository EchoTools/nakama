package evr

import (
	"fmt"

	"github.com/gofrs/uuid/v5"
)

// BroadcasterPlayerRemoved is a message from broadcaster to nakama, indicating a player was removed by the game server.
// NOTE: This is an unofficial message created for Echo Relay.
type BroadcasterPlayerRemoved struct {
	PlayerSession uuid.UUID
}

func (m *BroadcasterPlayerRemoved) Symbol() Symbol {
	return SymbolOf(m)
}
func (m BroadcasterPlayerRemoved) Token() string {
	return "ERGameServerPlayerRemove"
}

// NewERGameServerRemovePlayer initializes a new ERGameServerRemovePlayer message.
func NewBroadcasterRemovePlayer(sid uuid.UUID) *BroadcasterPlayerRemoved {
	return &BroadcasterPlayerRemoved{
		PlayerSession: sid,
	}
}

func (m *BroadcasterPlayerRemoved) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGuid(&m.PlayerSession) },
	})
}
func (m *BroadcasterPlayerRemoved) String() string {

	return fmt.Sprintf("%s(session=%s)", m.Token(), m.PlayerSession.String())
}
