package service

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
			name: "All flags set",
			membership: guildGroupPermissions{
				IsAllowedMatchmaking: true,
				IsEnforcer:           true,
				IsServerHost:         true,
				IsAllocator:          true,
				IsSuspended:          true,
				IsAPIAccess:          true,
				IsAccountAgeBypass:   true,
				IsVPNBypass:          true,
			},
			expected: uint64(0xff), // 111111111 in binary
		},
		{
			name: "No flags set",
			membership: guildGroupPermissions{
				IsAllowedMatchmaking: false,
				IsEnforcer:           false,
				IsServerHost:         false,
				IsAllocator:          false,
				IsSuspended:          false,
				IsAPIAccess:          false,
				IsAccountAgeBypass:   false,
				IsVPNBypass:          false,
			},
			expected: uint64(0),
		},
		{
			name: "Some flags set",
			membership: guildGroupPermissions{
				IsAllowedMatchmaking: true,
				IsEnforcer:           false,
				IsServerHost:         true,
				IsAllocator:          false,
				IsSuspended:          true,
				IsAPIAccess:          false,
				IsAccountAgeBypass:   true,
				IsVPNBypass:          false,
			},
			expected: uint64(0x55), // 101010101 in binary
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
			name:   "All flags set",
			bitset: bitset.From([]uint64{511}), // 111111111 in binary
			expected: guildGroupPermissions{
				IsAllowedMatchmaking: true,
				IsEnforcer:           true,
				IsServerHost:         true,
				IsAllocator:          true,
				IsSuspended:          true,
				IsAPIAccess:          true,
				IsAccountAgeBypass:   true,
				IsVPNBypass:          true,
			},
		},
		{
			name:   "No flags set",
			bitset: bitset.From([]uint64{0}),
			expected: guildGroupPermissions{
				IsAllowedMatchmaking: false,
				IsEnforcer:           false,
				IsServerHost:         false,
				IsAllocator:          false,
				IsSuspended:          false,
				IsAPIAccess:          false,
				IsAccountAgeBypass:   false,
				IsVPNBypass:          false,
			},
		},
		{
			name:   "Some flags set",
			bitset: bitset.From([]uint64{341}), // 101010101 in binary
			expected: guildGroupPermissions{
				IsAllowedMatchmaking: true,
				IsEnforcer:           false,
				IsServerHost:         true,
				IsAllocator:          false,
				IsSuspended:          true,
				IsAPIAccess:          false,
				IsAccountAgeBypass:   true,
				IsVPNBypass:          false,
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
				IsServerHost:         true,
				IsAllocator:          true,
				IsSuspended:          true,
				IsAPIAccess:          true,
				IsAccountAgeBypass:   true,
				IsVPNBypass:          true,
			},
			expected: bitset.From([]uint64{511}), // 111111111 in binary
		},
		{
			name: "No flags set",
			membership: guildGroupPermissions{
				IsAllowedMatchmaking: false,
				IsEnforcer:           false,
				IsServerHost:         false,
				IsAllocator:          false,
				IsSuspended:          false,
				IsAPIAccess:          false,
				IsAccountAgeBypass:   false,
				IsVPNBypass:          false,
			},
			expected: bitset.From([]uint64{0}),
		},
		{
			name: "Some flags set",
			membership: guildGroupPermissions{
				IsAllowedMatchmaking: true,
				IsEnforcer:           false,
				IsServerHost:         true,
				IsAllocator:          false,
				IsSuspended:          true,
				IsAPIAccess:          false,
				IsAccountAgeBypass:   true,
				IsVPNBypass:          false,
			},
			expected: bitset.From([]uint64{341}), // 101010101 in binary
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
