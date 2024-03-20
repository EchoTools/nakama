package evr

import (
	"encoding/binary"
	"fmt"
)

type DocumentSuccess struct {
	DocumentNameSymbol Symbol
	Document           interface{}
}

type Document interface {
	Symbol() Symbol
	Token() string
	String() string
}

func (m *DocumentSuccess) Token() string {
	return "SNSDocumentSuccess"
}

func (m *DocumentSuccess) Symbol() Symbol {
	return SymbolOf(m)
}

func (m *DocumentSuccess) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.DocumentNameSymbol) },
		func() error { return s.StreamJson(&m.Document, true, ZstdCompression) },
	})
}

func (m *DocumentSuccess) String() string {
	return fmt.Sprintf("DocumentSuccess(DocumentNameSymbol=%s", m.DocumentNameSymbol.Token())
}

type EulaDocument struct {
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

func (d EulaDocument) Symbol() Symbol {
	return ToSymbol(d.Type)
}

func (d EulaDocument) Token() string {
	return d.Type
}
func (d EulaDocument) String() string {
	return fmt.Sprintf("%s(lang=%v, type=%v)", d.Token(), d.Lang, d.Type)
}

func NewSNSDocumentSuccess(document Document) *DocumentSuccess {
	return &DocumentSuccess{
		DocumentNameSymbol: document.Symbol(),
		Document:           document,
	}
}

func NewEulaDocument(version, versionGa int, linkURL string) *EulaDocument {
	if linkURL == "" {
		linkURL = "https://github.com/EchoTools"
	}
	return &EulaDocument{
		Type:                          "eula",
		Lang:                          "en",
		Version:                       int64(version),
		VersionGameAdmin:              int64(versionGa),
		Text:                          "Welcome to EchoVRCE!\nThis network, weaving through basements, is a work of fiction. Uptime guarantees are comically optimistic; reliance on this service is advised against for anyone.",
		TextGameAdmin:                 "Welcome to EchoVRCE!\n\nThis network, weaving through basements, is a work of fiction. Uptime guarantees are comically optimistic; reliance on this service is advised against for anyone.",
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
