package evr

import (
	"encoding/binary"
	"fmt"
)

type ConfigFailure struct {
	Unk0      uint64
	Unk1      uint64
	ErrorInfo ConfigErrorInfo
}

func (m ConfigFailure) String() string {
	e := m.ErrorInfo
	return fmt.Sprintf(`%T(type="%s", id="%s", code=%d, error="%s")`, m, e.Type, e.Identifier, e.ErrorCode, e.Error)
}

type ConfigErrorInfo struct {
	Type       string `json:"type"`
	Identifier string `json:"identifier"`
	ErrorCode  uint64 `json:"errorCode"`
	Error      string `json:"error"`
}

func (m *ConfigFailure) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unk0) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unk1) },
		func() error { return s.StreamJson(&m.ErrorInfo, true, NoCompression) },
	})
}

func NewConfigFailure(typ, id string) *ConfigFailure {
	return &ConfigFailure{
		Unk0: 0,
		ErrorInfo: ConfigErrorInfo{
			Type:       typ,
			Identifier: id,
			ErrorCode:  0x00000001,
			Error:      "resource not found",
		},
	}
}
