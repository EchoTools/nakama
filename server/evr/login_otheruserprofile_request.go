package evr

import (
	"encoding/json"
	"fmt"
)

// OtherUserProfileRequest represents a message from client to server requesting the user profile for another user.
type OtherUserProfileRequest struct {
	EvrId EvrId           // The user identifier.
	Data  json.RawMessage // The request data for the underlying profile, indicating fields of interest.
}

func (m *OtherUserProfileRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamStruct(&m.EvrId) },
		func() error { return s.StreamJSONRawMessage(&m.Data, true, NoCompression) },
	})
}

// NewOtherUserProfileRequestWithArgs initializes a new OtherUserProfileRequest message with the provided arguments.
func NewOtherUserProfileRequest(userID EvrId, data []byte) *OtherUserProfileRequest {
	return &OtherUserProfileRequest{
		EvrId: userID,
		Data:  data,
	}
}

// String returns a string representation of the OtherUserProfileRequest message.
func (m *OtherUserProfileRequest) String() string {
	return fmt.Sprintf("%T(evr_id=%s)", m, m.EvrId.String())
}
