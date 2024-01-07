package evr

import (
	"encoding/binary"
	"fmt"

	"github.com/gofrs/uuid/v5"
)

// LobbyJoinSessionRequest is a message from client to server requesting joining of a specified game session that
// matches the message's underlying arguments.
type LobbyJoinSessionRequest struct {
	LobbyId         uuid.UUID
	VersionLock     int64
	Platform        Symbol
	Session         uuid.UUID
	Unk1            uint64
	Unk2            uint64
	SessionSettings SessionSettings
	EvrId           EvrId
	TeamIndex       int16
}

func (m LobbyJoinSessionRequest) Token() string {
	return "SNSLobbyJoinSessionRequestv7"
}

func (m LobbyJoinSessionRequest) Symbol() Symbol {
	return 3387628926720258577
}

func (m *LobbyJoinSessionRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGuid(&m.LobbyId) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.VersionLock) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Platform) },
		func() error { return s.StreamGuid(&m.Session) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unk1) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unk2) },
		func() error { return s.StreamJson(&m.SessionSettings, true, NoCompression) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EvrId.PlatformCode) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EvrId.AccountId) },
		func() error {
			// TODO (@thesprockee) Figure what's going on here.
			// The envelope's length is different by 2 bytes
			// When the team index is -1 (Any Team)?
			if s.Mode == DecodeMode {
				if s.Len()-s.Position() >= 2 { // Ensure there's two bytes left.
					return s.StreamNumber(binary.LittleEndian, &m.TeamIndex)
				}
			} else if m.TeamIndex != -1 { // // Do not send if set to "Any Team".

				return s.StreamNumber(binary.LittleEndian, &m.TeamIndex)
			}
			return nil
		},
	})
}

func (m LobbyJoinSessionRequest) String() string {
	return fmt.Sprintf("%s(lobbyId=%s, version_lock=%d, platform=%d, session=%s, unk1=%d, unk2=%d, session_settings=%v, user_id=%s, team_index=%d)",
		m.Token(),
		m.LobbyId.String(),
		m.VersionLock,
		m.Platform,
		m.Session,
		m.Unk1,
		m.Unk2,
		m.SessionSettings,
		m.EvrId.String(),
		m.TeamIndex,
	)
}

func (m *LobbyJoinSessionRequest) SessionID() uuid.UUID {
	return m.Session
}

func (m *LobbyJoinSessionRequest) EvrID() EvrId {
	return m.EvrId
}
