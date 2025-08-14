package server

import (
	"reflect"
	"slices"

	"github.com/bits-and-blooms/bitset"
)

type GuildGroupRoles struct {
	Member           string `json:"member"`
	Enforcer         string `json:"moderator"`
	Auditor          string `json:"auditor"`
	ServerHost       string `json:"server_host"`
	Allocator        string `json:"allocator"`
	Suspended        string `json:"suspended"`
	APIAccess        string `json:"api_access"`
	AccountAgeBypass string `json:"account_age_bypass"`
	VPNBypass        string `json:"vpn_bypass"`
	AccountLinked    string `json:"headset_linked"`
	UsernameOnly     string `json:"username_only"`
}

// Roles returns a slice of role IDs
func (r *GuildGroupRoles) AsSlice() []string {
	v := reflect.ValueOf(*r)
	roles := make([]string, 0, v.NumField())
	for i := 0; i < v.NumField(); i++ {
		role := v.Field(i).String()
		if role != "" {
			roles = append(roles, role)
		}
	}
	slices.Sort(roles)
	return slices.Compact(roles)
}

// Roles returns a slice of role IDs
func (r *GuildGroupRoles) AsSet() map[string]struct{} {
	v := reflect.ValueOf(*r)
	roleset := make(map[string]struct{}, v.NumField())
	for i := 0; i < v.NumField(); i++ {
		role := v.Field(i).String()
		if role != "" {
			roleset[role] = struct{}{}
		}
	}
	return roleset
}

type guildGroupPermissions struct {
	IsAllowedMatchmaking bool
	IsEnforcer           bool // Has kick/join/tc. access
	IsAuditor            bool // Can view audit logs and see extra info in /lookup
	IsServerHost         bool
	IsAllocator          bool // Can allocate servers with slash command
	IsSuspended          bool
	IsAPIAccess          bool
	IsAccountAgeBypass   bool
	IsVPNBypass          bool
}

func (m guildGroupPermissions) ToUint64() uint64 {
	var i uint64 = 0
	b := m.asBitSet()
	if len(b.Bytes()) == 0 {
		return i
	}
	return uint64(b.Bytes()[0])
}

func (m *guildGroupPermissions) FromUint64(v uint64) {
	b := bitset.From([]uint64{v})
	m.FromBitSet(b)
}

func (m *guildGroupPermissions) FromBitSet(b *bitset.BitSet) {

	value := reflect.ValueOf(m).Elem()
	for i := 0; i < value.NumField(); i++ {
		field := value.Field(i)
		if field.Kind() != reflect.Bool {
			continue
		}
		field.SetBool(b.Test(uint(i)))
	}
}

func (m guildGroupPermissions) asBitSet() *bitset.BitSet {
	value := reflect.ValueOf(m)

	b := bitset.New(uint(value.NumField()))
	for i := 0; i < value.NumField(); i++ {
		field := value.Field(i)
		if field.Kind() != reflect.Bool {
			continue
		}
		if field.Bool() {
			b.Set(uint(i))
		}
	}

	return b
}
