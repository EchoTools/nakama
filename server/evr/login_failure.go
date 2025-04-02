package evr

import (
	"encoding/binary"
	"fmt"
)

const (
	DefaultErrorStatusCode = 400 // Bad Input
)

type LoginFailure struct {
	UserId       EvrId
	StatusCode   uint64 // HTTP Status code
	ErrorMessage string
}

func (m LoginFailure) Token() string {
	return "SNSLoginFailure"
}

func (m LoginFailure) Symbol() Symbol {
	return ToSymbol(m.Token())
}

func (m LoginFailure) String() string {
	return fmt.Sprintf("%s(user_id=%s, status_code=%d, error_message=%s)",
		m.Token(), m.UserId.Token(), m.StatusCode, m.ErrorMessage)
}

func (m *LoginFailure) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.UserId.PlatformCode) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.UserId.AccountId) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.StatusCode) },
		func() error { return s.StreamNullTerminatedString(&m.ErrorMessage) },
	})
}

func NewLoginFailure(userId EvrId, errorMessage string) *LoginFailure {
	return &LoginFailure{
		UserId:       userId,
		StatusCode:   DefaultErrorStatusCode,
		ErrorMessage: errorMessage,
	}
}
