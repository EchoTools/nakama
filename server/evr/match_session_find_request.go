package evr

import (
	"encoding/binary"
	"fmt"

	"github.com/gofrs/uuid/v5"
)

const (
	Unk1_Flag0 = 1
	Unk1_Flag1 = 768
)

// LobbyFindSessionRequest is a message from client to server requesting finding of an existing game session that
// matches the message's underlying arguments.
type LobbyFindSessionRequest struct {
	VersionLock     uint64
	Mode            Symbol
	Level           Symbol
	Platform        Symbol // DMO, OVR_ORG, etc.
	LoginSession    uuid.UUID
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
		func() error { return s.StreamGuid(&m.LoginSession) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unk1) },
		func() error { return s.StreamGuid(&m.CurrentMatch) },
		func() error { return s.StreamGuid(&m.Channel) },
		func() error { return s.StreamJson(&m.SessionSettings, true, NoCompression) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EvrId.PlatformCode) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EvrId.AccountId) },
		func() error {
			// The team index is only present if it's defined.
			if s.Mode == DecodeMode {
				if s.Len() >= 2 {
					return s.StreamNumber(binary.LittleEndian, &m.TeamIndex)
				}
			} else {
				if m.TeamIndex != -1 {
					return s.StreamNumber(binary.LittleEndian, &m.TeamIndex)
				}
			}
			return nil
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
		m.LoginSession.String(),
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
		LoginSession:    loginSession,
		Unk1:            unk1,
		CurrentMatch:    currentSession,
		Channel:         channel,
		SessionSettings: sessionSettings,
		EvrId:           evrID,
		TeamIndex:       teamIndex,
	}
}

func (m *LobbyFindSessionRequest) SessionID() uuid.UUID {
	return m.LoginSession
}

func (m *LobbyFindSessionRequest) EvrID() EvrId {
	return m.EvrId
}
