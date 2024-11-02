package evr

import (
	"encoding/binary"
	"fmt"
)

type UserServerProfileUpdateRequest struct {
	EvrID   EvrId
	Payload UpdatePayload
}

func (m UserServerProfileUpdateRequest) String() string {
	return fmt.Sprintf("%T{EVRID:%s, MatchType:%s, SessionID:%s}", m, m.EvrID.String(), Symbol(m.Payload.MatchType).Token().String(), m.Payload.SessionID.String())
}

func (m *UserServerProfileUpdateRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EvrID) },
		func() error { return s.StreamJson(&m.Payload, true, NoCompression) },
	})
}

type UpdatePayload struct {
	MatchType int64       `json:"matchtype"`
	SessionID GUID        `json:"sessionid"`
	Update    StatsUpdate `json:"update"`
}

type StatsUpdate struct {
	StatsGroups map[string]map[string]StatUpdate `json:"stats"`
}

type StatUpdate struct {
	Operator string  `json:"op"`
	Value    float64 `json:"val"`
	Count    int64   `json:"cnt"`
}
