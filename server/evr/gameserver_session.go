package evr

import (
	"encoding/binary"
	"net"

	"github.com/gofrs/uuid/v5"
)

type EchoToolsLobbySessionStartedV1 struct {
	LobbySessionID uuid.UUID
}

func (m *EchoToolsLobbySessionStartedV1) Stream(s *EasyStream) error {
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
	EntrantIDs     []uuid.UUID
}

func (m *EchoToolsLobbyEntrantNewV1) Stream(s *EasyStream) error {
	// Print out the base64 encoded bytes

	numEntrants := uint64(len(m.EntrantIDs))
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGUID(&m.LobbySessionID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &numEntrants) },
		func() error {
			if s.Mode == DecodeMode {
				m.EntrantIDs = make([]uuid.UUID, numEntrants)
			}
			return s.StreamGUIDs(&m.EntrantIDs)
		},
	})
}

type EchoToolsLobbyEntrantAllowV1 struct {
	EntrantIDs []uuid.UUID
}

func NewEchoToolsLobbyEntrantAllowV1(entrantIDs ...uuid.UUID) *EchoToolsLobbyEntrantAllowV1 {
	return &EchoToolsLobbyEntrantAllowV1{EntrantIDs: entrantIDs}
}

func (m *EchoToolsLobbyEntrantAllowV1) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.Skip(1) },
		func() error {
			if s.Mode == DecodeMode {
				m.EntrantIDs = make([]uuid.UUID, s.Len()/16)
			}
			return s.StreamGUIDs(&m.EntrantIDs)
		},
	})
}

type EchoToolsLobbyEntrantRejectV1 struct {
	ErrorCode  PlayerRejectionReason
	EntrantIDs []uuid.UUID
}

func NewEchoToolsLobbyEntrantRejectV1(errorCode PlayerRejectionReason, entrantIDs ...uuid.UUID) *EchoToolsLobbyEntrantRejectV1 {
	return &EchoToolsLobbyEntrantRejectV1{ErrorCode: errorCode, EntrantIDs: entrantIDs}
}

func (m *EchoToolsLobbyEntrantRejectV1) Stream(s *EasyStream) error {
	numEntrants := uint64(len(m.EntrantIDs))
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.ErrorCode) },
		func() error { return s.StreamNumber(binary.LittleEndian, &numEntrants) },
		func() error {
			if s.Mode == DecodeMode {
				m.EntrantIDs = make([]uuid.UUID, numEntrants)
			}
			return s.StreamGUIDs(&m.EntrantIDs)
		},
	})
}

type EchoToolsLobbyEntrantRemovedV1 struct {
	EntrantID      uuid.UUID
	LobbySessionID uuid.UUID
}

func (m *EchoToolsLobbyEntrantRemovedV1) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGUID(&m.EntrantID) },
		func() error { return s.StreamGUID(&m.LobbySessionID) },
	})
}

type EchoToolsGameServerRegistrationRequestV1 struct {
	LoginSessionID uuid.UUID
	ServerID       uint64
	InternalIP     net.IP
	Port           uint16
	RegionHash     Symbol
	VersionLock    uint64
	TimeStepUsecs  uint32
}

func (m *EchoToolsGameServerRegistrationRequestV1) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGUID(&m.LoginSessionID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.ServerID) },
		func() error { return s.StreamIpAddress(&m.InternalIP) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Port) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.RegionHash) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.VersionLock) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.TimeStepUsecs) },
	})
}

func (m *EchoToolsGameServerRegistrationRequestV1) GetLoginSessionID() uuid.UUID {
	return m.LoginSessionID
}
