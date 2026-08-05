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
// The client handler (CR14LocalPlayerCS_ParseEarlyQuitConfig @ 0x1401613a0)
// reads a u32 decompressed size followed by zstd-compressed JSON. There is no
// fixed header and no zlib.
func TestEarlyQuitConfig(t *testing.T) {
	cfg := NewDefaultSNSEarlyQuitConfig()

	// Marshal
	s := NewEasyStream(EncodeMode, nil)
	if err := cfg.Stream(s); err != nil {
		t.Fatalf("marshal: %v", err)
	}
	b := s.Bytes()
	if len(b) < 4 {
		t.Fatalf("wire too short: %d bytes", len(b))
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
