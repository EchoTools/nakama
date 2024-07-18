package evr

import (
	"encoding/binary"
	"fmt"

	"github.com/gofrs/uuid/v5"
)

// LobbyFindSessionRequest is a message from client to server requesting finding of an existing game session that
// matches the message's underlying arguments.
type LobbyFindSessionRequest struct {
	VersionLock     uint64
	Mode            Symbol
	Level           Symbol
	Platform        Symbol // DMO, OVR_ORG, etc.
	LoginSessionID  uuid.UUID
	Unk1            uint64
	CurrentMatch    uuid.UUID
	Channel         uuid.UUID
	SessionSettings SessionSettings
	EvrId           EvrId
	TeamIndex       int16
}

func (m LobbyFindSessionRequest) Token() string {
	return "SNSLobbyFindSessionRequestv11"
}

func (m *LobbyFindSessionRequest) Symbol() Symbol {
	return SymbolOf(m)
}

func (m *LobbyFindSessionRequest) Stream(s *EasyStream) error {

	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.VersionLock) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Mode) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Level) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Platform) },
		func() error { return s.StreamGuid(&m.LoginSessionID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unk1) },
		func() error { return s.StreamGuid(&m.CurrentMatch) },
		func() error { return s.StreamGuid(&m.Channel) },
		func() error { return s.StreamJson(&m.SessionSettings, true, NoCompression) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EvrId.PlatformCode) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EvrId.AccountId) },
		func() error {
			if s.Mode == DecodeMode && s.Len() < 2 || s.Mode == EncodeMode && m.TeamIndex == -1 {
				m.TeamIndex = -1
				return nil
			}
			return s.StreamNumber(binary.LittleEndian, &m.TeamIndex)
		},
	})

}

func (m LobbyFindSessionRequest) String() string {
	return fmt.Sprintf("%s(version_lock=%016x, mode=%s, level=%s, platform=%s, login_session=%s, unk1=%d, current_match_id=%s, channel=%s, session_settings=%v, evr_id=%s, team_index=%d)",
		m.Token(),
		m.VersionLock,
		m.Mode.Token(),
		m.Level.Token(),
		m.Platform.String(),
		m.LoginSessionID.String(),
		m.Unk1,
		m.CurrentMatch,
		m.Channel.String(),
		m.SessionSettings.String(),
		m.EvrId.Token(),
		m.TeamIndex,
	)
}

func NewLobbyFindSessionRequest(versionLock uint64, matchMode Symbol, matchLevel Symbol, loginSession uuid.UUID, unk1 uint64, currentSession uuid.UUID, channel uuid.UUID, sessionSettings SessionSettings, platform Symbol, evrID EvrId, teamIndex int16) LobbyFindSessionRequest {
	return LobbyFindSessionRequest{
		VersionLock:     versionLock,
		Mode:            matchMode,
		Level:           matchLevel,
		Platform:        platform,
		LoginSessionID:  loginSession,
		Unk1:            unk1,
		CurrentMatch:    currentSession,
		Channel:         channel,
		SessionSettings: sessionSettings,
		EvrId:           evrID,
		TeamIndex:       teamIndex,
	}
}

func (m *LobbyFindSessionRequest) GetSessionID() uuid.UUID {
	return m.LoginSessionID
}

func (m *LobbyFindSessionRequest) GetEvrID() EvrId {
	return m.EvrId
}
