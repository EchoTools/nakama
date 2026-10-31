package evr

import (
	"fmt"
)

// SNSEarlyQuitFeatureFlags represents the feature flags for the early quit system.
//
// Wire layout (0x01 bytes):
//
//	+0x00  uint8  Flags     (bitfield of feature flags)
type SNSEarlyQuitFeatureFlags struct {
	Flags uint8
}

// Feature flag bit positions within Flags, named for what the client reads.
//
// The client reads bit N of the stored byte through R15NetEarlyQuitFeatureFlagExpression
// (echovr.exe 0x140d960a0). Across the 26 Quest script libraries that use it, N is only 0, 1 or 5;
// nothing reads bit 2 or 3, so those are not sent (#712). The stock client stores the byte as
// byte & 0xdd (PC 0x1401618b0, Quest libr15.so 0x136ac24), which clears bits 1 and 5; nevr-runtime's
// client keeps every bit. Server-side switches (penalty enforcement, auto-report) are service settings,
// not bits of this byte.
const (
	EarlyQuitFlagLockoutBar   uint8 = 1 << 0 // One LOCKOUT-bar gate
	EarlyQuitFlagLockoutTimer uint8 = 1 << 1 // The "Lockout Timer" tooltip and the FIND MATCH lockout elements
	EarlyQuitFlagInfoUI       uint8 = 1 << 5 // The early-quit info and warning UI (EARLY QUIT INFO, LOYALTY TIER, scoreboard values)
)

func (m SNSEarlyQuitFeatureFlags) Token() string {
	return "SNSEarlyQuitFeatureFlags"
}

func (m *SNSEarlyQuitFeatureFlags) Symbol() Symbol {
	return ToSymbol(m.Token())
}

func (m *SNSEarlyQuitFeatureFlags) String() string {
	return fmt.Sprintf("%s(flags=0x%02x)", m.Token(), m.Flags)
}

func (m *SNSEarlyQuitFeatureFlags) Stream(s *EasyStream) error {
	return s.StreamByte(&m.Flags)
}

// DefaultEarlyQuitFeatureFlags returns the flags sent to every player: 0x23, bits 0, 1 and 5.
func DefaultEarlyQuitFeatureFlags() uint8 {
	return EarlyQuitFlagLockoutBar | EarlyQuitFlagLockoutTimer | EarlyQuitFlagInfoUI
}

// Helper accessors for the bitfield.

func (m *SNSEarlyQuitFeatureFlags) IsLockoutBarEnabled() bool {
	return m.Flags&EarlyQuitFlagLockoutBar != 0
}

func (m *SNSEarlyQuitFeatureFlags) IsLockoutTimerEnabled() bool {
	return m.Flags&EarlyQuitFlagLockoutTimer != 0
}

func (m *SNSEarlyQuitFeatureFlags) IsInfoUIEnabled() bool {
	return m.Flags&EarlyQuitFlagInfoUI != 0
}
