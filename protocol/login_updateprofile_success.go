package evr

import (
	"fmt"
)

type UpdateProfileSuccess struct {
	XPID XPID
}

func (lr *UpdateProfileSuccess) String() string {
	return fmt.Sprintf("%T(XPID=%s)", lr, lr.XPID.String())
}

func (m *UpdateProfileSuccess) Stream(s *EasyStream) error {
	return s.StreamStruct(&m.XPID)
}
func NewUpdateProfileSuccess(xpID *XPID) *UpdateProfileSuccess {
	return &UpdateProfileSuccess{
		XPID: *xpID,
	}
}
