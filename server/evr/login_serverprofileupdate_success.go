package evr

import (
	"fmt"
)

var SNSUserServerProfileUpdateSuccessSymbol Symbol = ToSymbol("SNSUserServerProfileUpdateSuccess")

type UserServerProfileUpdateSuccess struct {
	EvrId EvrId
}

func (m *UserServerProfileUpdateSuccess) Token() string {
	return "SNSUserServerProfileUpdateSuccess"
}

func (m *UserServerProfileUpdateSuccess) Symbol() Symbol {
	return SymbolOf(m)
}

func (lr *UserServerProfileUpdateSuccess) String() string {
	return fmt.Sprintf("%s(user_id=%s)", lr.Token(), lr.EvrId.String())
}
func (m *UserServerProfileUpdateSuccess) Stream(s *EasyStream) error {
	return s.StreamStruct(&m.EvrId)
}
func NewUserServerProfileUpdateSuccess(userId EvrId) *UserServerProfileUpdateSuccess {
	return &UserServerProfileUpdateSuccess{
		EvrId: userId,
	}
}
