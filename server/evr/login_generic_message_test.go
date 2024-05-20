package evr

import (
	"testing"
)

func TestGenericMessageEncodeDecode(t *testing.T) {
	packet := []byte{
		0xf6, 0x40, 0xbb, 0x78, 0xa2, 0xe7, 0x8c, 0xbb, // Magic Marker
		0x69, 0x36, 0xeb, 0x47, 0xcb, 0x99, 0x3e, 0x01, // Symbol
		0x38, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // Length (56)
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // {Session uuid.UUID (16bytes)
		0x05, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // }
		0xc4, 0x89, 0xa5, 0xfa, 0x4a, 0x2e, 0x07, 0x00,
		0x15, 0xe6, 0x48, 0x84, 0xff, 0x35, 0xea, 0xb9,
		0x04, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x16, 0xa9, 0x53, 0x29, 0xef, 0x14, 0x0e, 0x00,
		0x16, 0x39, 0x73, 0xbd, 0x2b, 0x74, 0x03, 0x00,
	}

	codec := NewCodec(nil)

	// Unmarshal test packet
	msgs, err := codec.Unmarshal(packet)
	if err != nil {
		t.Errorf("Unmarshal returned an error: %v", err)
	}
	t.Errorf("%s", msgs)

}
