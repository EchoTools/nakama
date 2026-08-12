package evr

import (
	"bytes"
	"runtime"
	"testing"
)

// --- bytes.Split ran before the message-count guard -------------------------
//
// ParsePacket rejected a packet carrying more than MaxMessagesPerPacket
// messages -- but only after splitting it:
//
//	chunks := bytes.Split(data, MessageMarker)   // allocates first
//	...
//	if messageCount > MaxMessagesPerPacket { return ... }
//
// bytes.Split counts the separators up front and allocates
// make([][]byte, Count+1) before it copies anything. A slice header is 24
// bytes, so at the 256 KB MaxPacketLength a packet that is nothing but
// back-to-back 8-byte markers declares 32,768 of them and buys 768 KiB of
// slice headers to describe a packet the very next line throws away. Constant
// ~3x amplification of the attacker's bytes, transient, freed at the next GC.
//
// Counting the markers with bytes.Count first costs one pass and no
// allocation, so the guard can run before anything is allocated.

// markerFloodPacket returns a packet of the maximum accepted size consisting
// entirely of message markers: the worst case for the split, and a packet that
// is rejected either way.
func markerFloodPacket() []byte {
	return bytes.Repeat(MessageMarker, MaxPacketLength/len(MessageMarker))
}

func TestParsePacket_MessageCountGuardRunsBeforeSplit(t *testing.T) {
	payload := markerFloodPacket()
	markers := MaxPacketLength / len(MessageMarker)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	_, err := ParsePacket(payload)

	runtime.ReadMemStats(&after)
	delta := after.TotalAlloc - before.TotalAlloc

	if err == nil {
		t.Fatalf("a packet of %d markers was accepted; it declares far more than MaxMessagesPerPacket=%d messages", markers, MaxMessagesPerPacket)
	}

	// A rejection must not cost more than a scan. The split alone allocated
	// (markers+1) * 24 bytes of slice headers = 768 KiB; the ceiling here is
	// well above any bookkeeping a counting guard needs and well below that.
	const ceiling = 64 << 10
	t.Logf("payload=%d bytes  markers=%d  TotalAlloc delta=%d bytes (%.1f KiB)",
		len(payload), markers, delta, float64(delta)/(1<<10))
	if delta > ceiling {
		t.Fatalf("rejecting a %d-byte packet of %d markers allocated %d bytes (max %d): the message-count guard runs after bytes.Split",
			len(payload), markers, delta, ceiling)
	}
}

// The guard must reject on the real message count, not on the marker count, so
// a packet just over the limit is still refused and one at the limit is not
// refused by this check.
func TestParsePacket_MessageCountBoundary(t *testing.T) {
	// Each message is a marker plus a 16-byte header (symbol + length) and no
	// payload, which is the smallest well-formed message.
	message := func() []byte {
		b := append([]byte{}, MessageMarker...)
		b = appendUint64(b, 0)
		b = appendUint64(b, 0)
		return b
	}

	packetOf := func(n int) []byte {
		var b []byte
		for i := 0; i < n; i++ {
			b = append(b, message()...)
		}
		return b
	}

	if _, err := ParsePacket(packetOf(MaxMessagesPerPacket + 1)); err == nil {
		t.Errorf("a packet of %d messages was accepted, max is %d", MaxMessagesPerPacket+1, MaxMessagesPerPacket)
	}

	// At the limit the count guard must not be what rejects it. Zero-length
	// messages of an unknown symbol may still fail to decode; what must not
	// appear is the "too many messages" error.
	if _, err := ParsePacket(packetOf(MaxMessagesPerPacket)); err != nil {
		if bytes.Contains([]byte(err.Error()), []byte("too many messages")) {
			t.Errorf("a packet of exactly MaxMessagesPerPacket=%d messages was rejected for message count: %v", MaxMessagesPerPacket, err)
		}
	}
}
