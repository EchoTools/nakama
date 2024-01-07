package evr

import (
	"fmt"
)

type ChannelInfoRequest struct {
	Unused byte
}

func (m ChannelInfoRequest) Token() string {
	return "SNSChannelInfoRequest"
}

func (m ChannelInfoRequest) Symbol() Symbol {
	return ToSymbol(m.Token())
}

func (m *ChannelInfoRequest) Stream(s *EasyStream) error {
	return s.StreamByte(&m.Unused)
}

func (m ChannelInfoRequest) String() string {
	return fmt.Sprintf("%s()", m.Token())
}
