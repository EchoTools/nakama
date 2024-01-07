package evr

import (
	"encoding/binary"
	"fmt"

	"github.com/gofrs/uuid/v5"
)

type GenericMessageNotify struct {
	MessageType Symbol // ovr_social_member_data or ovr_social_member_data_nack
	Session     uuid.UUID
	RoomId      int64
	PartyData   GenericMessageData
}

func NewGenericMessageNotify(messageType Symbol, session uuid.UUID, roomId int64, partyData GenericMessageData) *GenericMessageNotify {
	return &GenericMessageNotify{
		MessageType: messageType,
		Session:     session,
		RoomId:      roomId,
		PartyData:   partyData,
	}
}

func (m GenericMessageNotify) Token() string {
	return "SNSGenericMessageNotify"
}

func (m *GenericMessageNotify) Symbol() Symbol {
	return SymbolOf(m)
}

func (m *GenericMessageNotify) Stream(s *EasyStream) error {
	padding := make([]byte, 8)
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamBytes(&padding, 8) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.RoomId) },
		func() error { return s.StreamGuid(&m.Session) },
		func() error { return s.StreamSymbol(&m.MessageType) },
		func() error { return s.StreamJson(&m.PartyData, true, ZstdCompression) },
	})
}

func (m *GenericMessageNotify) String() string {
	return fmt.Sprintf("GenericMessageNotify{MessageType: %d, Session: %s, RoomId: %d, PartyData: %v}", m.MessageType, m.Session, m.RoomId, m.PartyData)
}

func (m *GenericMessageNotify) SessionID() uuid.UUID {
	return m.Session
}
