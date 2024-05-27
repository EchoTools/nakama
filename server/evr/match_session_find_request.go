package evr

import (
	"encoding/binary"
	"fmt"
)

const (
	// First 8 bytes are the entrant count
	SessionFlag_TeamIndexes     uint32 = 0x100
	SessionFlag_EnableCrossPlay uint32 = 0x200
)

var (
	_ = IdentifyingMessage(&LobbyFindSessionRequest{})
	_ = LobbySessionRequest(&LobbyFindSessionRequest{})
)

// LobbyFindSessionRequest is a message from client to server requesting finding of an existing game session that
// matches the message's underlying arguments.
type LobbyFindSessionRequest struct {
	VersionLock      Symbol
	Mode             Symbol
	Level            Symbol
	Platform         Symbol // DMO, OVR_ORG, etc.
	LoginSessionID   GUID
	CrossPlayEnabled bool

	CurrentMatch    GUID
	Channel         GUID
	SessionSettings SessionSettings
	Entrants        []Entrant
}

type Entrant struct {
	EvrID EvrId
	Role  int16 // -1 for any team
}

func (e Entrant) String() string {
	return fmt.Sprintf("Entrant{EvrID: %s (%d)}", e.EvrID, e.Role)
}

func (m LobbyFindSessionRequest) Token() string {
	return "SNSLobbyFindSessionRequestv11"
}

func (m *LobbyFindSessionRequest) Symbol() Symbol {
	return SymbolOf(m)
}

func (m *LobbyFindSessionRequest) GetChannel() GUID {
	return m.Channel
}

func (m *LobbyFindSessionRequest) GetMode() Symbol {
	return m.Mode
}

func (m *LobbyFindSessionRequest) Stream(s *Stream) error {
	flags := uint32(0)

	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.VersionLock) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Mode) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Level) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Platform) },
		func() error { return s.StreamGUID(&m.LoginSessionID) },
		func() error {
			switch s.Mode {
			case DecodeMode:

				if err := s.StreamNumber(binary.LittleEndian, &flags); err != nil {
					return err
				}

				m.CrossPlayEnabled = flags&SessionFlag_EnableCrossPlay != 0

				m.Entrants = make([]Entrant, flags&0xFF)

				// Set all of the Roles to -1 (unspecified) by default
				for i := range m.Entrants {
					m.Entrants[i].Role = -1
				}
			case EncodeMode:
				if m.CrossPlayEnabled {
					flags |= SessionFlag_EnableCrossPlay
				}

				flags = (flags & 0xFFFFFF00) | uint32(len(m.Entrants))

				// TeamIndexes are only sent if there are entrants with team indexes > -1
				for _, entrant := range m.Entrants {
					if entrant.Role > -1 {
						flags |= SessionFlag_TeamIndexes
						break
					}
				}
				return s.StreamNumber(binary.LittleEndian, &flags)
			}
			return s.Skip(4)
		},
		func() error { return s.StreamGUID(&m.CurrentMatch) },
		func() error { return s.StreamGUID(&m.Channel) },
		func() error { return s.StreamJSON(&m.SessionSettings, true, NoCompression) },
		func() error {
			// Stream the entrants
			for i := range m.Entrants {
				if err := s.StreamStruct(&m.Entrants[i].EvrID); err != nil {
					return err
				}
			}
			return nil
		},
		func() error {
			// Stream the team indexes
			if flags&SessionFlag_TeamIndexes != 0 {
				for i := range m.Entrants {
					if err := s.StreamNumber(binary.LittleEndian, &m.Entrants[i].Role); err != nil {
						return err
					}
				}
			}
			return nil
		},
	})

}

func (m LobbyFindSessionRequest) String() string {
	return fmt.Sprintf("LobbyFindSessionRequest{Mode: %s, Level: %s, Channel: %s}", m.Mode, m.Level, m.Channel)

}

func NewFindSessionRequest(versionLock Symbol, mode Symbol, level Symbol, platform Symbol, loginSessionID GUID, crossPlayEnabled bool, currentMatch GUID, channel GUID, sessionSettings SessionSettings, entrants []Entrant) LobbyFindSessionRequest {
	return LobbyFindSessionRequest{
		VersionLock:      versionLock,
		Mode:             mode,
		Level:            level,
		Platform:         platform,
		LoginSessionID:   loginSessionID,
		CrossPlayEnabled: crossPlayEnabled,
		CurrentMatch:     currentMatch,
		Channel:          channel,
		SessionSettings:  sessionSettings,
		Entrants:         entrants,
	}
}

func (m LobbyFindSessionRequest) GetSessionID() GUID {
	return m.LoginSessionID
}

func (m LobbyFindSessionRequest) GetEvrID() EvrId {
	if len(m.Entrants) == 0 {
		return EvrId{}
	}

	return m.Entrants[0].EvrID
}
