package evr

import (
	"encoding/json"
	"fmt"
)

// OtherUserProfileRequest represents a message from client to server requesting the user profile for another user.
type OtherUserProfileRequest struct {
	EvrId EvrId  // The user identifier.
	Data  []byte // The request data for the underlying profile, indicating fields of interest.
}

func (m OtherUserProfileRequest) Token() string {
	return "SNSOtherUserProfileRequest"
}

func (m OtherUserProfileRequest) Symbol() Symbol {
	return ToSymbol(m.Token())
}

func (m *OtherUserProfileRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamStruct(&m.EvrId) },
		func() error { return s.StreamCompressedBytes(m.Data, true, NoCompression) },
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
	profileJson, err := json.Marshal(m.Data)
	if err != nil {
		profileJson = []byte(fmt.Sprintf("error: %s", err))
	}
	return fmt.Sprintf("%s(user_id=%s, profile_request=%s)", m.Token(), m.EvrId.String(), profileJson)
}
