package evr

import "fmt"

// ERGameServerPlayerSessionsUnlocked is a message from game server to server, indicating a session has been started.
// NOTE: This is an unofficial message created for Echo Relay.
type ERGameServerPlayerSessionsUnlocked struct {
	Unused byte
}

func (m *ERGameServerPlayerSessionsUnlocked) Token() string {
	return "ERGameServerPlayerSessionsUnlocked"
}

func (m *ERGameServerPlayerSessionsUnlocked) Symbol() Symbol {
	return 0x7777777777770400
}

func (m *ERGameServerPlayerSessionsUnlocked) String() string {
	return fmt.Sprintf("%s(unused=%d)",
		m.Token(),
		m.Unused,
	)
}

func (m *ERGameServerPlayerSessionsUnlocked) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamByte(&m.Unused) },
	})
}
