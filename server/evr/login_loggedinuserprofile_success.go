package evr

import (
	"encoding/binary"
	"fmt"
)

// SNSLoggedInUserProfileResponse is a message from client to
// server requesting the user profile for their logged-in account.
type LoggedInUserProfileSuccess struct {
	UserId  EvrId
	Payload GameProfiles
}

func (m LoggedInUserProfileSuccess) Token() string {
	return "SNSLoggedInUserProfileSuccess"
}

func (m LoggedInUserProfileSuccess) Symbol() Symbol {
	return ToSymbol(m.Token())
}

func (m *LoggedInUserProfileSuccess) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.UserId.PlatformCode) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.UserId.AccountId) },
		func() error { return s.StreamJson(&m.Payload, true, ZstdCompression) },
	})
}
func (r LoggedInUserProfileSuccess) String() string {
	return fmt.Sprintf("LoggedInUserProfileSuccess(user_id=%v)", r.UserId)
}

func NewLoggedInUserProfileSuccess(userId EvrId, client ClientProfile, server ServerProfile) *LoggedInUserProfileSuccess {
	return &LoggedInUserProfileSuccess{
		UserId: userId,
		Payload: GameProfiles{
			Client: client,
			Server: server,
		},
	}
}
