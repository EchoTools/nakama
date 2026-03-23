package evr

import (
	"encoding/binary"
	"testing"
)

func TestPacketEncoderConfigFromFlags(t *testing.T) {
	tests := []struct {
		flags              uint64
		expectedEncryption bool
		expectedMac        bool
		expectedDigestSize uint16
		expectedIteration  uint16
		expectedMacKeySize uint16
		expectedKeySize    uint16
		expectedRandomSize uint16
	}{
		{0, false, false, 0, 0, 0, 0, 0},
		{1, true, false, 0, 0, 0, 0, 0},
		{2, false, true, 0, 0, 0, 0, 0},
		{3, true, true, 0, 0, 0, 0, 0},
		{36037595259470083, true, true, 0x40, 0x00, 0x20, 0x20, 0x20},
		{36037595259469955, true, true, 0x20, 0x00, 0x20, 0x20, 0x20},
		{0x0080080080000102, false, true, 0x40, 0x00, 0x20, 0x20, 0x20},
		{0x0080080080000083, true, true, 0x20, 0x00, 0x20, 0x20, 0x20},
	}

	for _, tt := range tests {
		config := PacketEncoderConfigFromFlags(tt.flags)
		if config.ToFlags() != tt.flags {
			t.Errorf("ToFlags() = %v, want %v", config.ToFlags(), tt.flags)
		}
		if config.EncryptionEnabled != tt.expectedEncryption {
			t.Errorf("EncryptionEnabled = %v, want %v", config.EncryptionEnabled, tt.expectedEncryption)
		}
		if config.MacEnabled != tt.expectedMac {
			t.Errorf("MacEnabled = %v, want %v", config.MacEnabled, tt.expectedMac)
		}
		if config.MacDigestSize != tt.expectedDigestSize {
			t.Errorf("MacDigestSize = %v, want %v", config.MacDigestSize, tt.expectedDigestSize)
		}
		if config.MacPbkdf2Iterations != tt.expectedIteration {
			t.Errorf("MacPbkdf2Iterations = %v, want %v", config.MacPbkdf2Iterations, tt.expectedIteration)
		}
		if config.MacKeySize != tt.expectedMacKeySize {
			t.Errorf("MacKeySize = %v, want %v", config.MacKeySize, tt.expectedMacKeySize)
		}
		if config.EncryptionKeySize != tt.expectedKeySize {
			t.Errorf("EncryptionKeySize = %v, want %v", config.EncryptionKeySize, tt.expectedKeySize)
		}
		if config.RandomKeySize != tt.expectedRandomSize {
			t.Errorf("RandomKeySize = %v, want %v", config.RandomKeySize, tt.expectedRandomSize)
		}
	}
}

func TestToQuestFlags_ShiftedBy1(t *testing.T) {
	config := PacketEncoderConfig{
		EncryptionEnabled:   true,
		MacEnabled:          true,
		MacDigestSize:       0x20,
		MacPbkdf2Iterations: 0x00,
		MacKeySize:          0x20,
		EncryptionKeySize:   0x20,
		RandomKeySize:       0x20,
	}

	pcvrFlags := config.ToFlags()
	questFlags := config.ToQuestFlags()

	// Bit 0: Quest has "initialized" = 1, PCVR has EncryptionEnabled = 1
	if questFlags&1 != 1 {
		t.Error("Quest flags bit 0 (initialized) should be 1")
	}

	// Quest flags should always differ from PCVR flags (shifted layout)
	if pcvrFlags == questFlags {
		t.Errorf("Quest flags should differ from PCVR flags, both are 0x%016x", pcvrFlags)
	}

	// Verify Quest bit positions: EncryptionEnabled at bit 1, MacEnabled at bit 2
	if (questFlags>>1)&1 != 1 {
		t.Error("Quest EncryptionEnabled should be at bit 1")
	}
	if (questFlags>>2)&1 != 1 {
		t.Error("Quest MacEnabled should be at bit 2")
	}

	// MacDigestSize (0x20) at bits 3-14
	if (questFlags>>3)&0x0fff != 0x20 {
		t.Errorf("Quest MacDigestSize = 0x%x, want 0x20", (questFlags>>3)&0x0fff)
	}

	// MacKeySize (0x20) at bits 27-38
	if (questFlags>>27)&0x0fff != 0x20 {
		t.Errorf("Quest MacKeySize = 0x%x, want 0x20", (questFlags>>27)&0x0fff)
	}

	// EncryptionKeySize (0x20) at bits 39-50
	if (questFlags>>39)&0x0fff != 0x20 {
		t.Errorf("Quest EncryptionKeySize = 0x%x, want 0x20", (questFlags>>39)&0x0fff)
	}

	// RandomKeySize (0x20) at bits 51-62
	if (questFlags>>51)&0x0fff != 0x20 {
		t.Errorf("Quest RandomKeySize = 0x%x, want 0x20", (questFlags>>51)&0x0fff)
	}
}

func TestToQuestFlags_InitializedBitAlwaysSet(t *testing.T) {
	// Even with everything disabled/zero, bit 0 should be set
	config := PacketEncoderConfig{}
	questFlags := config.ToQuestFlags()
	if questFlags != 1 {
		t.Errorf("Empty config Quest flags = 0x%x, want 0x1 (initialized only)", questFlags)
	}
}

func TestToQuestFlags_RoundTrip_FieldValues(t *testing.T) {
	// Verify that each field in Quest layout doesn't overlap with adjacent fields
	configs := []PacketEncoderConfig{
		{EncryptionEnabled: true, MacEnabled: false, MacDigestSize: 0xfff},
		{EncryptionEnabled: false, MacEnabled: true, MacKeySize: 0xfff},
		{MacPbkdf2Iterations: 0xfff},
		{EncryptionKeySize: 0xfff},
		{RandomKeySize: 0xfff},
	}

	for i, cfg := range configs {
		flags := cfg.ToQuestFlags()
		// Verify initialized bit is always set
		if flags&1 != 1 {
			t.Errorf("case %d: initialized bit not set", i)
		}
		// Verify the flag value is non-zero beyond the initialized bit
		if flags <= 1 && (cfg.EncryptionEnabled || cfg.MacEnabled || cfg.MacDigestSize > 0 ||
			cfg.MacPbkdf2Iterations > 0 || cfg.MacKeySize > 0 || cfg.EncryptionKeySize > 0 || cfg.RandomKeySize > 0) {
			t.Errorf("case %d: flags should have more than just initialized bit, got 0x%x", i, flags)
		}
	}
}

func TestToQuestFlags_MaxFields_NoOverlap(t *testing.T) {
	// Set all 12-bit fields to max (0xfff) and verify no bit overlap
	config := PacketEncoderConfig{
		EncryptionEnabled:   true,
		MacEnabled:          true,
		MacDigestSize:       0x0fff,
		MacPbkdf2Iterations: 0x0fff,
		MacKeySize:          0x0fff,
		EncryptionKeySize:   0x0fff,
		RandomKeySize:       0x0fff,
	}

	questFlags := config.ToQuestFlags()

	// Extract each field and verify
	if (questFlags>>0)&1 != 1 {
		t.Error("initialized bit")
	}
	if (questFlags>>1)&1 != 1 {
		t.Error("encryption bit")
	}
	if (questFlags>>2)&1 != 1 {
		t.Error("mac bit")
	}
	if (questFlags>>3)&0x0fff != 0x0fff {
		t.Errorf("MacDigestSize = 0x%x", (questFlags>>3)&0x0fff)
	}
	if (questFlags>>15)&0x0fff != 0x0fff {
		t.Errorf("MacPbkdf2Iterations = 0x%x", (questFlags>>15)&0x0fff)
	}
	if (questFlags>>27)&0x0fff != 0x0fff {
		t.Errorf("MacKeySize = 0x%x", (questFlags>>27)&0x0fff)
	}
	if (questFlags>>39)&0x0fff != 0x0fff {
		t.Errorf("EncryptionKeySize = 0x%x", (questFlags>>39)&0x0fff)
	}
	if (questFlags>>51)&0x0fff != 0x0fff {
		t.Errorf("RandomKeySize = 0x%x", (questFlags>>51)&0x0fff)
	}

	// Verify total fits in 63 bits (bit 0 + 2 bool bits + 5*12 = 63 bits)
	if questFlags>>63 != 0 {
		t.Error("Quest flags overflow bit 63")
	}
}

func TestLobbySessionSuccessv5_Stream_UseQuestFlags(t *testing.T) {
	serverConfig := DefaultServerPacketConfig()
	clientConfig := DefaultClientPacketConfig()

	// Create two messages: one PCVR, one Quest
	pcvrMsg := &LobbySessionSuccessv5{
		GameMode:           0x1234,
		ServerEncoderFlags: serverConfig,
		ClientEncoderFlags: clientConfig,
		UseQuestFlags:      false,
	}
	questMsg := &LobbySessionSuccessv5{
		GameMode:           0x1234,
		ServerEncoderFlags: serverConfig,
		ClientEncoderFlags: clientConfig,
		UseQuestFlags:      true,
	}

	// Encode both
	pcvrStream := NewEasyStream(EncodeMode, []byte{})
	if err := pcvrMsg.Stream(pcvrStream); err != nil {
		t.Fatalf("PCVR stream failed: %v", err)
	}
	questStream := NewEasyStream(EncodeMode, []byte{})
	if err := questMsg.Stream(questStream); err != nil {
		t.Fatalf("Quest stream failed: %v", err)
	}

	pcvrBytes := pcvrStream.Bytes()
	questBytes := questStream.Bytes()

	// Both should be the same length (same fields, just different flag encoding)
	if len(pcvrBytes) != len(questBytes) {
		t.Fatalf("length mismatch: PCVR=%d, Quest=%d", len(pcvrBytes), len(questBytes))
	}

	// The bytes should differ (different flag encoding)
	if string(pcvrBytes) == string(questBytes) {
		t.Error("PCVR and Quest encodings should differ due to different flag layouts")
	}

	// Verify the encoder flags are at the expected offset.
	// Layout: GameMode(8) + LobbyID(16) + GroupID(16) + Endpoint(10) + TeamIndex(2) + SessionFlags(1) + Pad(3) = 56
	// encoderConfig0 starts at offset 56
	flagsOffset := 56
	pcvrServerFlags := binary.LittleEndian.Uint64(pcvrBytes[flagsOffset : flagsOffset+8])
	questServerFlags := binary.LittleEndian.Uint64(questBytes[flagsOffset : flagsOffset+8])

	if pcvrServerFlags != serverConfig.ToFlags() {
		t.Errorf("PCVR server flags = 0x%x, want 0x%x", pcvrServerFlags, serverConfig.ToFlags())
	}
	if questServerFlags != serverConfig.ToQuestFlags() {
		t.Errorf("Quest server flags = 0x%x, want 0x%x", questServerFlags, serverConfig.ToQuestFlags())
	}
}
