package evr

import (
	"fmt"
)

type DocumentRequest struct {
	Language string
	Type     string
}

func (m DocumentRequest) String() string {
	return fmt.Sprintf("%T(lang=%s, t=%s)", m, m.Language, m.Type)
}

func (m *DocumentRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNullTerminatedString(&m.Language) },
		func() error { return s.StreamNullTerminatedString(&m.Type) },
	})
}
