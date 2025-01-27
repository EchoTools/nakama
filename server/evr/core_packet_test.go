package evr

import (
	"bytes"
	"errors"
	"log"
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
)

var (
	//
	testMessage = []byte{
		0xf6, 0x40, 0xbb, 0x78, 0xa2, 0xe7, 0x8c, 0xbb, // Header
		0xe4, 0xee, 0x6b, 0xc7, 0x3a, 0x96, 0xe6, 0x43, // Symbol (*evr.STCPConnectionUnrequireEvent)
		0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // Length
		0x00, // Data
	}
)

func TestMultipleUnmarshal(t *testing.T) {

	// Test case 2: Valid byte slice
	b3 := append(testMessage, testMessage...)

	packet, err2 := ParsePacket(b3)
	if err2 != nil {
		t.Errorf("Unexpected error: %v", err2)
	}
	b, err := Marshal(packet...)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	tests := []struct {
		got  []byte
		want []byte
	}{
		{b, b3},
	}
	for _, tt := range tests {
		if got := tt.got; !bytes.Equal(got, tt.want) {
			t.Errorf("got %v, want %v", got, tt.want)
			t.Errorf(cmp.Diff(got, tt.want))
		}
	}
}

func TestMarshal(t *testing.T) {

	// Test case 2: Valid byte slice

	b3 := append(testMessage, testMessage...)

	packets, err2 := ParsePacket(b3)
	if err2 != nil {
		t.Errorf("Unexpected error: %v", err2)
	}
	log.Printf("Packets: %v", packets)
	messages := make([]Message, len(packets))
	copy(messages, packets)
	log.Printf("messages: %v", messages)
	b4, err := Marshal(messages...)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	tests := []struct {
		got  []byte
		want []byte
	}{
		{b4, b3},
	}
	for _, tt := range tests {
		if got := tt.got; !bytes.Equal(got, tt.want) {
			t.Errorf("got %v, want %v", got, tt.want)
			t.Errorf(cmp.Diff(got, tt.want))
		}
	}

}

func TestMultipleMarshal(t *testing.T) {

	// Test case 2: Valid byte slice
	b3 := append(testMessage, testMessage...)

	packet, err2 := ParsePacket(b3)
	if err2 != nil {
		t.Errorf("Unexpected error: %v", err2)
	}
	b, err := Marshal(packet...)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	tests := []struct {
		got  []byte
		want []byte
	}{
		{b, b3},
	}
	for _, tt := range tests {
		if got := tt.got; !bytes.Equal(got, tt.want) {
			t.Errorf("got %v, want %v", got, tt.want)
			t.Errorf(cmp.Diff(got, tt.want))
		}
	}
}

func TestUnmarshalUnknownSymbolPacket(t *testing.T) {

	// Test case: Unknown symbol in first message returns the second message and error

	data := append(testMessage, testMessage...)
	data[10] = 0x00
	packet, err := ParsePacket(data)
	if !errors.Is(err, ErrSymbolNotFound) {
		t.Errorf("got %v, want %s", err, ErrSymbolNotFound)
		return
	}
	if len(packet) != 0 {
		t.Fatalf("expected 0 message, got %v", len(packet))
	}

	// Test case: Unknown symbol in first packet, known symbol in second packet

	data = append(testMessage, testMessage...)
	data[len(testMessage)+10] = 0x00
	packet, err = ParsePacket(data)
	if !errors.Is(err, ErrSymbolNotFound) {
		t.Errorf("got %s, want %s", err, ErrSymbolNotFound)
		return
	}
	if len(packet) != 1 {
		t.Fatalf("expected 1 message, got %v", len(packet))
	}

	// Test case: Unknown symbol in both packets

	data = append(testMessage, testMessage...)
	data[10] = 0x00
	data[len(testMessage)+10] = 0x00
	packet, err = ParsePacket(data)
	if !errors.Is(err, ErrSymbolNotFound) {
		t.Errorf("got %s, want %s", err, ErrSymbolNotFound)
		return
	}
	if len(packet) != 0 {
		t.Fatalf("expected 0 messages, got %v", len(packet))
	}

	// Test case: Short packet in first message, valid second

	data = append(testMessage[:len(testMessage)-8], testMessage...)
	data[len(testMessage)+10] = 0x00
	packet, err = ParsePacket(data)
	if !errors.Is(err, ErrInvalidPacket) {
		t.Errorf("got %v, want %v", err, ErrInvalidPacket)
		return
	}
	if len(packet) != 0 {
		t.Fatalf("expected 0 messages, got %v", len(packet))
	}

	// Test case: Short packet in second message, valid first

	data = append(testMessage, testMessage[:len(testMessage)-8]...)
	data[len(testMessage)+10] = 0x00
	packet, err = ParsePacket(data)
	if !errors.Is(err, ErrInvalidPacket) {
		t.Errorf("got %s, want %s", err, ErrInvalidPacket)
		return
	}

	if len(packet) != 1 {
		t.Fatalf("expected 1 messages, got %v", len(packet))
	}

}

func TestUnmarshalInvalidPacket(t *testing.T) {

	// Test case: Unknown symbol in first message returns the second message and error

	data := append(testMessage, testMessage...)
	data[10] = 0x00
	packet, err := ParsePacket(data)
	if !errors.Is(err, ErrSymbolNotFound) {
		t.Errorf("got %s, want %s", err, ErrSymbolNotFound)
		return
	}
	if len(packet) != 0 {
		t.Fatalf("expected 1 message, got %v", len(packet))
	}

	// Test case: Unknown symbol in first packet, known symbol in second packet
	data = append(testMessage, testMessage...)
	data[len(testMessage)+10] = 0x00
	packet, err = ParsePacket(data)
	if !errors.Is(err, ErrSymbolNotFound) {
		t.Errorf("got %s, want %s", err, ErrSymbolNotFound)
		return
	}
	if len(packet) != 1 {
		t.Fatalf("expected 1 message, got %v", len(packet))
	}

	// Test case: Unknown symbol in both packets
	data = append(testMessage, testMessage...)
	data[10] = 0x00
	data[len(testMessage)+10] = 0x00
	packet, err = ParsePacket(data)
	if !errors.Is(err, ErrSymbolNotFound) {
		t.Errorf("got %s, want %s", err, ErrSymbolNotFound)
		return
	}
	if len(packet) != 0 {
		t.Fatalf("expected 0 messages, got %v", len(packet))
	}

}

func TestWrapBytes(t *testing.T) {
	type args struct {
		symbol Symbol
		data   []byte
	}
	tests := []struct {
		name    string
		args    args
		want    []byte
		wantErr bool
	}{
		{
			"Valid",
			args{
				SymbolOf(&STcpConnectionUnrequireEvent{}),
				[]byte{
					0xfe,
				},
			},
			[]byte{
				0xf6, 0x40, 0xbb, 0x78, 0xa2, 0xe7, 0x8c, 0xbb,
				0xe4, 0xee, 0x6b, 0xc7, 0x3a, 0x96, 0xe6, 0x43,
				0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0xfe,
			},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := WrapBytes(tt.args.symbol, tt.args.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("WrapBytes() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("WrapBytes() = %v, want %v", got, tt.want)
			}
		})
	}
}
func TestSymbol_Int64(t *testing.T) {
	tests := []struct {
		name string
		s    Symbol
		want int64
	}{
		{
			name: "Negative value",
			s:    Symbol(0x8000000000000000),
			want: -9223372036854775808,
		},
		{
			name: "Zero value",
			s:    Symbol(0),
			want: 0,
		},
		{
			name: "Max uint64 value",
			s:    Symbol(0xffffffffffffffff),
			want: -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.s.Int64(); got != tt.want {
				t.Errorf("Symbol.Int64() = %v, want %v", got, tt.want)
			}
		})
	}
}
