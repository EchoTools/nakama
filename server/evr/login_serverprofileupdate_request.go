package evr

import (
	"encoding/binary"
)

type UserServerProfileUpdateRequest struct {
	EvrId      EvrId
	UpdateInfo UpdateInfo
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
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EvrId) },
		func() error { return s.StreamJson(&m.UpdateInfo, true, NoCompression) },
	})
}

type UpdateInfo struct {
	Sessionid string `json:"sessionid"`
	Matchtype int64  `json:"matchtype"`
	Update    Update `json:"update"`
}

type Update struct {
	Unlocks Unlocks `json:"unlocks"`
}

type Unlocks struct {
	Arena  ArenaUnlocks  `json:"arena"`
	Combat CombatUnlocks `json:"combat"`
}
