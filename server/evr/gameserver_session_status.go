package evr

import (
	"encoding/binary"
	"encoding/json"

	"github.com/gofrs/uuid/v5"
)

type EchoToolsLobbySessionDataV1 struct {
	SessionID uuid.UUID
}

func (m *EchoToolsLobbySessionDataV1) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGUID(&m.SessionID) },
	})
}

type EntrantData struct {
	XPlatformId      EvrId
	PlatformId       Symbol
	UniqueName       []byte
	DisplayName      []byte
	SfwDisplayName   []byte
	Censored         int32
	Owned            bool
	Dirty            bool
	CrossplayEnabled bool
	Unused           uint16
	Ping             uint16
	GenIndex         uint16
	TeamIndex        uint16
	Json             json.RawMessage
}

func (m *EntrantData) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{

		func() error { return s.StreamStruct(&m.XPlatformId) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.PlatformId) },
		func() error { return s.StreamBytes(&m.UniqueName, 32) },
		func() error { return s.StreamBytes(&m.DisplayName, 32) },
		func() error { return s.StreamBytes(&m.SfwDisplayName, 32) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Censored) },
		func() error { return s.StreamBool(&m.Owned) },
		func() error { return s.StreamBool(&m.Dirty) },
		func() error { return s.StreamBool(&m.CrossplayEnabled) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unused) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Ping) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.GenIndex) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.TeamIndex) },
		func() error { return s.StreamJSONRawMessage(&m.Json, true, NoCompression) },
	})
}

type EchoToolsLobbyStatusV1 struct {
	LobbySessionID uuid.UUID
	TimeStepUsecs  uint64
	TickCount      uint64
	Slots          []*EntrantData
}

func (m *EchoToolsLobbyStatusV1) Stream(s *EasyStream) error {
	numSlots := uint64(len(m.Slots))

	return RunErrorFunctions([]func() error{

		func() error { return s.StreamGUID(&m.LobbySessionID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.TimeStepUsecs) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.TickCount) },
		func() error { return s.StreamNumber(binary.LittleEndian, &numSlots) },
		/*
			func() error {
				return nil
				if s.Mode == DecodeMode {
					m.Slots = make([]*EntrantData, numSlots)
				}
				for i := range m.Slots {
					slot := EntrantData{}
					if err := s.StreamStruct(&slot); err != nil {
						return err
					}
					m.Slots[i] = &slot
				}
				return nil
			},
		*/
	})
}
