package evr

import (
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/gofrs/uuid/v5"
)

type LobbyPlayerSessionsSuccess struct {
	MatchingSession uuid.UUID   // Unk1, The matching-related session token for the current matchmaker operation.
	PlayerSessions  []uuid.UUID // Unk1, The player session token obtained for the requested player user identifier.

	Unk0          byte      // V2, V3
	EvrId         EvrId     // V2, V3
	PlayerSession uuid.UUID // V2, V3
	TeamIndex     int16     // V3
	Unk1          uint16    // V3
	Unk2          uint32    // V3
}

func NewLobbyPlayerSessionsSuccess(evrId EvrId, matchingSession uuid.UUID, playerSession uuid.UUID, playerSessions []uuid.UUID, teamIndex int16) *LobbyPlayerSessionsSuccess {
	return &LobbyPlayerSessionsSuccess{
		MatchingSession: matchingSession,
		PlayerSessions:  playerSessions,
		Unk0:            0xFF,
		EvrId:           evrId,
		PlayerSession:   playerSession,
		TeamIndex:       teamIndex,
		Unk1:            0,
		Unk2:            0,
	}
}

func (m LobbyPlayerSessionsSuccess) VersionU() *LobbyPlayerSessionsSuccessUnk1 {
	s := LobbyPlayerSessionsSuccessUnk1(m)
	return &s
}

func (m LobbyPlayerSessionsSuccess) Version2() *LobbyPlayerSessionsSuccessv2 {
	s := LobbyPlayerSessionsSuccessv2(m)
	return &s
}

func (m LobbyPlayerSessionsSuccess) Version3() *LobbyPlayerSessionsSuccessv3 {
	s := LobbyPlayerSessionsSuccessv3(m)
	return &s
}

type LobbyPlayerSessionsSuccessUnk1 LobbyPlayerSessionsSuccess

func (m *LobbyPlayerSessionsSuccessUnk1) Token() string {
	return "ERLobbyPlayerSessionsSuccessUnk1"
}

func (m *LobbyPlayerSessionsSuccessUnk1) Symbol() Symbol {
	return SymbolOf(m)
}

func (m *LobbyPlayerSessionsSuccessUnk1) Stream(s *EasyStream) error {
	count := uint64(len(m.PlayerSessions))

	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &count) },
		func() error { return s.StreamGuid(&m.MatchingSession) },
		func() error {
			if s.Mode == DecodeMode {
				m.PlayerSessions = make([]uuid.UUID, s.Len()/16)
			}
			return s.StreamGuids(&m.PlayerSessions)
		},
	})
}

func (m *LobbyPlayerSessionsSuccessUnk1) String() string {
	sessions := make([]string, len(m.PlayerSessions))
	for i, session := range m.PlayerSessions {
		sessions[i] = session.String()
	}
	return fmt.Sprintf("%s(matching_session=%s, player_sessions=[%s])", m.Token(), m.MatchingSession, strings.Join(sessions, ", "))
}

type LobbyPlayerSessionsSuccessv3 LobbyPlayerSessionsSuccess

func (m LobbyPlayerSessionsSuccessv3) Token() string {
	return "SNSLobbyPlayerSessionsSuccessv3"
}

func (m *LobbyPlayerSessionsSuccessv3) Symbol() Symbol {
	return SymbolOf(m)
}

func (m *LobbyPlayerSessionsSuccessv3) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unk0) },
		func() error { return s.StreamStruct(&m.EvrId) },
		func() error { return s.StreamGuid(&m.PlayerSession) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.TeamIndex) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unk1) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unk2) },
	})

}

func (m *LobbyPlayerSessionsSuccessv3) String() string {
	return fmt.Sprintf("%s(unk0=%d, user_id=%v, player_session=%s, team_index=%d, unk1=%d, unk2=%d)",
		m.Token(), m.Unk0, m.EvrId, m.PlayerSession, m.TeamIndex, m.Unk1, m.Unk2)
}

type LobbyPlayerSessionsSuccessv2 LobbyPlayerSessionsSuccess

func (m LobbyPlayerSessionsSuccessv2) Token() string {
	return "SNSLobbyPlayerSessionsSuccessv2"
}

func (m *LobbyPlayerSessionsSuccessv2) Symbol() Symbol {
	return SymbolOf(m)
}

// Stream streams the message data in/out based on the streaming mode set.
func (m *LobbyPlayerSessionsSuccessv2) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unk0) },
		func() error { return s.StreamStruct(&m.EvrId) },
		func() error { return s.StreamGuid(&m.PlayerSession) },
	})
}

// String returns a string representation of the LobbyPlayerSessionsSuccessv2 message.
func (m LobbyPlayerSessionsSuccessv2) String() string {
	return fmt.Sprintf("%s(unk0=%d, user_id=%v, player_session=%s)", m.Token(), m.Unk0, m.EvrId, m.PlayerSession)
}
