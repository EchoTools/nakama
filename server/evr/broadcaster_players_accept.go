package evr

import (
	"fmt"
	"strings"

	"github.com/gofrs/uuid/v5"
)

// Game Server -> Nakama: player sessions that the server intends to accept.

type BroadcasterPlayersAccept struct {
	PlayerSessions []uuid.UUID
}

func (m BroadcasterPlayersAccept) Symbol() Symbol {
	return Symbol(0x7777777777770500)
}
func (m BroadcasterPlayersAccept) Token() string {
	return "ERGameServerAcceptPlayers"
}

// NewERGameServerAcceptPlayersWithSessions initializes a new ERGameServerAcceptPlayers with the provided arguments.
func NewBroadcasterPlayersAccept(playerSessions []uuid.UUID) *BroadcasterPlayersAccept {
	return &BroadcasterPlayersAccept{
		PlayerSessions: playerSessions,
	}
}

func (m *BroadcasterPlayersAccept) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error {
			if s.Mode == DecodeMode {
				m.PlayerSessions = make([]uuid.UUID, s.Len()/16)
			}
			return s.StreamGuids(&m.PlayerSessions)
		},
	})
}

func (m *BroadcasterPlayersAccept) String() string {
	sessions := make([]string, len(m.PlayerSessions))
	for i, session := range m.PlayerSessions {
		sessions[i] = session.String()
	}
	return fmt.Sprintf("%s(player_sessions=[%s])", m.Token(), strings.Join(sessions, ", "))
}
