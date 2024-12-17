package evr

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"log"

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
	XPlatformId EvrId
	TeamIndex   uint16
	Ping        uint16
}

func (m *EntrantData) String() string {
	return fmt.Sprintf("%T(%v, %d, %d)", m, m.XPlatformId, m.TeamIndex, m.Ping)
}

func (m *EntrantData) SizeOf() int {
	return SizeOf(EntrantData{})
}

func (m *EntrantData) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.XPlatformId.AccountId) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.XPlatformId.PlatformCode) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.TeamIndex) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Ping) },
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

		func() error {

			if s.Mode == DecodeMode {
				m.Slots = make([]*EntrantData, numSlots)
			}

			for i := range m.Slots {
				m.Slots[i] = &EntrantData{}
				log.Printf("Stream EntrantData[%d] (%d): %d/%d", i, SizeOf(EntrantData{}), s.Position(), s.Len())

				log.Printf("entrantdata: %v", m.Slots[i])
				if s.Len() < SizeOf(EntrantData{}) {
					m.Slots = m.Slots[:i]
					return nil
				}
				if err := s.StreamStruct(m.Slots[i]); err != nil {
					return err
				}
				log.Printf("%v", m.Slots[i])
			}
			return nil
		},
	})
}

func (m *EchoToolsLobbyStatusV1) String() string {
	return fmt.Sprintf("%T(%s, %d, %d, %d)", m, m.LobbySessionID, m.TimeStepUsecs, m.TickCount, len(m.Slots))
}
