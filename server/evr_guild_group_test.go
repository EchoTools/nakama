package server

import (
	"testing"
	"time"

	"github.com/bits-and-blooms/bitset"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
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
				IsModerator:          true,
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
				IsModerator:          false,
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
				IsModerator:          false,
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
				IsModerator:          true,
				IsServerHost:         true,
				IsAllocator:          true,
				IsSuspended:          true,
				IsAPIAccess:          true,
				IsAccountAgeBypass:   true,
				IsVPNBypass:          true,
				IsLimitedAccess:      true,
			},
		},
		{
			name:   "No flags set",
			bitset: bitset.From([]uint64{0}),
			expected: guildGroupPermissions{
				IsAllowedMatchmaking: false,
				IsModerator:          false,
				IsServerHost:         false,
				IsAllocator:          false,
				IsSuspended:          false,
				IsAPIAccess:          false,
				IsAccountAgeBypass:   false,
				IsVPNBypass:          false,
				IsLimitedAccess:      false,
			},
		},
		{
			name:   "Some flags set",
			bitset: bitset.From([]uint64{341}), // 101010101 in binary
			expected: guildGroupPermissions{
				IsAllowedMatchmaking: true,
				IsModerator:          false,
				IsServerHost:         true,
				IsAllocator:          false,
				IsSuspended:          true,
				IsAPIAccess:          false,
				IsAccountAgeBypass:   true,
				IsVPNBypass:          false,
				IsLimitedAccess:      true,
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
				IsModerator:          true,
				IsServerHost:         true,
				IsAllocator:          true,
				IsSuspended:          true,
				IsAPIAccess:          true,
				IsAccountAgeBypass:   true,
				IsVPNBypass:          true,
				IsLimitedAccess:      true,
			},
			expected: bitset.From([]uint64{511}), // 111111111 in binary
		},
		{
			name: "No flags set",
			membership: guildGroupPermissions{
				IsAllowedMatchmaking: false,
				IsModerator:          false,
				IsServerHost:         false,
				IsAllocator:          false,
				IsSuspended:          false,
				IsAPIAccess:          false,
				IsAccountAgeBypass:   false,
				IsVPNBypass:          false,
				IsLimitedAccess:      false,
			},
			expected: bitset.From([]uint64{0}),
		},
		{
			name: "Some flags set",
			membership: guildGroupPermissions{
				IsAllowedMatchmaking: true,
				IsModerator:          false,
				IsServerHost:         true,
				IsAllocator:          false,
				IsSuspended:          true,
				IsAPIAccess:          false,
				IsAccountAgeBypass:   true,
				IsVPNBypass:          false,
				IsLimitedAccess:      true,
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
func TestGroupMetadata_IsSuspended(t *testing.T) {

	user1 := uuid.Must(uuid.NewV4()).String()
	user2 := uuid.Must(uuid.NewV4()).String()

	tests := []struct {
		name      string
		groupMeta GroupMetadata
		userID    string
		xpid      evr.EvrId
		expected  bool
	}{
		{
			name: "User has suspended role",
			groupMeta: GroupMetadata{
				Roles: &GuildGroupRoles{
					Suspended: "suspended_role",
				},
				RoleCache: map[string]map[string]struct{}{
					"suspended_role": {
						user1: {},
					},
				},
			},
			userID:   user1,
			xpid:     evr.EvrId{PlatformCode: evr.OVR_ORG, AccountId: 12341234},
			expected: true,
		},
		{
			name: "User is suspended by device",
			groupMeta: GroupMetadata{
				Roles: &GuildGroupRoles{
					Suspended: "suspended_role",
				},
				Suspensions: map[evr.EvrId]string{
					evr.EvrId{PlatformCode: evr.OVR_ORG, AccountId: 12341234}: user1,
				},
				RoleCache: map[string]map[string]struct{}{
					"suspended_role": {
						user1: {},
					},
				},
			},
			userID:   user2,
			xpid:     evr.EvrId{PlatformCode: evr.OVR_ORG, AccountId: 12341234},
			expected: true,
		},
		{
			name: "User is not suspended",
			groupMeta: GroupMetadata{
				Roles: &GuildGroupRoles{
					Suspended: "suspended_role",
				},
				Suspensions: map[evr.EvrId]string{
					{PlatformCode: evr.OVR_ORG, AccountId: 12341234}: user2,
				},
				RoleCache: map[string]map[string]struct{}{
					"suspended_role": {
						user2: {},
					},
				},
			},
			userID:   user1,
			xpid:     evr.EvrId{PlatformCode: evr.OVR_ORG, AccountId: 56785678},
			expected: false,
		},
		{
			name: "User suspension removed",
			groupMeta: GroupMetadata{
				Roles: &GuildGroupRoles{
					Suspended: "suspended_role",
				},
				Suspensions: map[evr.EvrId]string{
					{PlatformCode: evr.OVR_ORG, AccountId: 12341234}: user1,
				},
				RoleCache: map[string]map[string]struct{}{
					"suspended_role": {},
				},
			},
			userID:   user1,
			xpid:     evr.EvrId{PlatformCode: evr.OVR_ORG, AccountId: 12341234},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.groupMeta.IsSuspended(tt.userID, &tt.xpid)
			assert.Equal(t, tt.expected, result)
		})
	}
}
func TestGroupMetadata_MarshalToMap(t *testing.T) {
	tests := []struct {
		name      string
		groupMeta GroupMetadata
		expected  map[string]interface{}
	}{

		{
			name: "GroupMetadata with values",
			groupMeta: GroupMetadata{
				CommunityValuesUserIDs: map[string]time.Time{
					"user1": time.Date(2025, 02, 11, 0, 0, 0, 0, time.UTC),
					"user2": time.Date(2023, 10, 01, 0, 0, 0, 0, time.UTC),
				},
			},
			expected: map[string]interface{}{
				"community_values_user_ids": map[string]interface{}{
					"user1": "2025-02-11T00:00:00Z",
					"user2": "2023-10-01T00:00:00Z",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tt.groupMeta.MarshalToMap()

			assert.Equal(t, tt.expected["community_values_user_ids"], result["community_values_user_ids"])

			assert.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}
