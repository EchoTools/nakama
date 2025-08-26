package evr

type UnimplementedMessage struct {
	SymbolHash Symbol
	Data       []byte
}

func (m *UnimplementedMessage) Stream(s *EasyStream) error {
	if s.Mode == DecodeMode {
		m.Data = make([]byte, s.Len())
	}
	return s.StreamBytes(&m.Data, len(m.Data))
}
