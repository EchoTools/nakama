package evr

import (
	"fmt"
)

// BroadcasterSessionEnded is a message from game server to server, indicating the game server's session ended.
// NOTE: This is an unofficial message created for Echo Relay.
type BroadcasterSessionEnded struct {
	Unused byte
}

func (m BroadcasterSessionEnded) Token() string {
	return "ERGameServerEndSession"
}

func (m BroadcasterSessionEnded) Symbol() Symbol {
	return Symbol(0x7777777777770200)
}

func (m BroadcasterSessionEnded) String() string {
	return fmt.Sprintf("BroadcasterSessionEnded(unused=%d)", m.Unused)
}

func (m *BroadcasterSessionEnded) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamByte(&m.Unused) },
	})
}
