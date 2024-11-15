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
	ServerProfileJSON *json.RawMessage
}

func NewOtherUserProfileSuccess(evrId EvrId, profile *ServerProfile) *OtherUserProfileSuccess {
	var data json.RawMessage
	data, err := json.Marshal(profile)
	if err != nil {
		panic("failed to marshal profile")
	}

	return &OtherUserProfileSuccess{
		EvrId:             evrId,
		ServerProfileJSON: &data,
	}
}

func (m *OtherUserProfileSuccess) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamStruct(&m.EvrId) },
		func() error {

			return s.StreamJSONRawMessage(m.ServerProfileJSON, true, ZstdCompression)
		},
	})
}

func (m *OtherUserProfileSuccess) String() string {
	return fmt.Sprintf("%T(user_id=%s)", m, m.EvrId.Token())
}
