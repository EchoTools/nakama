package evr

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
)

type UserServerProfileUpdateRequest struct {
	EvrID   XPID
	Payload json.RawMessage
}

func (m UserServerProfileUpdateRequest) String() string {
	return fmt.Sprintf("%T{EVRID:%s", m, m.EvrID)
}

func (m *UserServerProfileUpdateRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EvrID) },
		func() error { return s.StreamJSONRawMessage(&m.Payload, true, NoCompression) },
	})
}

type UpdatePayload struct {
	MatchType int64 `json:"matchtype"`
	mode      Symbol
	SessionID GUID                `json:"sessionid"`
	Update    ServerProfileUpdate `json:"update"`
}

func (m *UpdatePayload) UmarshalJSON(data []byte) error {
	//alias
	type Alias *UpdatePayload
	aux := Alias(m)
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	switch aux.mode {
	case 4421472114608583000:
		aux.mode = ModeCombatPublic
	case 14654894462969098216:
		aux.mode = ModeArenaPublic
	default:
	}

	return nil
}

func (m UpdatePayload) MarshalJSON() ([]byte, error) {
	//alias
	type Alias UpdatePayload
	aux := Alias(m)

	switch aux.mode {
	case ModeCombatPublic:
		aux.mode = 0x3D5C3976578A3158
	case ModeArenaPublic:
		aux.mode = 0xCB60A4DE7E1CAFE8
	}

	return json.Marshal(aux)
}

type ServerProfileUpdate struct {
	Statistics *ServerProfileUpdateStatistics `json:"stats,omitempty"`
	Unlocks    *ServerProfileUpdateUnlocks    `json:"unlocks,omitempty"`
}

type ServerProfileUpdateStatistics struct {
	Arena  *ArenaStatistics  `json:"arena,omitempty"`
	Combat *CombatStatistics `json:"combat,omitempty"`
}

func (m ServerProfileUpdateStatistics) IsWinner() bool {
	isWinner := false

	if m.Arena != nil && m.Arena.ArenaWins.Value > 0 {
		isWinner = true
	}

	if m.Combat != nil && m.Combat.CombatWins.Value > 0 {
		isWinner = true
	}

	return isWinner
}

type ServerProfileUpdateUnlocks struct {
	Arena  map[string]bool `json:"arena"`
	Combat map[string]bool `json:"combat"`
}

type StatUpdate struct {
	Operator string  `json:"op,omitempty"`
	Value    float64 `json:"val,omitempty"`
	Count    int64   `json:"cnt,omitempty"`
}
