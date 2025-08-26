package evr

import (
	"fmt"
)

type UserServerProfileUpdateSuccess struct {
	EvrId XPID
}

func (lr *UserServerProfileUpdateSuccess) String() string {
	return fmt.Sprintf("%T(user_id=%s)", lr, lr.EvrId.String())
}
func (m *UserServerProfileUpdateSuccess) Stream(s *EasyStream) error {
	return s.StreamStruct(&m.EvrId)
}
func NewUserServerProfileUpdateSuccess(userId XPID) *UserServerProfileUpdateSuccess {
	return &UserServerProfileUpdateSuccess{
		EvrId: userId,
	}
}
