package evr

import (
	"fmt"
	"strings"

	"github.com/gofrs/uuid/v5"
)

// Game Server -> Nakama: player sessions that have been accepted.
type GameServerJoinAllowed struct {
	EntrantIDs []uuid.UUID
}

func NewGameServerJoinAllowed(entrantIDs ...uuid.UUID) *GameServerJoinAllowed {
	return &GameServerJoinAllowed{EntrantIDs: entrantIDs}
}

func (m *GameServerJoinAllowed) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.Skip(1) },
		func() error {
			if s.Mode == DecodeMode {
				m.EntrantIDs = make([]uuid.UUID, s.Len()/16)
			}
			return s.StreamGuids(&m.EntrantIDs)
		},
	})
}

func (m *GameServerJoinAllowed) String() string {
	sessions := make([]string, len(m.EntrantIDs))
	for i, session := range m.EntrantIDs {
		sessions[i] = session.String()
	}
	return fmt.Sprintf("%T(entrant_ids=[%s])", m, strings.Join(sessions, ", "))
}
