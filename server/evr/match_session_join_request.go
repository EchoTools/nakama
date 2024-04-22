package evr

import (
	"encoding/binary"
	"fmt"

	"github.com/gofrs/uuid/v5"
)

const (
	Unk1_Flag0 uint64 = 1 << iota // -mp
	Unk1_Flag1
	Unk1_Flag2
	Unk1_Flag3
	Unk1_Flag4
	Unk1_Flag5
	Unk1_Flag6
	Unk1_Flag7
	Unk1_Flag8 // -moderator
	Unk1_Flag9 // -mp -noovr
	Unk1_Flag10
	Unk1_Flag11
	Unk1_Flag12
	Unk1_Flag13
	Unk1_Flag14
	Unk1_Flag15
	Unk1_Flag16
)

const (
	Unk2_Flag0 uint64 = 1 << iota // -mp
	Unk2_Flag1
	Unk2_Flag2
	Unk2_ModerateUser // -moderateuser
	Unk2_Flag4
	Unk2_Flag5
	Unk2_Flag6
	Unk2_Flag7
	Unk2_Flag8 // -moderator
	Unk2_Flag9 // -mp -noovr
	Unk2_Flag10
	Unk2_Flag11
	Unk2_Flag12
	Unk2_Flag13
	Unk2_Flag14
	Unk2_Flag15
	Unk2_Flag16
)

// LobbyJoinSessionRequest is a message from client to server requesting joining of a specified game session that
// matches the message's underlying arguments.
type LobbyJoinSessionRequest struct {
	MatchID         uuid.UUID
	VersionLock     int64
	Platform        Symbol
	LoginSessionID  uuid.UUID
	Unk1            uint64
	Unk2            uint64
	SessionSettings SessionSettings
	EvrId           EvrId
	TeamIndex       int16
	OtherEvrID      EvrId
}

func (m LobbyJoinSessionRequest) Token() string {
	return "SNSLobbyJoinSessionRequestv7"
}

func (m LobbyJoinSessionRequest) Symbol() Symbol {
	return 3387628926720258577
}

func (m *LobbyJoinSessionRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGuid(&m.MatchID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.VersionLock) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Platform) },
		func() error { return s.StreamGuid(&m.LoginSessionID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unk1) },
		func() error {

			err := s.StreamNumber(binary.LittleEndian, &m.Unk2)
			if err != nil {
				return err
			}
			if m.Unk2&Unk2_ModerateUser != 0 {
				// Parse the lobbyID as the OtherEvrID
				// Take the first 8 bytes of the lobbyID and set it as the platform code
				// Take the last 8 bytes of the lobbyID and set it as the account ID
				// Set the lobbyID to 0
				// REad the bytes
				m.OtherEvrID.PlatformCode = PlatformCode(uint64(m.MatchID[3]))
				m.OtherEvrID.AccountId = uint64(binary.LittleEndian.Uint64(m.MatchID[8:]))
				m.MatchID = uuid.Nil
			}
			return nil
		},
		func() error { return s.StreamJson(&m.SessionSettings, true, NoCompression) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EvrId.PlatformCode) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EvrId.AccountId) },
		func() error {
			if s.Mode == DecodeMode && s.Len() < 2 || s.Mode == EncodeMode && m.TeamIndex == -1 {
				m.TeamIndex = -1
				return nil
			}
			err := s.StreamNumber(binary.LittleEndian, &m.TeamIndex)
			if err != nil {
				return err
			}
			// If the moderator flag is set,
			return nil
		},
	})
}

func (m LobbyJoinSessionRequest) String() string {
	return fmt.Sprintf("%s(lobby_id=%s, version_lock=%d, platform=%s, login_session=%s, unk1=%d, unk2=%d, session_settings=%v, evr_id=%s, team_index=%d, other_evr_id=%s)", m.Token(), m.MatchID, m.VersionLock, m.Platform.String(), m.LoginSessionID, m.Unk1, m.Unk2, m.SessionSettings.String(), m.EvrId.Token(), m.TeamIndex, m.OtherEvrID.Token())
}

func (m *LobbyJoinSessionRequest) SessionID() uuid.UUID {
	return m.LoginSessionID
}

func (m *LobbyJoinSessionRequest) EvrID() EvrId {
	return m.EvrId
}
