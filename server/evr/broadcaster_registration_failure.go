package evr

import (
	"fmt"
)

// BroadcasterRegistrationFailure represents a message from server to game server, indicating a game server registration request had failed.
type BroadcasterRegistrationFailure struct {
	Code BroadcasterRegistrationFailureCode // The failure code for the lobby registration.
}

// BroadcasterRegistrationFailureCode indicates the type of game server registration failure that occurred.
type BroadcasterRegistrationFailureCode byte

const (
	BroadcasterRegistration_InvalidRequest BroadcasterRegistrationFailureCode = iota
	BroadcasterRegistration_Timeout
	BroadcasterRegistration_CryptographyError
	BroadcasterRegistration_DatabaseError
	BroadcasterRegistration_AccountDoesNotExist
	BroadcasterRegistration_ConnectionFailed
	BroadcasterRegistration_ConnectionLost
	BroadcasterRegistration_ProviderError
	BroadcasterRegistration_Restricted
	BroadcasterRegistration_Unknown
	BroadcasterRegistration_Failure
	BroadcasterRegistration_Success
)

func (m *BroadcasterRegistrationFailure) Symbol() Symbol {
	return SymbolOf(m)
}

func (m *BroadcasterRegistrationFailure) Token() string {
	return "SNSBroadcasterRegistrationFailure"
}

// ToString returns a string representation of the BroadcasterRegistrationFailure.
func (lr BroadcasterRegistrationFailure) String() string {
	return fmt.Sprintf("BroadcasterRegistrationFailure(result=%v)", lr.Code)
}

func (m *BroadcasterRegistrationFailure) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{

		func() error { b := byte(m.Code); return s.StreamByte(&b) },
	})
}

func NewBroadcasterRegistrationFailure(code BroadcasterRegistrationFailureCode) *BroadcasterRegistrationFailure {
	return &BroadcasterRegistrationFailure{
		Code: code,
	}
}
