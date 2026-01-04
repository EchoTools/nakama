package evr

import (
	"fmt"
)

// SNSLoggedInUserProfileResponse is a message from client to
// server requesting the user profile for their logged-in account.
type LoggedInUserProfileSuccess struct {
	UserId  EvrId
	Payload UserProfiles
}

func (m LoggedInUserProfileSuccess) Token() string {
	return "SNSLoggedInUserProfileSuccess"
}

func (m LoggedInUserProfileSuccess) Symbol() Symbol {
	return ToSymbol(m.Token())
}

func (m *LoggedInUserProfileSuccess) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamStruct(&m.UserId) },
		func() error { return s.StreamJson(&m.Payload, true, ZstdCompression) },
	})
}
func (r LoggedInUserProfileSuccess) String() string {
	return fmt.Sprintf("LoggedInUserProfileSuccess(user_id=%v)", r.UserId)
}

func NewLoggedInUserProfileSuccess(xpid EvrId, client *ClientProfile, server *ServerProfile) *LoggedInUserProfileSuccess {
	return &LoggedInUserProfileSuccess{
		UserId: xpid,
		Payload: UserProfiles{
			Client: client,
			Server: server,
		},
	}
}

type UserProfiles struct {
	Client *ClientProfile          `json:"client"`
	Server *ServerProfile          `json:"server"`
	Config *EarlyQuitServiceConfig `json:"config,omitempty"`
}
