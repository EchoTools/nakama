package evr

import (
	"fmt"

	"github.com/gofrs/uuid/v5"
)

// BroadcasterSessionStarted is a message from game server to server, indicating a session has been started.
// NOTE: This is an unofficial message created for Echo Relay.
type BroadcasterSessionStarted struct {
	LobbySessionID uuid.UUID
}

// Stream streams the message data in/out based on the streaming mode set.
func (m *BroadcasterSessionStarted) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGUID(&m.LobbySessionID) },
	})
}

func (m *BroadcasterSessionStarted) String() string {
	return fmt.Sprintf("%T()", m)
}
