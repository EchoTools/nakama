package evr

import (
	"github.com/gofrs/uuid/v5"
)

type JSONPayload []byte
type CompressedJSONPayload []byte

type GameServerLobbySessionStarted struct {
	LobbySessionID uuid.UUID
}

func (m *GameServerLobbySessionStarted) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGUID(&m.LobbySessionID) },
	})
}

type GameServerLobbySessionEnded struct {
	LobbySessionID uuid.UUID
}

func (m *GameServerLobbySessionEnded) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGUID(&m.LobbySessionID) },
	})
}

type GameServerLobbySessionErrored struct {
	LobbySessionID uuid.UUID
}

func (m *GameServerLobbySessionErrored) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGUID(&m.LobbySessionID) },
	})
}

type GameServerLobbyEntrantRemoved struct {
	LobbySessionID uuid.UUID
	EntrantID      uuid.UUID
}

func (m *GameServerLobbyEntrantRemoved) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGUID(&m.LobbySessionID) },
		func() error { return s.StreamGUID(&m.EntrantID) },
	})
}
