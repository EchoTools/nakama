package evr

import (
	"fmt"
	"strings"

	"github.com/gofrs/uuid/v5"
)

// Game Server -> Nakama: player sessions that have been accepted.
type BroadcasterPlayersAccepted struct {
	Unk0           byte
	PlayerSessions []uuid.UUID
}

func (m BroadcasterPlayersAccepted) Symbol() Symbol { return 0x7777777777770600 }
func (m BroadcasterPlayersAccepted) Token() string  { return "ERGameServerPlayersAccepted" }

func NewBroadcasterPlayersAccepted(playerSessions ...uuid.UUID) *BroadcasterPlayersAccepted {
	return &BroadcasterPlayersAccepted{Unk0: 0, PlayerSessions: playerSessions}
}

func (m *BroadcasterPlayersAccepted) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamByte(&m.Unk0) },
		func() error {
			if s.Mode == DecodeMode {
				m.PlayerSessions = make([]uuid.UUID, s.Len()/16)
			}
			return s.StreamGuids(&m.PlayerSessions)
		},
	})
}

func (m *BroadcasterPlayersAccepted) String() string {
	sessions := make([]string, len(m.PlayerSessions))
	for i, session := range m.PlayerSessions {
		sessions[i] = session.String()
	}
	return fmt.Sprintf("%s(player_sessions=[%s])", m.Token(), strings.Join(sessions, ", "))
}
