package evr

import (
	"encoding/binary"
)

type UserServerProfileUpdateRequest struct {
	EvrID   EvrId
	Payload UpdatePayload
}

func (m UserServerProfileUpdateRequest) Token() string {
	return "SNSUserServerProfileUpdateRequest"
}

func (m *UserServerProfileUpdateRequest) Symbol() Symbol {
	return SymbolOf(m)
}

func (m UserServerProfileUpdateRequest) String() string {
	return m.Token()
}

func (m *UserServerProfileUpdateRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EvrID) },
		func() error { return s.StreamJson(&m.Payload, true, NoCompression) },
	})
}

type UpdatePayload struct {
	Matchtype float64     `json:"matchtype"`
	Sessionid string      `json:"sessionid"`
	Update    StatsUpdate `json:"update"`
}

type StatsUpdate struct {
	StatsGroups map[string]any `json:"stats"`
}
