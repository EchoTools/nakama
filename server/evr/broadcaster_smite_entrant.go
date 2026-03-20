package evr

import (
	"encoding/binary"
	"fmt"
)

// SNSLobbySmiteEntrant is a server-to-client message that removes ("smites")
// a player from a lobby session. Used to enforce early quit penalties by
// kicking penalized players from matchmaking lobbies.
//
// Wire layout (0x10 bytes):
//   +0x00  uint64  PlayerID   (player to remove from lobby)
//   +0x08  uint64  Reserved   (padding/reserved)
//
// Handler flags: HOST_ONLY (0x00000004) — only the lobby host processes this.
type SNSLobbySmiteEntrant struct {
	PlayerID uint64
	Reserved uint64
}

func (m *SNSLobbySmiteEntrant) Token() string {
	return "SNSLobbySmiteEntrant"
}

func (m *SNSLobbySmiteEntrant) Symbol() Symbol {
	return ToSymbol(m.Token())
}

func (m *SNSLobbySmiteEntrant) String() string {
	return fmt.Sprintf("%s(player_id=0x%x)", m.Token(), m.PlayerID)
}

func (m *SNSLobbySmiteEntrant) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.PlayerID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Reserved) },
	})
}

// NewSNSLobbySmiteEntrant creates a smite message for the given player.
func NewSNSLobbySmiteEntrant(playerID uint64) *SNSLobbySmiteEntrant {
	return &SNSLobbySmiteEntrant{
		PlayerID: playerID,
	}
}
