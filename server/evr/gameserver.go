package evr

import (
	"net"

	"github.com/gofrs/uuid/v5"
)

type JSONPayload []byte
type CompressedJSONPayload []byte

type GameServerEntrantList []GameServerLobbyEntrantMeta

type GameServerLobbyEntrantMeta struct {
	Unk0  uuid.UUID
	EvrID EvrId
	Flags uint64
}

type GameServerRegistrationRequest struct {
	ServerID      uint64
	InternalIP    net.IP
	Port          uint16
	Region        Symbol
	VersionLock   uint64
	TimeStepUsecs uint32
}

type GameServerRegistrationSuccess struct {
	ServerId        uint64
	ExternalAddress net.IP
	Unk0            uint64
}

type GameServerRegistrationFailure struct {
	Code uint8
}

type GameServerLobbySessionStart struct {
	MatchID     GUID
	GroupID     GUID
	PlayerLimit uint8
	LobbyType   uint8
	Settings    JSONPayload
	Entrants    []EntrantDescriptor // Information regarding entrants (e.g. including offline/local player ids, or AI bot platform ids).
}

type GameServerLobbySessionStarted struct {
	LobbySessionID GUID
}

type GameServerLobbySessionEnded struct{}

type GameServerLobbySessionErrored struct {
	LobbySessionID GUID
}

// GameServerLobbySessionLock is sent by the game server to signal the game service
// that it should not allow any more entrants to join the lobby.
type GameServerLobbySessionLock struct {
	LobbySessionID GUID
}

// GameServerLobbySessionUnlock is sent by the game server to signal the game service
// that it should allow entrants to join the lobby.
type GameServerLobbySessionUnlock struct {
	LobbySessionID GUID
}

type GameServerLobbyEntrantsAdded struct {
	LobbySessionID GUID
	NumEntrants    uint64
	Entrants       GameServerEntrantList
}

type GameServerLobbyEntrantsAllow struct {
	EntrantIDs []GUID
}

type GameServerLobbyEntrantsReject struct {
	EntrantIDs []GUID
}

type GameServerLobbyEntrantRemoved struct {
	LobbySessionID GUID
	EntrantID      GUID
}
