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
	if err := s.StreamByte(&pad); err != nil {
		return err
	}
	return s.StreamJson(&m, true, NoCompression)
}
