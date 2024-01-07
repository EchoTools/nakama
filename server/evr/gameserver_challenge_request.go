package evr

import (
	"encoding/hex"
	"fmt"
)

// GameServerChallengeRequest represents a message from server to game server, providing a challenge to the game server to complete prior to registration.
// NOTE: This is an unofficial message created for Echo Relay.
// TODO: This is unused in favor of lazy SERVERDB API key authentication.
type GameServerChallengeRequest struct {
	InputPayload []byte
}

func (m *GameServerChallengeRequest) Token() string {
	return "ERGameServerChallengeRequest"
}

func (m *GameServerChallengeRequest) Symbol() Symbol {
	return 0x7777777777770A00
}

// String returns a string representation of the ERGameServerChallengeRequest.
func (r *GameServerChallengeRequest) String() string {
	return fmt.Sprintf("ERGameServerChallengeRequest(input_payload=%s)", hex.EncodeToString(r.InputPayload))
}

func (m *GameServerChallengeRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error {
			if s.Mode == DecodeMode {
				m.InputPayload = make([]byte, s.Len()-s.Position())
			}
			return s.StreamBytes(&m.InputPayload, len(m.InputPayload))
		},
	})
}

func NewERGameServerChallengeRequest(inputPayload []byte) *GameServerChallengeRequest {
	return &GameServerChallengeRequest{
		InputPayload: inputPayload,
	}
}
