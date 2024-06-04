package evr

import (
	"fmt"

	"github.com/muesli/reflow/wordwrap"
)

type Document interface {
	Symbol() Symbol
}

var _ = Document(&EULADocument{})

type EULADocument struct {
	Type                          string `json:"type"`
	Lang                          string `json:"lang"`
	Version                       int64  `json:"version"`
	VersionGameAdmin              int64  `json:"version_ga"`
	Text                          string `json:"text"`
	TextGameAdmin                 string `json:"text_ga"`
	MarkAsReadProfileKey          string `json:"mark_as_read_profile_key"`
	MarkAsReadProfileKeyGameAdmin string `json:"mark_as_read_profile_key_ga"`
	LinkCc                        string `json:"link_cc"`
	LinkPp                        string `json:"link_pp"`
	LinkVR                        string `json:"link_vr"`
	LinkCp                        string `json:"link_cp"`
	LinkEc                        string `json:"link_ec"`
	LinkEa                        string `json:"link_ea"`
	LinkGa                        string `json:"link_ga"`
	LinkTc                        string `json:"link_tc"`
}

func (d EULADocument) Symbol() Symbol {
	return ToSymbol(string(d.Type))
}

func (d EULADocument) String() string {
	return fmt.Sprintf("%T(lang=%v, type=%v)", d, d.Lang, d.Type)
}

func DefaultEULADocument(language string) EULADocument {
	return NewEULADocument(0, 0, language, "https://github.com/EchoTools", wordwrap.String("Welcome to EchoVRCE!\nThis network, weaving through basements, is a work of fiction. Uptime guarantees are comically optimistic, so we advise against relying on this service.", 28))
}

func NewEULADocument(version, versionGa int, language, linkURL, text string) EULADocument {
	return EULADocument{
		Type:                          "eula",
		Lang:                          language,
		Version:                       int64(version),
		VersionGameAdmin:              int64(versionGa),
		Text:                          text,
		TextGameAdmin:                 text,
		MarkAsReadProfileKey:          "legal|eula_version",
		MarkAsReadProfileKeyGameAdmin: "legal|game_admin_version",
		LinkCc:                        linkURL,
		LinkPp:                        linkURL,
		LinkVR:                        linkURL,
		LinkCp:                        linkURL,
		LinkEc:                        linkURL,
		LinkEa:                        linkURL,
		LinkGa:                        linkURL,
		LinkTc:                        linkURL,
	}
}

type DocumentSuccess struct {
	Document Document
}

func NewDocumentSuccess(document Document) *DocumentSuccess {
	return &DocumentSuccess{
		Document: document,
	}
}

func (m *DocumentSuccess) Stream(s *EasyStream) error {
	documentSymbol := m.Document.Symbol()
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamSymbol(&documentSymbol) },
		func() error {
			// Set the document type based on the symbol
			if s.Mode == DecodeMode {
				switch documentSymbol {
				case 0xc8c33e483f6612b1: // eula
					m.Document = &EULADocument{}
				default:
					return fmt.Errorf("unknown document type: `%s`", documentSymbol.Token())
				}
			}
			return s.StreamJson(m.Document, true, ZstdCompression)
		},
	})
}

func (m *DocumentSuccess) String() string {
	return fmt.Sprintf("DocumentSuccess(type=%T)", m.Document)
}
