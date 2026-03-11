package evr

import (
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
