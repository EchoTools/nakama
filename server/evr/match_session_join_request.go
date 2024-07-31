package evr

import (
	"encoding/binary"
	"fmt"

	"github.com/gofrs/uuid/v5"
)

const (
	Flags_Flag0 uint64 = 1 << iota // -mp
	Flags_Flag1
	Flags_Flag2
	Flags_ModerateUser // -moderateuser
	Flags_Flag4
	Flags_Flag5
	Flags_Flag6
	Flags_Flag7
	Flags_Moderator
	Flags_CrossPlayEnabled
	Flags_Flag10
	Flags_Flag11
	Flags_Flag12
	Flags_Flag13
	Flags_Flag14
	Flags_Flag15
	Flags_Flag16
)

var _ = IdentifyingMessage(&LobbyJoinSessionRequest{})
var _ = LobbySessionRequest(&LobbyJoinSessionRequest{})

// LobbyJoinSessionRequest is a message from client to server requesting joining of a specified game session that
// matches the message's underlying arguments.
type LobbyJoinSessionRequest struct {
	MatchID          uuid.UUID
	VersionLock      int64
	Platform         Symbol
	LoginSessionID   uuid.UUID
	Flags            uint64
	CrossPlayEnabled bool
	SessionSettings  LobbySessionSettings
	OtherEvrID       EvrId
	Entrants         []Entrant
}

func (m LobbyJoinSessionRequest) Token() string {
	return "SNSLobbyJoinSessionRequestv7"
}

func (m LobbyJoinSessionRequest) Symbol() Symbol {
	return 3387628926720258577
}

func (m *LobbyJoinSessionRequest) Stream(s *EasyStream) error {
	flags := uint32(0)
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGuid(&m.MatchID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.VersionLock) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Platform) },
		func() error { return s.StreamGuid(&m.LoginSessionID) },
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
		func() error {

			err := s.StreamNumber(binary.LittleEndian, &m.Flags)
			if err != nil {
				return err
			}
			if m.Flags&Flags_ModerateUser != 0 {
				// Parse the lobbyID as the OtherEvrID
				m.OtherEvrID.PlatformCode = PlatformCode(uint64(m.MatchID[3]))
				m.OtherEvrID.AccountId = uint64(binary.LittleEndian.Uint64(m.MatchID[8:]))
				m.MatchID = uuid.Nil
			}
			return nil
		},
		func() error { return s.StreamJson(&m.SessionSettings, true, NoCompression) },
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

func (m LobbyJoinSessionRequest) String() string {
	return fmt.Sprintf("LobbyJoinSessionRequest(match=%s)", m.MatchID)
}

func (m *LobbyJoinSessionRequest) GetSessionID() uuid.UUID {
	return m.LoginSessionID
}

func (m *LobbyJoinSessionRequest) GetEvrID() EvrId {
	if len(m.Entrants) == 0 {
		return EvrId{}
	}
	return m.Entrants[0].EvrID
}

func (m *LobbyJoinSessionRequest) GetChannel() uuid.UUID {
	return uuid.Nil
}

func (m *LobbyJoinSessionRequest) GetMode() Symbol {
	return Symbol(0)
}

func (m *LobbyJoinSessionRequest) GetAlignment() int8 {
	if len(m.Entrants) == 0 {
		return int8(TeamUnassigned)
	}
	return m.Entrants[0].Role
}
