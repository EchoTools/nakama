package evr

import (
	"encoding/binary"
	"runtime"
	"testing"
)

// zstdBomb: length prefix + a zstd frame whose Window_Descriptor requests a
// 512 MiB window, decoding to the JSON literal `null`.
func zstdBomb() []byte {
	frame := []byte{0x28, 0xB5, 0x2F, 0xFD, 0x00, 0x98, 0x21, 0x00, 0x00, 'n', 'u', 'l', 'l'}
	out := make([]byte, 4, 4+len(frame))
	binary.LittleEndian.PutUint32(out, uint32(len(frame)))
	return append(out, frame...)
}

// TestZstdWindowIsBounded proves a client cannot force a large allocation via
// the zstd Window_Descriptor. Unbounded, one 17-byte payload allocated 513 MiB.
func TestZstdWindowIsBounded(t *testing.T) {
	payload := zstdBomb()

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	var v any
	s := NewEasyStream(DecodeMode, payload)
	_ = s.StreamJson(&v, false, ZstdCompression) // error is fine; allocation is what matters

	runtime.ReadMemStats(&after)
	delta := after.TotalAlloc - before.TotalAlloc

	const ceiling = 8 << 20 // 8 MiB — generous; the bug allocated 513 MiB
	t.Logf("payload=%d bytes  TotalAlloc delta=%d bytes (%.1f MiB)",
		len(payload), delta, float64(delta)/(1<<20))
	if delta > ceiling {
		t.Fatalf("zstd window unbounded: %d bytes allocated from a %d-byte payload (max %d)",
			delta, len(payload), ceiling)
	}
}

// TestZstdWindowBoundedAcrossPacket: MaxMessagesPerPacket bombs in one packet.
// Unbounded this reached 16.03 GiB.
func TestZstdWindowBoundedAcrossPacket(t *testing.T) {
	payload := zstdBomb()

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	for i := 0; i < MaxMessagesPerPacket; i++ {
		var v any
		s := NewEasyStream(DecodeMode, payload)
		_ = s.StreamJson(&v, false, ZstdCompression)
	}

	runtime.ReadMemStats(&after)
	delta := after.TotalAlloc - before.TotalAlloc

	const ceiling = 64 << 20
	t.Logf("%d messages  TotalAlloc delta=%d bytes (%.2f MiB)",
		MaxMessagesPerPacket, delta, float64(delta)/(1<<20))
	if delta > ceiling {
		t.Fatalf("packet of %d bombs allocated %d bytes (max %d)",
			MaxMessagesPerPacket, delta, ceiling)
	}
}
