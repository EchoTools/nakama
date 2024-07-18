package evr

import (
	"fmt"
	"strings"

	"github.com/gofrs/uuid/v5"
)

// Game Server -> Nakama: player sessions that the server intends to accept.

type GameServerJoinAttempt struct {
	EntrantIDs []uuid.UUID
}

func NewGameServerJoinAttempt(entrantIDs []uuid.UUID) *GameServerJoinAttempt {
	return &GameServerJoinAttempt{
		EntrantIDs: entrantIDs,
	}
}

func (m *GameServerJoinAttempt) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error {
			if s.Mode == DecodeMode {
				m.EntrantIDs = make([]uuid.UUID, s.Len()/16)
			}
			return s.StreamGuids(&m.EntrantIDs)
		},
	})
}

func (m *GameServerJoinAttempt) String() string {
	sessions := make([]string, len(m.EntrantIDs))
	for i, session := range m.EntrantIDs {
		sessions[i] = session.String()
	}
	return fmt.Sprintf("%T(entrant_ids=[%s])", m, strings.Join(sessions, ", "))
}
