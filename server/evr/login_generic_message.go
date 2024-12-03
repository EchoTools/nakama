package evr

import (
	"encoding/binary"
	"fmt"

	"github.com/gofrs/uuid/v5"
)

type GenericMessage struct {
	Session     uuid.UUID
	AcctId      uint64
	MessageType Symbol
	OtherEvrID  EvrId
	RoomID      int64
	PartyData   GenericMessageData
}

func NewGenericMessage(session uuid.UUID, acctId uint64, messageType Symbol, otherEvrId EvrId, partyData GenericMessageData) *GenericMessage {
	return &GenericMessage{
		Session:     session,
		AcctId:      acctId,
		MessageType: messageType,
		OtherEvrID:  otherEvrId,
		PartyData:   partyData,
	}
}

func (m GenericMessage) Token() string {
	return "SNSGenericMessage"
}

func (m *GenericMessage) Symbol() Symbol {
	return SymbolOf(m)
}

func (m *GenericMessage) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGUID(&m.Session) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.AcctId) },
		func() error { return s.StreamSymbol(&m.MessageType) },
		func() error { return s.StreamStruct(&m.OtherEvrID) },
		func() error {
			// ovr_social_member_data_nack
			if m.MessageType == 0xb9ea35ff8448e615 {
				return s.StreamNumber(binary.LittleEndian, &m.RoomID)
			} else {
				return s.StreamJson(&m.PartyData, true, ZstdCompression)
			}
		},
	})
}

func (m *GenericMessage) GetLoginSessionID() uuid.UUID {
	return m.Session
}

func (m GenericMessage) String() string {
	return fmt.Sprintf("GenericMessage{Session: %s, AcctId: %d, OVRSymbol: %d, OtherEvrId: %s, RoomId: %d, PartyData: %v}", m.Session, m.AcctId, m.MessageType, m.OtherEvrID.Token(), m.RoomID, m.PartyData)
}

type GenericMessageData struct {
	Mode        int64  `json:"matchtype"`
	HeadsetType int    `json:"headsettype"`
	Status      string `json:"status"`
	LobbyType   uint8  `json:"lobbytype"`
	LobbyId     string `json:"lobbyid"`
	Team        int16  `json:"team"`
	RoomId      uint64 `json:"roomid"`
}

func NewGenericMessageData(matchType Symbol, headsetType int, status string, lobbyType LobbyType, lobbyId string, team int, roomId int) *GenericMessageData {
	return &GenericMessageData{
		Mode:        int64(matchType),
		HeadsetType: int(headsetType),
		Status:      status,
		LobbyType:   uint8(lobbyType),
		LobbyId:     lobbyId,
		Team:        int16(team),
		RoomId:      uint64(roomId),
	}
}
