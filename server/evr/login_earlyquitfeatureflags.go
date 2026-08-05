package evr

import (
	"fmt"
)

// SNSEarlyQuitFeatureFlags represents the feature flags for the early quit system.
//
// Wire layout (0x01 bytes):
//   +0x00  uint8  Flags     (bitfield of feature flags)
type SNSEarlyQuitFeatureFlags struct {
	Flags uint8
}

// Feature flag bit positions within Flags.
const (
	EarlyQuitFlagEnabled              uint8 = 1 << 0 // Early quit system on/off
	EarlyQuitFlagSteadyPlayerTracking uint8 = 1 << 1 // Steady player tracking
	EarlyQuitFlagPenaltyEnforcement   uint8 = 1 << 2 // Penalty enforcement
	EarlyQuitFlagAutoReport           uint8 = 1 << 3 // Auto-report at max penalty
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

// DefaultEarlyQuitFeatureFlags returns the default enabled feature flags.
func DefaultEarlyQuitFeatureFlags() uint8 {
	return EarlyQuitFlagEnabled | EarlyQuitFlagSteadyPlayerTracking |
		EarlyQuitFlagPenaltyEnforcement | EarlyQuitFlagAutoReport
}

// Helper accessors for the bitfield.

func (m *SNSEarlyQuitFeatureFlags) IsEnabled() bool {
	return m.Flags&EarlyQuitFlagEnabled != 0
}

func (m *SNSEarlyQuitFeatureFlags) SetEnabled(v bool) {
	if v {
		m.Flags |= EarlyQuitFlagEnabled
	} else {
		m.Flags &^= EarlyQuitFlagEnabled
	}
}

func (m *SNSEarlyQuitFeatureFlags) IsPenaltyEnforcementEnabled() bool {
	return m.Flags&EarlyQuitFlagPenaltyEnforcement != 0
}

func (m *SNSEarlyQuitFeatureFlags) IsAutoReportEnabled() bool {
	return m.Flags&EarlyQuitFlagAutoReport != 0
}
