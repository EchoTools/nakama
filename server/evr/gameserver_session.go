package evr

import (
	"encoding/binary"
	"errors"
	"net"

	"github.com/gofrs/uuid/v5"
)

type EchoToolsLobbySessionStartedV1 struct {
	LobbySessionID uuid.UUID
}

func (m *EchoToolsLobbySessionStartedV1) Stream(s *EasyStream) error {
	return s.StreamGUID(&m.LobbySessionID)
}

type EchoToolsLobbySessionSuccessV1 struct {
	LobbySessionID uuid.UUID
}

func (m *EchoToolsLobbySessionSuccessV1) Stream(s *EasyStream) error {
	return s.StreamGUID(&m.LobbySessionID)
}

type EchoToolsLobbySessionStartV1 struct {
	GameServerSessionStart
}

type EchoToolsLobbySessionEndedV1 struct {
	LobbySessionID uuid.UUID
}

func (m *EchoToolsLobbySessionEndedV1) Stream(s *EasyStream) error {
	return s.StreamGUID(&m.LobbySessionID)
}

type EchoToolsLobbySessionErroredV1 struct {
	LobbySessionID uuid.UUID
}

func (m *EchoToolsLobbySessionErroredV1) Stream(s *EasyStream) error {
	return s.StreamGUID(&m.LobbySessionID)
}

type EchoToolsLobbySessionLockV1 struct {
	LobbySessionID uuid.UUID
}

func (m *EchoToolsLobbySessionLockV1) Stream(s *EasyStream) error {
	return s.StreamGUID(&m.LobbySessionID)
}

type EchoToolsLobbySessionUnlockV1 struct {
	LobbySessionID uuid.UUID
}

func (m *EchoToolsLobbySessionUnlockV1) Stream(s *EasyStream) error {
	return s.StreamGUID(&m.LobbySessionID)
}

type EchoToolsLobbyEntrantNewV1 struct {
	LobbySessionID uuid.UUID
}

func (m *EchoToolsLobbyEntrantNewV1) Stream(s *EasyStream) error {
	return s.StreamGUID(&m.LobbySessionID)
}

type EchoToolsLobbyEntrantAcceptV1 struct {
	GameServerJoinAllowed
}

type EchoToolsLobbyEntrantRejectV1 struct {
	GameServerJoinRejected
}

type EchoToolsLobbyEntrantRemovedV1 struct {
	GameServerPlayerRemoved
	LobbySessionID uuid.UUID
}

type EchoToolsLobbySessionDataV1 struct {
	SessionID  uuid.UUID
	DataLength uint32
	Data       []byte
}

func (m *EchoToolsLobbySessionDataV1) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{

		func() error { return s.StreamGUID(&m.SessionID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.DataLength) },
		func() error { return s.StreamBytes(&m.Data, int(m.DataLength)) },
	})
}

type EchoToolsLobbyStatusV1 struct {
	SessionID     uuid.UUID
	TimeStepUsecs uint32
	NumEntrants   uint64
	Entrants      []*LobbyStatusEntrant
}

type LobbyStatusEntrant struct {
	EntrantSessionID uuid.UUID
	EntrantEvrID     EvrId
	EntrantFlags     uint64
}

func (m *LobbyStatusEntrant) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{

		func() error { return s.StreamGUID(&m.EntrantSessionID) },
		func() error { return s.StreamStruct(&m.EntrantEvrID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EntrantFlags) },
	})
}

func (m *EchoToolsLobbyStatusV1) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{

		func() error { return s.StreamGUID(&m.SessionID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.TimeStepUsecs) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.NumEntrants) },
		func() error {
			if m.NumEntrants > 16 {
				return errors.New("NumEntrants is too large")
			}
			m.Entrants = make([]*LobbyStatusEntrant, m.NumEntrants)
			for i := range m.Entrants {
				m.Entrants[i] = &LobbyStatusEntrant{}
				if err := s.StreamStruct(m.Entrants[i]); err != nil {
					return err
				}
			}
			return nil
		},
	})
}

type EchoToolsGameServerRegistrationRequestV1 struct {
	LoginSessionID uuid.UUID
	ServerId       uint64
	InternalIP     net.IP
	Port           uint16
	Region         Symbol
	VersionLock    uint64
	TimeStepUsecs  uint32
}

func (m *EchoToolsGameServerRegistrationRequestV1) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGUID(&m.LoginSessionID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.ServerId) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Port) },
		func() error { return s.StreamIpAddress(&m.InternalIP) },
		func() error {
			pad10 := make([]byte, 10) // Pad to 16 bytes
			for i := range pad10 {
				pad10[i] = 0xcc
			}
			return s.StreamBytes(&pad10, 10)
		},
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Region) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.VersionLock) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.TimeStepUsecs) },
	})
}
