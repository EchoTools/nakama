package evr

import (
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/gofrs/uuid/v5"
	"github.com/samber/lo"
)

// LobbyPlayerSessionsRequest is a message from client to server, asking it to obtain game server sessions for a given list of user identifiers.
type LobbyPlayerSessionsRequest struct {
	LoginSessionID uuid.UUID
	EvrId          EvrId
	LobbySessionID uuid.UUID
	Platform       Symbol
	PlayerEvrIDs   []EvrId
}

func (m LobbyPlayerSessionsRequest) Token() string {
	return "SNSLobbyPlayerSessionsRequestv5"
}

func (m LobbyPlayerSessionsRequest) Symbol() Symbol {
	return SymbolOf(&m)
}

func (m *LobbyPlayerSessionsRequest) Stream(s *EasyStream) error {
	playerCount := uint64(len(m.PlayerEvrIDs))
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGuid(&m.LoginSessionID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EvrId.PlatformCode) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EvrId.AccountId) },
		func() error { return s.StreamGuid(&m.LobbySessionID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Platform) },
		func() error { return s.StreamNumber(binary.LittleEndian, &playerCount) },
		func() error {
			if s.Mode == DecodeMode {
				m.PlayerEvrIDs = make([]EvrId, playerCount)
			}
			for i := range m.PlayerEvrIDs {
				if err := s.StreamStruct(&m.PlayerEvrIDs[i]); err != nil {
					return err
				}
			}
			return nil
		},
	})
}

func (m *LobbyPlayerSessionsRequest) String() string {
	return fmt.Sprintf("%s(session=%s, user_id=%s, matching_session=%s, platform=%d, player_user_ids=[%s])",
		m.Token(),
		m.LoginSessionID,
		m.EvrId.String(),
		m.LobbySessionID,
		m.Platform,
		strings.Join(lo.Map(m.PlayerEvrIDs, func(id EvrId, i int) string { return id.Token() }), ", "),
	)
}

func (m *LobbyPlayerSessionsRequest) SessionID() uuid.UUID {
	return m.LoginSessionID
}

func (m *LobbyPlayerSessionsRequest) EvrID() EvrId {
	return m.EvrId
}

func (m *LobbyPlayerSessionsRequest) MatchSessionID() uuid.UUID {
	return m.LobbySessionID
}
