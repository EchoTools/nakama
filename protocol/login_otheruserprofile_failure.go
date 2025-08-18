package evr

import (
	"encoding/binary"
	"fmt"
	"net/http"
)

// OtherUserProfileFailure represents a message from server to client indicating a failure in OtherUserProfileRequest.
type OtherUserProfileFailure struct {
	EvrId      EvrId  // The identifier of the associated user.
	StatusCode uint64 // The status code returned with the failure. (These are http status codes)
	Message    string // The message returned with the failure.
}

func (m *OtherUserProfileFailure) Token() string {
	return "SNSOtherUserProfileFailure"
}

func (m *OtherUserProfileFailure) Symbol() Symbol {
	return SymbolOf(m)
}

func NewOtherUserProfileFailure(evrId EvrId, statusCode uint64, message string) *OtherUserProfileFailure {
	return &OtherUserProfileFailure{
		EvrId:      evrId,
		StatusCode: statusCode,
		Message:    message,
	}
}

func (m *OtherUserProfileFailure) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamStruct(&m.EvrId) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.StatusCode) },
		func() error { return s.StreamNullTerminatedString(&m.Message) },
	})
}

func (m *OtherUserProfileFailure) String() string {
	return fmt.Sprintf("%s(user_id=%v, status=%v, msg=\"%s\")", m.Token(), m.EvrId, http.StatusText(int(m.StatusCode)), m.Message)
}
