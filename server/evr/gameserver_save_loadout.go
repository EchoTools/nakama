package evr

import (
	"encoding/binary"
	"encoding/json"
	"fmt"

	"github.com/gofrs/uuid/v5"
)

// GameServerSaveLoadoutRequest is a message sent from the game server to nakama
// when a player updates their loadout at the character customization screen.
// The game server receives this as an internal broadcaster message (SR15NetSaveLoadoutRequest)
// and forwards it to nakama via the TCP broadcaster WebSocket connection.
type GameServerSaveLoadoutRequest struct {
	// EntrantSessionID is the session UUID of the player who saved their loadout
	EntrantSessionID uuid.UUID
	// EvrID is the EchoVR ID of the player
	EvrID EvrId
	// LoadoutNumber is the loadout slot number (0-based)
	LoadoutNumber int32
	// Loadout is the JSON-encoded loadout data
	Loadout json.RawMessage
}

func (m *GameServerSaveLoadoutRequest) String() string {
	return fmt.Sprintf("GameServerSaveLoadoutRequest{EntrantSessionID: %s, EvrID: %s, LoadoutNumber: %d}",
		m.EntrantSessionID.String(), m.EvrID.String(), m.LoadoutNumber)
}

func (m *GameServerSaveLoadoutRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGUID(&m.EntrantSessionID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EvrID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.LoadoutNumber) },
		func() error { return s.StreamJSONRawMessage(&m.Loadout, true, NoCompression) },
	})
}

// SaveLoadoutPayload represents the JSON structure of the loadout data
type SaveLoadoutPayload struct {
	Slots CosmeticLoadout `json:"slots"`
}
