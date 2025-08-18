package evr

import "fmt"

// A message from client to server requesting a specific configuration resource.
type ConfigRequest struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

func (m ConfigRequest) String() string {
	return fmt.Sprintf("%T(id=%s)", m, m.ID)
}

func (m *ConfigRequest) Stream(s *EasyStream) error {
	pad := byte(0)
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamByte(&pad) },
		func() error { return s.StreamJson(&m, true, NoCompression) },
	})
}
