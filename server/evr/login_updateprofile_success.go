package evr

import (
	"fmt"
)

type UpdateProfileSuccess struct {
	UserId EvrId
}

func (m *UpdateProfileSuccess) Token() string {
	return "SNSUpdateProfileSuccess"
}

func (m *UpdateProfileSuccess) Symbol() Symbol {
	return ToSymbol(m.Token())
}

func (lr *UpdateProfileSuccess) String() string {
	return fmt.Sprintf("%s(user_id=%s)", lr.Token(), lr.UserId.String())
}

func (m *UpdateProfileSuccess) Stream(s *EasyStream) error {
	return s.StreamStruct(&m.UserId)
}
func NewSNSUpdateProfileSuccess(userId *EvrId) *UpdateProfileSuccess {
	return &UpdateProfileSuccess{
		UserId: *userId,
	}
}
