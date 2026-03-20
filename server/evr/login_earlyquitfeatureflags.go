package evr

import (
	"encoding/binary"
	"fmt"
)

// SNSEarlyQuitFeatureFlags represents the feature flags for the early quit system.
//
// Wire layout (0x08 bytes):
//   +0x00  uint32  Flags     (bitfield of feature flags)
//   +0x04  uint32  Reserved
type SNSEarlyQuitFeatureFlags struct {
	Flags    uint32
	Reserved uint32
}

// Feature flag bit positions within Flags.
const (
	EarlyQuitFlagEnabled             uint32 = 1 << 0 // Early quit system on/off
	EarlyQuitFlagSteadyPlayerTracking uint32 = 1 << 1 // Steady player tracking
	EarlyQuitFlagPenaltyEnforcement  uint32 = 1 << 2 // Penalty enforcement
	EarlyQuitFlagAutoReport          uint32 = 1 << 3 // Auto-report at max penalty
)

func (m SNSEarlyQuitFeatureFlags) Token() string {
	return "SNSEarlyQuitFeatureFlags"
}

func (m *SNSEarlyQuitFeatureFlags) Symbol() Symbol {
	return ToSymbol(m.Token())
}

func (m *SNSEarlyQuitFeatureFlags) String() string {
	return fmt.Sprintf("%s(flags=0x%08x)", m.Token(), m.Flags)
}

func (m *SNSEarlyQuitFeatureFlags) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Flags) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Reserved) },
	})
}

// DefaultEarlyQuitFeatureFlags returns the default enabled feature flags.
func DefaultEarlyQuitFeatureFlags() *SNSEarlyQuitFeatureFlags {
	return &SNSEarlyQuitFeatureFlags{
		Flags: EarlyQuitFlagEnabled | EarlyQuitFlagSteadyPlayerTracking |
			EarlyQuitFlagPenaltyEnforcement | EarlyQuitFlagAutoReport,
	}
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
