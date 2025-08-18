package evr

import (
	"testing"
)

func TestPacketEncoderSettingsFromFlags(t *testing.T) {
	tests := []struct {
		flags              uint64
		expectedEncryption bool
		expectedMac        bool
		expectedDigestSize int
		expectedIteration  int
		expectedMacKeySize int
		expectedKeySize    int
		expectedRandomSize int
	}{
		{0, false, false, 0, 0, 0, 0, 0},                                // test with all flags disabled
		{1, true, false, 0, 0, 0, 0, 0},                                 // test with encryption flag enabled
		{2, false, true, 0, 0, 0, 0, 0},                                 // test with mac flag enabled
		{3, true, true, 0, 0, 0, 0, 0},                                  // test with both encryption and mac flags enabled
		{36037595259470083, true, true, 0x40, 0x00, 0x20, 0x20, 0x20},   // default client flags
		{36037595259469955, true, true, 0x20, 0x00, 0x20, 0x20, 0x20},   // default server flags
		{0x0080080080000102, false, true, 0x40, 0x00, 0x20, 0x20, 0x20}, // default client flags
		{0x0080080080000083, true, true, 0x20, 0x00, 0x20, 0x20, 0x20},  // default server flags
	}

	for _, tt := range tests {
		settings := PacketEncoderSettingsFromFlags(tt.flags)
		if settings.ToFlags() != tt.flags {
			t.Errorf("ToFlags() = %v, want %v", settings.ToFlags(), tt.flags)
		}
		if settings.EncryptionEnabled != tt.expectedEncryption {
			t.Errorf("EncryptionEnabled = %v, want %v", settings.EncryptionEnabled, tt.expectedEncryption)
		}
		if settings.MACEnabled != tt.expectedMac {
			t.Errorf("MacEnabled = %v, want %v", settings.MACEnabled, tt.expectedMac)
		}
		if settings.MACDigestSize != tt.expectedDigestSize {
			t.Errorf("MacDigestSize = %v, want %v", settings.MACDigestSize, tt.expectedDigestSize)
		}
		if settings.MACPBKDF2IterationCount != tt.expectedIteration {
			t.Errorf("MacPBKDF2IterationCount = %v, want %v", settings.MACPBKDF2IterationCount, tt.expectedIteration)
		}
		if settings.MACKeySize != tt.expectedMacKeySize {
			t.Errorf("MacKeySize = %v, want %v", settings.MACKeySize, tt.expectedMacKeySize)
		}
		if settings.EncryptionKeySize != tt.expectedKeySize {
			t.Errorf("EncryptionKeySize = %v, want %v", settings.EncryptionKeySize, tt.expectedKeySize)
		}
		if settings.RandomKeySize != tt.expectedRandomSize {
			t.Errorf("RandomKeySize = %v, want %v", settings.RandomKeySize, tt.expectedRandomSize)
		}
	}
}
