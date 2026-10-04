package evr

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// TestEarlyQuitConfig verifies the on-wire format of SNSEarlyQuitConfig.
//
// The client handler (CR15NetGame::EarlyQuitConfigCB @ 0x1401613a0) reads, one byte into the payload, a u32
// decompressed size followed by zstd-compressed JSON (#665). No zlib.
func TestEarlyQuitConfig(t *testing.T) {
	cfg := NewDefaultSNSEarlyQuitConfig()

	// Marshal
	s := NewEasyStream(EncodeMode, nil)
	if err := cfg.Stream(s); err != nil {
		t.Fatalf("marshal: %v", err)
	}
	b := s.Bytes()
	if len(b) < 5 {
		t.Fatalf("wire too short: %d bytes", len(b))
	}
	if b[0] != 0 {
		t.Fatalf("leading byte = %#x, want 0", b[0])
	}
	// The client reads from the byte after the leading one. Without it, it read `03 00 00 28` as the size.
	b = b[1:]
	if b[4] != 0x28 || b[5] != 0xb5 || b[6] != 0x2f || b[7] != 0xfd {
		t.Fatalf("bytes 5..8 = % x, want the zstd frame magic 28 b5 2f fd right after the size", b[4:8])
	}

	// First 4 bytes: decompressed JSON size (LE u32)
	size := binary.LittleEndian.Uint32(b[:4])
	expected, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if int(size) != len(expected) {
		t.Errorf("size prefix = %d, want %d (decompressed JSON size)", size, len(expected))
	}

	// Remainder: zstd-compressed JSON
	zr, err := zstd.NewReader(bytes.NewReader(b[4:]))
	if err != nil {
		t.Fatalf("zstd.NewReader: %v", err)
	}
	defer zr.Close()
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("zstd decompress: %v", err)
	}
	if len(raw) != int(size) {
		t.Errorf("decompressed %d bytes, want %d", len(raw), size)
	}

	// Parse JSON and check the payload shape
	var parsed SNSEarlyQuitConfig
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("json.Unmarshal: %v: %s", err, raw)
	}
	if len(parsed.PenaltyLevels) == 0 {
		t.Fatal("penalty_levels is empty")
	}
	if parsed.PenaltyLevels[0].PenaltyLevel != 0 {
		t.Errorf("penalty_levels[0].penalty_level = %d, want 0", parsed.PenaltyLevels[0].PenaltyLevel)
	}
}

// The decoder takes the same layout back, leading byte included.
func TestEarlyQuitConfigRoundTrip(t *testing.T) {
	cfg := NewDefaultSNSEarlyQuitConfig()
	enc := NewEasyStream(EncodeMode, nil)
	if err := cfg.Stream(enc); err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got SNSEarlyQuitConfig
	if err := got.Stream(NewEasyStream(DecodeMode, enc.Bytes())); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.PenaltyLevels) != len(cfg.PenaltyLevels) || len(got.SteadyPlayerLevels) != len(cfg.SteadyPlayerLevels) {
		t.Fatalf("round trip: %d/%d levels, want %d/%d", len(got.PenaltyLevels), len(got.SteadyPlayerLevels),
			len(cfg.PenaltyLevels), len(cfg.SteadyPlayerLevels))
	}
}
