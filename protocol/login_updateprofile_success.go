package evr

import (
	"fmt"
)

type UpdateProfileSuccess struct {
	XPID EvrId
}

func (lr *UpdateProfileSuccess) String() string {
	return fmt.Sprintf("%T(XPID=%s)", lr, lr.XPID.String())
}

func (m *UpdateProfileSuccess) Stream(s *EasyStream) error {
	return s.StreamStruct(&m.XPID)
}
func NewUpdateProfileSuccess(xpID *EvrId) *UpdateProfileSuccess {
	return &UpdateProfileSuccess{
		XPID: *xpID,
	}
}
