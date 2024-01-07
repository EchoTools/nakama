package evr

import (
	"fmt"
)

// OtherUserProfileSuccess is a message from server to the client indicating a successful OtherUserProfileRequest.
// It contains profile information about the requested user.
type OtherUserProfileSuccess struct {
	Message
	EvrId         EvrId
	ServerProfile *ServerProfile
}

func NewOtherUserProfileSuccess(evrId EvrId, profile *ServerProfile) *OtherUserProfileSuccess {
	return &OtherUserProfileSuccess{
		EvrId:         evrId,
		ServerProfile: profile,
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
			return s.StreamJson(m.ServerProfile, true, ZstdCompression)
		},
	})
}

func (m *OtherUserProfileSuccess) String() string {
	return fmt.Sprintf("%s(user_id=%s)", m.Token(), m.EvrId.Token())
}
