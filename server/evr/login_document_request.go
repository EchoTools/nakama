package evr

import (
	"fmt"
)

type DocumentRequest struct {
	Language string `json:"language"`
	Name     string `json:"name"`
}

func (m *DocumentRequest) Token() string {
	return "SNSDocumentRequestv2"
}

func (m *DocumentRequest) Symbol() Symbol {
	return SymbolOf(m)
}

func (m DocumentRequest) String() string {
	return fmt.Sprintf("%s(lang=%v, name=%v)", m.Token(), m.Language, m.Name)
}

func (m *DocumentRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNullTerminatedString(&m.Language) },
		func() error { return s.StreamNullTerminatedString(&m.Name) },
	})
}
