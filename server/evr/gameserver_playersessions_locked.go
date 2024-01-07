package evr

import "fmt"

// GameServerPlayerSessionsLocked is a message from game server to server, indicating a session has been started.
// NOTE: This is an unofficial message created for Echo Relay.
type GameServerPlayerSessionsLocked struct {
	Unused byte
}

func (m *GameServerPlayerSessionsLocked) Token() string {
	return "ERGameServerPlayerSessionsLocked"
}

func (m *GameServerPlayerSessionsLocked) Symbol() Symbol {
	return 0x7777777777770300
}

// Stream streams the message data in/out based on the streaming mode set.
func (m *GameServerPlayerSessionsLocked) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamByte(&m.Unused) },
	})
}
func (m *GameServerPlayerSessionsLocked) String() string {
	return fmt.Sprintf("%s(unused=%d)",
		m.Token(),
		m.Unused,
	)
}
