package evr

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/go-restruct/restruct"
	"github.com/google/go-cmp/cmp"
)

var (
	//
	testMessageData = []byte{
		0xf6, 0x40, 0xbb, 0x78, 0xa2, 0xe7, 0x8c, 0xbb, // Header
		0xe4, 0xee, 0x6b, 0xc7, 0x3a, 0x96, 0xe6, 0x43, // Symbol (*evr.STCPConnectionUnrequireEvent)
		0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // Length
		0x00, // Data
	}
)

func TestMultipleEnvelopeUnmarshal(t *testing.T) {

	// Test case 2: Valid byte slice
	data := append(testMessageData, testMessageData...)
	restruct.EnableExprBeta()
	packet := &Packet{}
	if err := restruct.Unpack(data, binary.LittleEndian, &packet); err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	packet, err := Unpack(data)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	encoded, err := Pack(packet)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	tests := []struct {
		got  []byte
		want []byte
	}{
		{encoded, data},
	}
	for _, tt := range tests {
		if got := tt.got; !bytes.Equal(got, tt.want) {
			t.Errorf("got %v, want %v", got, tt.want)
			t.Error(cmp.Diff(got, tt.want))
		}
	}
}
