package evr

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"log"

	"github.com/gofrs/uuid/v5"
)

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

type EchoToolsLobbySessionDataV1 struct {
	SessionID     uuid.UUID
	TimeStepUsecs uint32
	EntrantCount  uint64
	Entrants      []*EntrantData
}

func (m *EchoToolsLobbySessionDataV1) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGUID(&m.SessionID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.TimeStepUsecs) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EntrantCount) },
		func() error {

			data, _ := json.Marshal(m)
			log.Printf("EchoToolsLobbySessionDataV1: %s", string(data))

			m.Entrants = make([]*EntrantData, m.EntrantCount)
			for i := range m.Entrants {
				m.Entrants[i] = &EntrantData{}
				if err := s.StreamStruct(m.Entrants[i]); err != nil {
					return err
				}
			}
			return nil
		},
	})
}

type EchoToolsLobbyStatusV1 struct {
	SessionID     uuid.UUID
	TimeStepUsecs uint32
	NumEntrants   uint64
	Entrants      []*LobbyStatusEntrant
}

type LobbyStatusEntrant struct {
	EntrantSessionID uuid.UUID
	EntrantEvrID     EvrId
	EntrantFlags     uint64
}

func (m *LobbyStatusEntrant) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{

		func() error { return s.StreamGUID(&m.EntrantSessionID) },
		func() error { return s.StreamStruct(&m.EntrantEvrID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EntrantFlags) },
	})
}

func (m *EchoToolsLobbyStatusV1) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{

		func() error { return s.StreamGUID(&m.SessionID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.TimeStepUsecs) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.NumEntrants) },
		func() error {
			if m.NumEntrants > 16 {
				return errors.New("NumEntrants is too large")
			}
			m.Entrants = make([]*LobbyStatusEntrant, m.NumEntrants)
			for i := range m.Entrants {
				m.Entrants[i] = &LobbyStatusEntrant{}
				if err := s.StreamStruct(m.Entrants[i]); err != nil {
					return err
				}
			}
			return nil
		},
	})
}
