package evr

import "testing"

// TestEarlyQuitFeatureFlags verifies the on-wire format of SNSEarlyQuitFeatureFlags.
//
// The client handler (@ 0x1401618b0) reads a single unsigned byte for the flags
// bitfield (MOVZX EDX, byte ptr [RAX]). The wire payload must be exactly 1 byte.
func TestEarlyQuitFeatureFlags(t *testing.T) {
	m := &SNSEarlyQuitFeatureFlags{Flags: DefaultEarlyQuitFeatureFlags()}

	// Marshal
	s := NewEasyStream(EncodeMode, nil)
	if err := m.Stream(s); err != nil {
		t.Fatalf("marshal: %v", err)
	}
	b := s.Bytes()
	if len(b) != 1 {
		t.Fatalf("encoded %d bytes, want exactly 1", len(b))
	}
	if b[0] != 0x23 {
		t.Errorf("encoded byte = 0x%02x, want 0x23 (bits 0, 1 and 5, the ones the game scripts read)", b[0])
	}

	// Unmarshal
	var out SNSEarlyQuitFeatureFlags
	d := NewEasyStream(DecodeMode, b)
	if err := out.Stream(d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Flags != 0x23 {
		t.Errorf("decoded flags = 0x%02x, want 0x23", out.Flags)
	}
}
