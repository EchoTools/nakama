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
	envelopes := make([]Message, 0)
	err := Unmarshal(data, &envelopes)
	if !errors.Is(err, ErrSymbolNotFound) {
		t.Errorf("got %s, want %s", err, ErrSymbolNotFound)
		return
	}
	if len(envelopes) != 0 {
		t.Fatalf("expected 1 message, got %v", len(envelopes))
	}

	// Test case: Unknown symbol in first packet, known symbol in second packet
	data = append(testMessage, testMessage...)
	data[len(testMessage)+10] = 0x00
	envelopes = make([]Message, 0)
	err = Unmarshal(data, &envelopes)
	if !errors.Is(err, ErrSymbolNotFound) {
		t.Errorf("got %s, want %s", err, ErrSymbolNotFound)
		return
	}
	if len(envelopes) != 1 {
		t.Fatalf("expected 1 message, got %v", len(envelopes))
	}

	// Test case: Unknown symbol in both packets
	data = append(testMessage, testMessage...)
	data[10] = 0x00
	data[len(testMessage)+10] = 0x00
	envelopes = make([]Message, 0)
	err = Unmarshal(data, &envelopes)
	if !errors.Is(err, ErrSymbolNotFound) {
		t.Errorf("got %s, want %s", err, ErrSymbolNotFound)
		return
	}
	if len(envelopes) != 0 {
		t.Fatalf("expected 0 messages, got %v", len(envelopes))
	}

}

type notSymbolizable struct{}
