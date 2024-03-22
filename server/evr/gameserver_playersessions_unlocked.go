package evr

import "fmt"

// BroadcasterPlayerSessionsUnlocked is a message from game server to server, indicating a session has been started.
// NOTE: This is an unofficial message created for Echo Relay.
type BroadcasterPlayerSessionsUnlocked struct {
	Unused byte
}

func (m *BroadcasterPlayerSessionsUnlocked) Token() string {
	return "ERGameServerPlayerSessionsUnlocked"
}

func (m *BroadcasterPlayerSessionsUnlocked) Symbol() Symbol {
	return 0x7777777777770400
}

func (m *BroadcasterPlayerSessionsUnlocked) String() string {
	return fmt.Sprintf("%s(unused=%d)",
		m.Token(),
		m.Unused,
	)
}

func (m *BroadcasterPlayerSessionsUnlocked) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamByte(&m.Unused) },
	})
}
