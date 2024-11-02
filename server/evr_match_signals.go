package server

import (
	"encoding/json"
	"time"
)

type SignalOpCode int

const (
	SignalPrepareSession SignalOpCode = iota
	SignalStartSession
	SignalEndSession
	SignalLockSession
	SignalUnlockSession
	SignalGetEndpoint
	SignalGetPresences
	SignalReserveSlots
	SignalPruneUnderutilized
	SignalShutdown
)

type SignalEnvelope struct {
	UserId  string
	OpCode  SignalOpCode
	Payload []byte
}

func NewSignalEnvelope(userId string, signal SignalOpCode, data any) *SignalEnvelope {
	payload, err := json.Marshal(data)
	if err != nil {
		return nil
	}
	return &SignalEnvelope{
		UserId:  userId,
		OpCode:  signal,
		Payload: payload,
	}
}

func (s SignalEnvelope) GetOpCode() SignalOpCode {
	return s.OpCode
}

func (s SignalEnvelope) GetData() []byte {
	return s.Payload
}

func (s SignalEnvelope) String() string {
	b, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	return string(b)
}

type SignalResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Payload string `json:"payload"`
}

func (r SignalResponse) String() string {
	b, err := json.Marshal(r)
	if err != nil {
		return ""
	}
	return string(b)
}

func SignalResponseFromString(data string) SignalResponse {
	r := SignalResponse{}
	if err := json.Unmarshal([]byte(data), &r); err != nil {
		return SignalResponse{}
	}
	return r
}

type SignalShutdownPayload struct {
	GraceSeconds         int  `json:"grace_seconds"`
	DisconnectGameServer bool `json:"disconnect_game_server"`
	DisconnectUsers      bool `json:"disconnect_users"`
}

type SignalReserveSlotsPayload struct {
	SessionIDs    []string
	RoleAlignment int
	ExpiryTime    time.Time
}
