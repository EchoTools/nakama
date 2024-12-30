package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGuildGroupMembership_ToUint64(t *testing.T) {
	tests := []struct {
		name       string
		membership GuildGroupMembership
		expected   uint64
	}{
		{
			name: "All flags set",
			membership: GuildGroupMembership{
				IsAllowedMatchmaking: true,
				IsModerator:          true,
				IsServerHost:         true,
				IsAllocator:          true,
				IsSuspended:          true,
				IsAPIAccess:          true,
				IsAccountAgeBypass:   true,
				IsVPNBypass:          true,
				IsHeadsetLinked:      true,
			},
			expected: uint64(511), // 111111111 in binary
		},
		{
			name: "No flags set",
			membership: GuildGroupMembership{
				IsAllowedMatchmaking: false,
				IsModerator:          false,
				IsServerHost:         false,
				IsAllocator:          false,
				IsSuspended:          false,
				IsAPIAccess:          false,
				IsAccountAgeBypass:   false,
				IsVPNBypass:          false,
				IsHeadsetLinked:      false,
			},
			expected: uint64(0),
		},
		{
			name: "Some flags set",
			membership: GuildGroupMembership{
				IsAllowedMatchmaking: true,
				IsModerator:          false,
				IsServerHost:         true,
				IsAllocator:          false,
				IsSuspended:          true,
				IsAPIAccess:          false,
				IsAccountAgeBypass:   true,
				IsVPNBypass:          false,
				IsHeadsetLinked:      true,
			},
			expected: uint64(341), // 101010101 in binary
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.membership.ToUint64()
			assert.Equal(t, tt.expected, result)
		})
	}
}
