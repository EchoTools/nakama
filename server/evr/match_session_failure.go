package evr

import (
	"encoding/binary"
	"fmt"

	"github.com/gofrs/uuid/v5"
)

type LobbySessionFailureErrorCode uint32

const (
	LobbySessionFailure_Timeout0 LobbySessionFailureErrorCode = iota
	LobbySessionFailure_UpdateRequired
	LobbySessionFailure_BadRequest
	LobbySessionFailure_Timeout3
	LobbySessionFailure_ServerDoesNotExist
	LobbySessionFailure_ServerIsIncompatible
	LobbySessionFailure_ServerFindFailed
	LobbySessionFailure_ServerIsLocked
	LobbySessionFailure_ServerIsFull
	LobbySessionFailure_InternalError
	LobbySessionFailure_MissingEntitlement
	LobbySessionFailure_BannedFromLobbyGroup
	LobbySessionFailure_KickedFromLobbyGroup
	LobbySessionFailure_NotALobbyGroupMod
)

// LobbySessionFailure is a message from server to client indicating a lobby session request failed.
type LobbySessionFailure struct {
	GameTypeSymbol Symbol                       // A symbol representing the gametype requested for the session.
	ChannelUUID    uuid.UUID                    // The channel requested for the session.
	ErrorCode      LobbySessionFailureErrorCode // The error code to return with the failure.
	Unk0           uint32                       // TODO: Add description
	Message        string                       // The message sent with the failure.
}

func (m *LobbySessionFailure) String() string {
	return fmt.Sprintf("LobbySessionFailure(game_type=%d, channel=%s, error_code=%d, unk0=%d, msg=%s)",
		m.GameTypeSymbol,
		m.ChannelUUID.String(),
		m.ErrorCode,
		m.Unk0,
		m.Message,
	)
}

func NewLobbySessionFailure(gameType Symbol, channel uuid.UUID, errorCode LobbySessionFailureErrorCode, message string) *LobbySessionFailure {

	return &LobbySessionFailure{
		GameTypeSymbol: gameType,
		ChannelUUID:    channel,
		ErrorCode:      errorCode,
		Unk0:           255,
		Message:        message,
	}
}

func (m *LobbySessionFailure) Version1() *LobbySessionFailurev1 {
	return &LobbySessionFailurev1{
		ErrorCode: uint8(m.ErrorCode),
	}
}
func (m *LobbySessionFailure) Version2() *LobbySessionFailurev2 {
	v2 := LobbySessionFailurev2(*m)
	return &v2
}

func (m *LobbySessionFailure) Version3() *LobbySessionFailurev3 {
	v3 := LobbySessionFailurev3(*m)
	return &v3
}
func (m *LobbySessionFailure) Version4() *LobbySessionFailurev4 {
	v4 := LobbySessionFailurev4(*m)
	return &v4
}

type LobbySessionFailurev1 struct {
	ErrorCode uint8 // The error code to return with the failure.
}

func (m *LobbySessionFailurev1) String() string {
	return fmt.Sprintf("LobbySessionFailurev1(error_code=%d)",
		m.ErrorCode,
	)
}

func (m *LobbySessionFailurev1) Symbol() Symbol { return SymbolOf(m) }
func (m *LobbySessionFailurev1) Token() string  { return "SNSLobbySessionFailurev1" }

func (m *LobbySessionFailurev1) Stream(s *EasyStream) error {
	return s.StreamNumber(binary.LittleEndian, &m.ErrorCode)
}

type LobbySessionFailurev2 LobbySessionFailure

func (m LobbySessionFailurev2) Token() string   { return "SNSLobbySessionFailurev2" }
func (m *LobbySessionFailurev2) Symbol() Symbol { return SymbolOf(m) }

func (m *LobbySessionFailurev2) String() string {
	return fmt.Sprintf("LobbySessionFailurev2(channel_uuid=%s, error_code=%d)", m.ChannelUUID.String(), m.ErrorCode)
}

func (m *LobbySessionFailurev2) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGuid(&m.ChannelUUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.ErrorCode) },
	})
}

type LobbySessionFailurev3 LobbySessionFailure

func (m LobbySessionFailurev3) Token() string   { return "SNSLobbySessionFailurev3" }
func (m *LobbySessionFailurev3) Symbol() Symbol { return SymbolOf(m) }

func (m *LobbySessionFailurev3) String() string {
	return fmt.Sprintf("LobbySessionFailurev3(game_type=%d, channel=%s, error_code=%d, unk0=%d)",
		m.GameTypeSymbol,
		m.ChannelUUID.String(),
		m.ErrorCode,
		m.Unk0,
	)
}

func (m *LobbySessionFailurev3) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.GameTypeSymbol) },
		func() error { return s.StreamGuid(&m.ChannelUUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.ErrorCode) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unk0) },
	})
}

type LobbySessionFailurev4 LobbySessionFailure

func (m LobbySessionFailurev4) Token() string   { return "SNSLobbySessionFailurev4" }
func (m *LobbySessionFailurev4) Symbol() Symbol { return SymbolOf(m) }

func (m *LobbySessionFailurev4) String() string {
	return fmt.Sprintf("LobbySessionFailurev4(game_type=%d, channel=%s, error_code=%d, unk0=%d, msg=%s)",
		m.GameTypeSymbol,
		m.ChannelUUID.String(),
		m.ErrorCode,
		m.Unk0,
		m.Message,
	)
}

func (m *LobbySessionFailurev4) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.GameTypeSymbol) },
		func() error { return s.StreamGuid(&m.ChannelUUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.ErrorCode) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unk0) },
		func() error { return s.StreamString(&m.Message, 72) },
	})
}
