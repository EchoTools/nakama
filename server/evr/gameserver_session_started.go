package evr

import (
	"fmt"
)

// BroadcasterSessionStarted is a message from game server to server, indicating a session has been started.
// NOTE: This is an unofficial message created for Echo Relay.
type BroadcasterSessionStarted struct {
	Unused byte
}

func (m *BroadcasterSessionStarted) Token() string {
	return "ERGameServerSessionStarted"
}

func (m *BroadcasterSessionStarted) Symbol() Symbol {
	return 0x7777777777770100
}

// Stream streams the message data in/out based on the streaming mode set.
func (m *BroadcasterSessionStarted) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamByte(&m.Unused) },
	})
}
func (m *BroadcasterSessionStarted) String() string {
	return fmt.Sprintf("%s(unused=%d)",
		m.Token(),
		m.Unused,
	)
}
