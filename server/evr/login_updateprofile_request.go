package evr

import (
	"encoding/binary"
	"fmt"

	"github.com/gofrs/uuid/v5"
)

type UpdateProfile struct {
	Session       uuid.UUID
	EvrId         EvrId
	ClientProfile ClientProfile
}

func (m *UpdateProfile) Token() string {
	return "SNSUpdateProfile"
}

func (m *UpdateProfile) Symbol() Symbol {
	return ToSymbol(m.Token())
}

func (lr *UpdateProfile) String() string {
	return fmt.Sprintf("UpdateProfile(session=%s, evr_id=%s)", lr.Session.String(), lr.EvrId.String(), lr.ClientProfile.String())
}

func (m *UpdateProfile) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGuid(&m.Session) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EvrId.PlatformCode) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EvrId.AccountId) },
		func() error { return s.StreamJson(&m.ClientProfile, true, NoCompression) },
	})
}
