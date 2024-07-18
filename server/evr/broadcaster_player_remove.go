package evr

import (
	"fmt"

	"github.com/gofrs/uuid/v5"
)

// BroadcasterPlayerRemoved is a message from broadcaster to nakama, indicating a player was removed by the game server.
// NOTE: This is an unofficial message created for Echo Relay.
type BroadcasterPlayerRemoved struct {
	EntrantID uuid.UUID
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
		EntrantID: sid,
	}
}

func (m *BroadcasterPlayerRemoved) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGuid(&m.EntrantID) },
	})
}
func (m *BroadcasterPlayerRemoved) String() string {

	return fmt.Sprintf("BroadcasterPlayerRemoved(player_session=%s)", m.EntrantID.String())
}
