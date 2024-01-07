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
	Platform        Symbol
	Session         uuid.UUID
	Unk1            uint64
	MatchingSession uuid.UUID
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
		func() error { return s.StreamGuid(&m.Session) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unk1) },
		func() error { return s.StreamGuid(&m.MatchingSession) },
		func() error { return s.StreamGuid(&m.Channel) },
		func() error { return s.StreamJson(&m.SessionSettings, true, NoCompression) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EvrId.PlatformCode) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EvrId.AccountId) },
		func() error {
			// TODO: Figure this out properly
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
	return fmt.Sprintf("%s(version_lock=%016x, mode=%s, level=%s, platform=%s, session=%s, unk1=%d, currentsession=%s, channel=%s, session_settings=%v, evr_id=%s, team_index=%d)",
		m.Token(),
		m.VersionLock,
		m.Mode.Token(),
		m.Level.Token(),
		m.Platform.Token(),
		m.Session.String(),
		m.Unk1,
		m.MatchingSession,
		m.Channel.String(),
		m.SessionSettings.String(),
		m.EvrId.Token(),
		m.TeamIndex,
	)
}

func NewLobbyFindSessionRequest(versionLock uint64, matchMode Symbol, matchLevel Symbol, platform Symbol, session uuid.UUID, unk1 uint64, matchingSession uuid.UUID, channel uuid.UUID, sessionSettings SessionSettings, evrId EvrId, teamIndex int16) LobbyFindSessionRequest {
	return LobbyFindSessionRequest{
		VersionLock:     versionLock,
		Mode:            matchMode,
		Level:           matchLevel,
		Platform:        platform,
		Session:         session,
		Unk1:            unk1,
		MatchingSession: matchingSession,
		Channel:         channel,
		SessionSettings: sessionSettings,
		EvrId:           evrId,
		TeamIndex:       teamIndex,
	}
}

func (m *LobbyFindSessionRequest) SessionID() uuid.UUID {
	return m.Session
}

func (m *LobbyFindSessionRequest) EvrID() EvrId {
	return m.EvrId
}
