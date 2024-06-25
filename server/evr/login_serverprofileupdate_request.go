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
	return fmt.Sprintf("%T{EVRID:%s, MatchType:%s, SessionID:%s}", m, m.EvrID.String(), m.Payload.MatchType.Token().String(), m.Payload.SessionID.String())
}

func (m *UserServerProfileUpdateRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EvrID) },
		func() error { return s.StreamJson(&m.Payload, true, NoCompression) },
	})
}

type UpdatePayload struct {
	MatchType Symbol      `json:"matchtype"`
	SessionID GUID        `json:"sessionid"`
	Update    StatsUpdate `json:"update"`
}

type StatsUpdate struct {
	StatsGroups map[string]map[string]StatUpdate `json:"stats"`
}

type StatUpdate struct {
	Operand string `json:"op"`
	Value   any    `json:"val"`
	Count   *int64 `json:"cnt,omitempty"`
}

func (u StatUpdate) IsFloat64() bool {
	if u.Value == nil {
		return false
	}
	if _, ok := u.Value.(float64); ok {
		return true
	}
	return false
}

func (u StatUpdate) IsInt64() bool {
	if u.Value == nil {
		return false
	}
	if _, ok := u.Value.(int64); ok {
		return true
	}
	return false
}

func (u StatUpdate) Int64() int64 {
	if u.IsInt64() {
		return u.Value.(int64)
	}
	return 0
}

func (u StatUpdate) Float64() float64 {
	if u.IsFloat64() {
		return u.Value.(float64)
	}
	return 0
}
