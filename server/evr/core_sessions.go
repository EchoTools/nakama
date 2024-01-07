package evr

import (
	"encoding/binary"

	"github.com/gofrs/uuid/v5"
)

type LoginSession struct {
	AccountId uint64    `json:"account_id"`
	Session   uuid.UUID `json:"session"`
}

func (m *LoginSession) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.AccountId) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Session) },
	})
}

type MatchingSession struct {
	AccountId uint64 `json:"account_id"`
	Session   uint64 `json:"session"`
}

func (m *MatchingSession) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.AccountId) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Session) },
	})
}

type SessionUuid struct {
	uuid.UUID
}

func (m *SessionUuid) Equals(other *SessionUuid) bool {
	return m.UUID == other.UUID
}

func (m *SessionUuid) Stream(s *EasyStream) error {
	bytes, err := m.MarshalBinary()
	if err != nil {
		return err
	}

	return RunErrorFunctions([]func() error{
		func() error { return s.StreamBytes(&bytes, 16) },
	})
}
