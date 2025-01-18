package evr

import (
	"fmt"

	"github.com/gofrs/uuid/v5"
)

type UpdateClientProfile struct {
	LoginSessionID uuid.UUID
	XPID           EvrId
	Payload        ClientProfile
}

func (lr *UpdateClientProfile) String() string {
	return fmt.Sprintf("%T(session=%s, evr_id=%s)", lr, lr.LoginSessionID.String(), lr.XPID.String())
}

func (m *UpdateClientProfile) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGUID(&m.LoginSessionID) },
		func() error { return s.StreamStruct(&m.XPID) },
		func() error { return s.StreamJson(&m.Payload, true, NoCompression) },
	})
}
