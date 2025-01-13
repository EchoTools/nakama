package evr

import (
	nevr "github.com/echotools/nevr-common/rtapi"
	"google.golang.org/protobuf/proto"
)

type EchoToolsProtobufMessageV1 struct {
	Envelope *nevr.Envelope
}

func (m *EchoToolsProtobufMessageV1) Stream(s *EasyStream) error {

	var err error
	var payload []byte
	if s.Mode == DecodeMode {
		m.Envelope = &nevr.Envelope{}
		err = proto.Unmarshal(s.Bytes(), m.Envelope)
	} else {
		payload, err = proto.Marshal(m.Envelope)
		err = s.StreamBytes(&payload, len(payload))
	}
	return err

}
