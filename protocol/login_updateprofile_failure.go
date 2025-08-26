package evr

import (
	"encoding/binary"
	"fmt"
)

type UpdateProfileFailure struct {
	EvrId      XPID
	statusCode uint64 // HTTP Status Code
	Message    string
}

func (m *UpdateProfileFailure) Token() string {
	return "SNSUpdateProfileFailure"
}

func (m *UpdateProfileFailure) Symbol() Symbol {
	return ToSymbol(m.Token())
}

func (lr *UpdateProfileFailure) String() string {
	return fmt.Sprintf("%s(user_id=%s, status_code=%d, msg='%s')", lr.Token(), lr.EvrId.String(), lr.statusCode, lr.Message)
}

func (m *UpdateProfileFailure) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EvrId) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.statusCode) },
		func() error { return s.StreamNullTerminatedString(&m.Message) },
	})
}

func NewUpdateProfileFailure(evrId XPID, statusCode uint64, message string) *UpdateProfileFailure {
	return &UpdateProfileFailure{
		EvrId:      evrId,
		statusCode: statusCode,
		Message:    message,
	}
}
