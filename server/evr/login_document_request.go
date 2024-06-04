package evr

import (
	"fmt"
)

type DocumentRequest struct {
	Language string
	Type     string
}

func (m *DocumentRequest) Token() string {
	return "SNSDocumentRequestv2"
}

func (m *DocumentRequest) Symbol() Symbol {
	return SymbolOf(m)
}

func (m DocumentRequest) String() string {
	return fmt.Sprintf("%s(lang=%v, t=%v)", m.Token(), m.Language, m.Type)
}

func (m *DocumentRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNullTerminatedString(&m.Language) },
		func() error { return s.StreamNullTerminatedString(&m.Type) },
	})
}

func NewDocumentRequest(t, language string) *DocumentRequest {
	return &DocumentRequest{
		Language: string(language),
		Type:     string(t),
	}
}
