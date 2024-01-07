package evr

import (
	"fmt"
)

// A message from client to server requesting a specific configuration resource.
type ConfigRequest struct {
	TypeTail   byte `struct:"byte"`
	ConfigInfo ConfigInfo
}

type ConfigInfo struct {
	Type string `json:"type"`
	Id   string `json:"id"`
}

func (m ConfigRequest) Token() string   { return "SNSConfigRequestv2" }
func (m *ConfigRequest) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *ConfigRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamByte(&m.TypeTail) },
		func() error { return s.StreamJson(&m.ConfigInfo, true, NoCompression) },
	})
}

func (m ConfigRequest) String() string {
	return fmt.Sprintf("%s(config_info=%v)", m.Token(), m.ConfigInfo.Type)
}
