package evr

import (
	"encoding/json"
	"fmt"
)

// OtherUserProfileSuccess is a message from server to the client indicating a successful OtherUserProfileRequest.
// It contains profile information about the requested user.
type OtherUserProfileSuccess struct {
	Message
	EvrId             EvrId
	ServerProfileJSON []byte
}

func NewOtherUserProfileSuccess(evrId EvrId, profile *ServerProfile) *OtherUserProfileSuccess {
	data, err := json.Marshal(profile)
	if err != nil {
		panic("failed to marshal profile")
	}

	return &OtherUserProfileSuccess{
		EvrId:             evrId,
		ServerProfileJSON: data,
	}
}

func (m *OtherUserProfileSuccess) Token() string {
	return "SNSOtherUserProfileSuccess"
}

func (m *OtherUserProfileSuccess) Symbol() Symbol {
	return SymbolOf(m)
}

func (m *OtherUserProfileSuccess) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamStruct(&m.EvrId) },
		func() error {
			return s.StreamCompressedBytes(m.ServerProfileJSON, true, ZstdCompression)
		},
	})
}

func (m *OtherUserProfileSuccess) String() string {
	return fmt.Sprintf("%s(user_id=%s)", m.Token(), m.EvrId.Token())
}

func (m *OtherUserProfileSuccess) GetProfile() ServerProfile {
	profile := ServerProfile{}
	err := json.Unmarshal(m.ServerProfileJSON, &profile)
	if err != nil {
		panic("failed to unmarshal profile")
	}
	return profile
}
