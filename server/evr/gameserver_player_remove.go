package evr

import (
	"fmt"

	"github.com/gofrs/uuid/v5"
)

// GameServerPlayerRemoved is a message from broadcaster to nakama, indicating a player was removed by the game server.
// NOTE: This is an unofficial message created for Echo Relay.
type GameServerPlayerRemoved struct {
	EntrantID uuid.UUID
}

func (m *GameServerPlayerRemoved) Symbol() Symbol {
	return SymbolOf(m)
}
func (m GameServerPlayerRemoved) Token() string {
	return "ERGameServerPlayerRemove"
}

// NewERGameServerRemovePlayer initializes a new ERGameServerRemovePlayer message.
func NewBroadcasterRemovePlayer(sid uuid.UUID) *GameServerPlayerRemoved {
	return &GameServerPlayerRemoved{
		EntrantID: sid,
	}
}

func (m *GameServerPlayerRemoved) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGUID(&m.EntrantID) },
	})
}
func (m *GameServerPlayerRemoved) String() string {

	return fmt.Sprintf("BroadcasterPlayerRemoved(player_session=%s)", m.EntrantID.String())
}
