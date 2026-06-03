package server

import (
	"testing"

	"github.com/bits-and-blooms/bitset"
	"github.com/stretchr/testify/assert"
)

func TestGuildGroupMembership_ToUint64(t *testing.T) {
	tests := []struct {
		name       string
		membership guildGroupPermissions
		expected   uint64
	}{
		{
			// 9 permission bits since IsAuditor was added: 0x1ff == 0b111111111.
			name: "All flags set",
			membership: guildGroupPermissions{
				IsAllowedMatchmaking: true,
				IsEnforcer:           true,
				IsAuditor:            true,
				IsServerHost:         true,
				IsAllocator:          true,
				IsSuspended:          true,
				IsAPIAccess:          true,
				IsAccountAgeBypass:   true,
				IsVPNBypass:          true,
			},
			expected: uint64(0x1ff),
		},
		{
			name:       "No flags set",
			membership: guildGroupPermissions{},
			expected:   uint64(0),
		},
		{
			// bits 0,3,5,7 set (matchmaking, serverhost, suspended, accountage).
			name: "Some flags set",
			membership: guildGroupPermissions{
				IsAllowedMatchmaking: true,
				IsServerHost:         true,
				IsSuspended:          true,
				IsAccountAgeBypass:   true,
			},
			expected: uint64(0xa9),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.membership.ToUint64()
			assert.Equal(t, tt.expected, result)
		})
	}
}
func TestGuildGroupMembership_FromBitSet(t *testing.T) {
	tests := []struct {
		name     string
		bitset   *bitset.BitSet
		expected guildGroupPermissions
	}{
		{
			// 0x1ff sets all 9 permission bits (IsAuditor included).
			name:   "All flags set",
			bitset: bitset.From([]uint64{0x1ff}),
			expected: guildGroupPermissions{
				IsAllowedMatchmaking: true,
				IsEnforcer:           true,
				IsAuditor:            true,
				IsServerHost:         true,
				IsAllocator:          true,
				IsSuspended:          true,
				IsAPIAccess:          true,
				IsAccountAgeBypass:   true,
				IsVPNBypass:          true,
			},
		},
		{
			name:     "No flags set",
			bitset:   bitset.From([]uint64{0}),
			expected: guildGroupPermissions{},
		},
		{
			// 0x155 == bits 0,2,4,6,8 (matchmaking, auditor, allocator, apiaccess, vpn).
			name:   "Some flags set",
			bitset: bitset.From([]uint64{0x155}),
			expected: guildGroupPermissions{
				IsAllowedMatchmaking: true,
				IsAuditor:            true,
				IsAllocator:          true,
				IsAPIAccess:          true,
				IsVPNBypass:          true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var membership guildGroupPermissions
			membership.FromBitSet(tt.bitset)
			assert.Equal(t, tt.expected, membership)
		})
	}
}
func TestGuildGroupMembership_asBitSet(t *testing.T) {
	tests := []struct {
		name       string
		membership guildGroupPermissions
		expected   *bitset.BitSet
	}{
		{
			name: "All flags set",
			membership: guildGroupPermissions{
				IsAllowedMatchmaking: true,
				IsEnforcer:           true,
				IsAuditor:            true,
				IsServerHost:         true,
				IsAllocator:          true,
				IsSuspended:          true,
				IsAPIAccess:          true,
				IsAccountAgeBypass:   true,
				IsVPNBypass:          true,
			},
			expected: bitset.From([]uint64{0x1ff}),
		},
		{
			name:       "No flags set",
			membership: guildGroupPermissions{},
			expected:   bitset.From([]uint64{0}),
		},
		{
			// bits 0,3,5,7 (matchmaking, serverhost, suspended, accountage).
			name: "Some flags set",
			membership: guildGroupPermissions{
				IsAllowedMatchmaking: true,
				IsServerHost:         true,
				IsSuspended:          true,
				IsAccountAgeBypass:   true,
			},
			expected: bitset.From([]uint64{0xa9}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.membership.asBitSet()
			if result.Difference(tt.expected).Any() {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}
