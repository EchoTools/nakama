package evr

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
)

type UserServerProfileUpdateRequest struct {
	EvrID   EvrId
	Payload UpdatePayload
}

func (m UserServerProfileUpdateRequest) String() string {
	return fmt.Sprintf("%T{EVRID:%s, MatchType:%s, SessionID:%s}", m, m.EvrID.String(), Symbol(m.Payload.mode).Token().String(), m.Payload.SessionID.String())
}

func (m *UserServerProfileUpdateRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EvrID) },
		func() error { return s.StreamJson(&m.Payload, true, NoCompression) },
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
		return fmt.Errorf("invalid match type: %s", aux.mode)
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
	default:
		return nil, fmt.Errorf("invalid match type: %s", aux.mode)
	}

	return json.Marshal(aux)
}

func (m UpdatePayload) IsWinner() bool {
	switch m.mode {
	case ModeArenaPublic:
		return m.Update.Statistics.Arena.ArenaWins.Value > 0
	case ModeCombatPublic:
		return m.Update.Statistics.Combat.CombatWins.Value > 0
	default:
		return false
	}
}

type ServerProfileUpdate struct {
	Statistics ServerProfileUpdateStatistics `json:"stats"`
	Unlocks    ServerProfileUpdateUnlocks    `json:"unlocks"`
}

type ServerProfileUpdateStatistics struct {
	Arena  ArenaStatistics  `json:"arena"`
	Combat CombatStatistics `json:"combat"`
}

type ServerProfileUpdateUnlocks struct {
	Arena  map[string]bool `json:"arena"`
	Combat map[string]bool `json:"combat"`
}

type StatUpdate struct {
	Operator string  `json:"op"`
	Value    float64 `json:"val"`
	Count    int64   `json:"cnt"`
}
