package evr

import (
	"encoding/binary"
	"fmt"
	"net/http"
)

// nakama -> client: failure response to LoggedInUserProfileFailure.
type LoggedInUserProfileFailure struct {
	EvrId        EvrId
	StatusCode   uint64 // HTTP status code
	ErrorMessage string
}

func (m LoggedInUserProfileFailure) Token() string {
	return "SNSLoggedInUserProfileFailure"
}

func (m LoggedInUserProfileFailure) Symbol() Symbol {
	return ToSymbol(m.Token())
}

func (m *LoggedInUserProfileFailure) String() string {
	return fmt.Sprintf("%s(user_id=%v, status=%v, msg=\"%s\")", m.Token(), m.EvrId, http.StatusText(int(m.StatusCode)), m.ErrorMessage)
}

func (m *LoggedInUserProfileFailure) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EvrId.PlatformCode) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EvrId.AccountId) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.StatusCode) },
		func() error { return s.StreamNullTerminatedString(&m.ErrorMessage) },
	})
}

func NewLoggedInUserProfileFailure(evrId EvrId, statusCode int, message string) *LoggedInUserProfileFailure {
	return &LoggedInUserProfileFailure{
		EvrId:        evrId,
		StatusCode:   uint64(statusCode),
		ErrorMessage: message,
	}
}
