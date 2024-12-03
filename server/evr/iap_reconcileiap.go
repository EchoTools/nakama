package evr

import (
	"fmt"

	"github.com/gofrs/uuid/v5"
)

// ReconcileIAP represents an in-app purchase related request.
type ReconcileIAP struct {
	Message
	Session uuid.UUID
	EvrId   EvrId
}

func (m ReconcileIAP) Token() string {
	return "SNSReconcileIAP"
}

func (m ReconcileIAP) Symbol() Symbol {
	return ToSymbol(m.Token())
}

func (m ReconcileIAP) String() string {
	return fmt.Sprintf("%s()", m.Token())
}

func NewReconcileIAP(userID EvrId, session uuid.UUID) *ReconcileIAP {
	return &ReconcileIAP{
		EvrId:   userID,
		Session: session,
	}
}

func (r *ReconcileIAP) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGUID(&r.Session) },
		func() error { return s.StreamStruct(&r.EvrId) },
	})
}

func (r *ReconcileIAP) ToString() string {
	return fmt.Sprintf("%s(user_id=%s, session=%v)", r.Token(), r.EvrId.Token(), r.Session)
}

func (m *ReconcileIAP) GetSessionID() uuid.UUID {
	return m.Session
}

func (m *ReconcileIAP) GetEvrID() EvrId {
	return m.EvrId
}
