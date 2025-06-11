package evr

import (
	"encoding/binary"
	"net"

	"github.com/gofrs/uuid/v5"
)

type NEVRRegistrationRequestV1 struct {
	LoginSessionID uuid.UUID
	ServerID       uint64
	InternalIP     net.IP
	Port           uint16
	RegionHash     Symbol
	VersionLock    uint64
	TimeStepUsecs  uint32
}

func (r NEVRRegistrationRequestV1) Symbol() Symbol {
	return 0x802806fd6110d2bd // NEVRRegistrationRequestV1
}

func (m NEVRRegistrationRequestV1) GetLoginSessionID() uuid.UUID {
	return m.LoginSessionID
}

func (m *NEVRRegistrationRequestV1) Stream(s *EasyStream) error {
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

type NEVRLobbySessionStartedV1 struct {
	LobbySessionID uuid.UUID
}

func (m *NEVRLobbySessionStartedV1) Stream(s *EasyStream) error {
	return s.StreamGUID(&m.LobbySessionID)
}

type NEVRLobbySessionStartV1 struct {
	GameServerSessionStart
}

type NEVRLobbySessionEndedV1 struct {
	LobbySessionID uuid.UUID
}

func (m *NEVRLobbySessionEndedV1) Stream(s *EasyStream) error {
	return s.StreamGUID(&m.LobbySessionID)
}

type NEVRLobbySessionErroredV1 struct {
	LobbySessionID uuid.UUID
}

func (m *NEVRLobbySessionErroredV1) Stream(s *EasyStream) error {
	return s.StreamGUID(&m.LobbySessionID)
}

type NEVRLobbySessionLockV1 struct {
	LobbySessionID uuid.UUID
}

func (m *NEVRLobbySessionLockV1) Stream(s *EasyStream) error {
	return s.StreamGUID(&m.LobbySessionID)
}

type NEVRLobbySessionUnlockV1 struct {
	LobbySessionID uuid.UUID
}

func (m *NEVRLobbySessionUnlockV1) Stream(s *EasyStream) error {
	return s.StreamGUID(&m.LobbySessionID)
}

type NEVRLobbyEntrantNewV1 struct {
	LobbySessionID uuid.UUID
	EntrantIDs     []uuid.UUID
}

func (m *NEVRLobbyEntrantNewV1) Stream(s *EasyStream) error {
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

type NEVRLobbyEntrantAllowV1 struct {
	EntrantIDs []uuid.UUID
}

func NewEchoToolsLobbyEntrantAllowV1(entrantIDs ...uuid.UUID) *NEVRLobbyEntrantAllowV1 {
	return &NEVRLobbyEntrantAllowV1{EntrantIDs: entrantIDs}
}

func (m *NEVRLobbyEntrantAllowV1) Stream(s *EasyStream) error {
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

type NEVRLobbyEntrantRejectV1 struct {
	ErrorCode  PlayerRejectionReason
	EntrantIDs []uuid.UUID
}

func NewEchoToolsLobbyEntrantRejectV1(errorCode PlayerRejectionReason, entrantIDs ...uuid.UUID) *NEVRLobbyEntrantRejectV1 {
	return &NEVRLobbyEntrantRejectV1{ErrorCode: errorCode, EntrantIDs: entrantIDs}
}

func (m *NEVRLobbyEntrantRejectV1) Stream(s *EasyStream) error {
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

type NEVRLobbyEntrantRemovedV1 struct {
	EntrantID      uuid.UUID
	LobbySessionID uuid.UUID
}

func (m *NEVRLobbyEntrantRemovedV1) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGUID(&m.EntrantID) },
		func() error { return s.StreamGUID(&m.LobbySessionID) },
	})
}
