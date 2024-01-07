package evr

import (
	"encoding/binary"
	"fmt"
)

var Symbol_SNSConfigFailure Symbol = ConfigFailure{}.Symbol()

type ConfigFailure struct {
	Unk0      uint64 // EchoRelay uses a UInt128 here.
	Unk1      uint64
	ErrorInfo ConfigErrorInfo
}

func (m ConfigFailure) Token() string  { return "SNSConfigFailurev2" }
func (m ConfigFailure) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m ConfigFailure) String() string {
	e := m.ErrorInfo
	return fmt.Sprintf(`ConfigErrorInfo{Type: "%v" Identifier: "%v" ErrorCode: 0x%08x Error: "%v"}`, e.Type, e.Identifier, e.ErrorCode, e.Error)
}

type ConfigErrorInfo struct {
	Type       string `json:"type" validate:"required,notblank"`
	Identifier string `json:"identifier" validate:"required,notblank"`
	ErrorCode  uint64 `json:"errorCode" validate:"required,notblank"`
	Error      string `json:"error" validate:"required,notblank"`
}

func (configErrorInfo ConfigErrorInfo) Verify() bool {
	return ValidateStruct(configErrorInfo) == nil
}

func (m *ConfigFailure) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unk0) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unk1) },
		func() error { return s.StreamJson(&m.ErrorInfo, true, NoCompression) },
	})
}

func NewSNSConfigFailure(errorInfo ConfigErrorInfo) *ConfigFailure {
	return &ConfigFailure{
		Unk0:      0,
		ErrorInfo: errorInfo,
	}
}
