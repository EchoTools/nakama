package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

type SignalOpCode int

const (
	SignalPrepareSession SignalOpCode = iota
	SignalStartSession
	SignalLockSession
	SignalUnlockSession
	SignalStartedSession
	SignalEndedSession
	SignalGetEndpoint
	SignalGetPresences
	SignalReserveSlots
	SignalPruneUnderutilized
	SignalShutdown
	SignalPlayerUpdate
	SignalKickEntrants
)

type SignalEnvelope struct {
	UserID  string
	OpCode  SignalOpCode
	Payload []byte
}

func NewSignalEnvelope(userID string, signal SignalOpCode, data any) *SignalEnvelope {
	payload, err := json.Marshal(data)
	if err != nil {
		return nil
	}
	return &SignalEnvelope{
		UserID:  userID,
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

type SignalKickEntrantsPayload struct {
	UserIDs []uuid.UUID `json:"user_ids"`
}

type SignalReserveSlotsPayload struct {
	SessionIDs    []string
	RoleAlignment int
	ExpiryTime    time.Time
}

// SignalMatch is a helper function to send a signal to a match.
func SignalMatch(ctx context.Context, nk runtime.NakamaModule, matchID MatchID, opCode SignalOpCode, data any) (string, error) {
	dataJson, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("failed to marshal match label: %w", err)
	}

	signal := SignalEnvelope{
		OpCode:  opCode,
		Payload: dataJson,
	}
	payload, err := json.Marshal(signal)
	if err != nil {
		return "", fmt.Errorf("failed to marshal match signal: %w", err)
	}

	result, err := nk.MatchSignal(ctx, matchID.String(), string(payload))
	if err != nil {
		return "", fmt.Errorf("failed to signal match: %w", err)
	}

	response := SignalResponse{}
	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return "", fmt.Errorf("failed to unmarshal response: %w", err)
	}
	if !response.Success {
		return "", fmt.Errorf("match signal response: %v", response.Message)
	}
	return response.Payload, nil
}
