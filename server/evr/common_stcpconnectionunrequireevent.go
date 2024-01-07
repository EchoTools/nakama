package evr

type STcpConnectionUnrequireEvent struct {
	Unused byte
}

func (m STcpConnectionUnrequireEvent) Token() string {
	return "STcpConnectionUnrequireEvent"
}

func (m *STcpConnectionUnrequireEvent) Symbol() Symbol {
	return SymbolOf(m)
}

func (m *STcpConnectionUnrequireEvent) Stream(s *EasyStream) error {
	return s.StreamByte(&m.Unused)
}

func (m STcpConnectionUnrequireEvent) String() string {
	return "STcpConnectionUnrequireEvent"
}

func NewSTcpConnectionUnrequireEvent() *STcpConnectionUnrequireEvent {
	return &STcpConnectionUnrequireEvent{
		Unused: byte(0),
	}
}
