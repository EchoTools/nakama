package evr

import (
	"encoding/hex"
	"fmt"
)

// GameServerChallengeResponse is a message from the game server to the server,
// providing a response to be validated before registration.
// This is unused in favor of lazy SERVERDB API key authentication.
type GameServerChallengeResponse struct {
	SignedPayload []byte // The signed payload received from the client
}

func (m *GameServerChallengeResponse) Token() string {
	return "ERGameServerChallengeResponse"
}

func (m *GameServerChallengeResponse) Symbol() Symbol {
	return ToSymbol(m.Token())
}

func (r *GameServerChallengeResponse) String() string {
	return fmt.Sprintf("%s(signed_payload=%s)", r.Token(), hex.EncodeToString(r.SignedPayload))
}

func NewERGameServerChallengeResponse(signedPayload []byte) *GameServerChallengeResponse {
	return &GameServerChallengeResponse{
		SignedPayload: signedPayload,
	}
}
func (m *GameServerChallengeResponse) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error {
			if s.Mode == DecodeMode {
				m.SignedPayload = make([]byte, s.Len()-s.Position())
			}
			return s.StreamBytes(&m.SignedPayload, len(m.SignedPayload))
		},
	})
}
