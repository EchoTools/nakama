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
