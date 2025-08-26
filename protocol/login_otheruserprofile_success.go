package evr

import (
	"encoding/json"
	"fmt"
)

// OtherUserProfileSuccess is a message from server to the client indicating a successful OtherUserProfileRequest.
// It contains profile information about the requested user.
type OtherUserProfileSuccess struct {
	Message
	XPID        XPID
	ProfileData json.RawMessage // ServerProfile as JSON
}

func NewOtherUserProfileSuccess(evrId XPID, profile *ServerProfile) *OtherUserProfileSuccess {
	var data json.RawMessage
	data, err := json.Marshal(profile)
	if err != nil {
		panic("failed to marshal profile")
	}

	return &OtherUserProfileSuccess{
		XPID:        evrId,
		ProfileData: data,
	}
}

func (m *OtherUserProfileSuccess) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamStruct(&m.XPID) },
		func() error {

			return s.StreamJSONRawMessage(&m.ProfileData, true, ZstdCompression)
		},
	})
}

func (m *OtherUserProfileSuccess) String() string {
	return fmt.Sprintf("%T(user_id=%s)", m, m.XPID.Token())
}
