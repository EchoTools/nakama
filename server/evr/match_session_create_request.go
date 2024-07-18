package evr

import (
	"encoding/binary"
	"fmt"

	"github.com/gofrs/uuid/v5"
)

var (
	UnspecifiedRegion = Symbol(0xffffffffffffffff)
	DefaultRegion     = ToSymbol("default")
)

// LobbyCreateSessionRequest represents a request from the client to the server for creating a new game session.
type LobbyCreateSessionRequest struct {
	Region          Symbol          // Symbol representing the region
	VersionLock     int64           // Version lock
	Mode            Symbol          // Symbol representing the game type
	Level           Symbol          // Symbol representing the level
	Platform        Symbol          // Symbol representing the platform
	LoginSessionID  uuid.UUID       // Session identifier
	Unk1            uint64          // Unknown field 1
	LobbyType       uint32          // the visibility of the session to create.
	Unk2            uint32          // Unknown field 2
	Channel         uuid.UUID       // Channel UUID
	SessionSettings SessionSettings // Session settings
	EvrId           EvrId           // User ID
	TeamIndex       int16           // Index of the team
}

func (m LobbyCreateSessionRequest) Token() string {
	return "SNSLobbyCreateSessionRequestv9"
}

func (m LobbyCreateSessionRequest) Symbol() Symbol {
	return 6456590782678944787
}

func (m *LobbyCreateSessionRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Region) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.VersionLock) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Mode) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Level) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Platform) },
		func() error { return s.StreamGuid(&m.LoginSessionID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unk1) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.LobbyType) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unk2) },
		func() error { return s.StreamGuid(&m.Channel) },
		func() error { return s.StreamJson(&m.SessionSettings, true, NoCompression) },
		func() error { return s.StreamStruct(&m.EvrId) },
		func() error {
			if s.Mode == DecodeMode && s.Len() < 2 || s.Mode == EncodeMode && m.TeamIndex == -1 {
				m.TeamIndex = -1
				return nil
			}
			return s.StreamNumber(binary.LittleEndian, &m.TeamIndex)
		},
	})

}
func (m *LobbyCreateSessionRequest) String() string {
	return fmt.Sprintf("%s(RegionSymbol=%d, version_lock=%d, game_type=%d, level=%d, platform=%d, session=%s, unk1=%d, lobby_type=%d, unk2=%d, channel=%s, session_settings=%s, user_id=%s, team_index=%d)",
		m.Token(),
		m.Region,
		m.VersionLock,
		m.Mode,
		m.Level,
		m.Platform,
		m.LoginSessionID.String(),
		m.Unk1,
		m.LobbyType,
		m.Unk2,
		m.Channel.String(),
		m.SessionSettings.String(),
		m.EvrId.String(),
		m.TeamIndex,
	)
}

func (m *LobbyCreateSessionRequest) GetSessionID() uuid.UUID {
	return m.LoginSessionID
}

func (m *LobbyCreateSessionRequest) GetEvrID() EvrId {
	return m.EvrId
}
