package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// findRPCRegistration returns the registration for the given RPC ID, or nil.
func findRPCRegistration(regs []RPCRegistration, id string) *RPCRegistration {
	for i := range regs {
		if regs[i].ID == id {
			return &regs[i]
		}
	}
	return nil
}

// TestRegistration_PlayerReportRegistered guards against the regression where the
// player/report button 404s because there is no registration entry for its handler.
func TestRegistration_PlayerReportRegistered(t *testing.T) {
	regs := buildEVRRPCRegistrations(nil, nil)

	reg := findRPCRegistration(regs, "player/report")
	require.NotNil(t, reg, "player/report must be registered or the client button 404s")

	require.NotNil(t, reg.Handler, "player/report must have a handler")
	require.NotNil(t, reg.Permission, "player/report must declare a permission")

	// Any authenticated user may report; the handler enforces rate-limiting,
	// self-report rejection, and validation.
	assert.True(t, reg.Permission.RequireAuth, "player/report must require auth")
	assert.Empty(t, reg.Permission.AllowedGroups,
		"player/report must not restrict by group; validation is handler-enforced")
}

// TestRegistration_MatchAllocateDelegatesToHandler guards against the regression
// where match/allocate's middleware restricted access to global operators/bots,
// rejecting guild allocator-role users before the handler's own IsAllocator check
// could run. The middleware must delegate the gate to the handler (mirroring
// match/prepare) by leaving AllowedGroups empty.
func TestRegistration_MatchAllocateDelegatesToHandler(t *testing.T) {
	regs := buildEVRRPCRegistrations(nil, nil)

	reg := findRPCRegistration(regs, "match/allocate")
	require.NotNil(t, reg, "match/allocate must be registered")
	require.NotNil(t, reg.Permission, "match/allocate must declare a permission")

	assert.True(t, reg.Permission.RequireAuth, "match/allocate must require auth")
	// An empty AllowedGroups delegates the allocator gate to the handler's
	// IsAllocator check, allowing guild allocator-role users through the
	// middleware. A group-restricted list would reject them prematurely.
	assert.Empty(t, reg.Permission.AllowedGroups,
		"match/allocate must delegate the allocator gate to the handler (empty AllowedGroups), mirroring match/prepare")

	// Sanity: mirror match/prepare's permission shape exactly.
	prepare := findRPCRegistration(regs, "match/prepare")
	require.NotNil(t, prepare, "match/prepare must be registered")
	require.NotNil(t, prepare.Permission)
	assert.Equal(t, prepare.Permission.RequireAuth, reg.Permission.RequireAuth,
		"match/allocate must mirror match/prepare's RequireAuth")
	assert.Equal(t, prepare.Permission.AllowedGroups, reg.Permission.AllowedGroups,
		"match/allocate must mirror match/prepare's AllowedGroups")
}

// TestRegistration_NoDuplicateRPCIDs ensures the declarative table never
// registers the same endpoint ID twice (which would silently shadow a handler).
func TestRegistration_NoDuplicateRPCIDs(t *testing.T) {
	regs := buildEVRRPCRegistrations(nil, nil)

	seen := make(map[string]int, len(regs))
	for _, r := range regs {
		seen[r.ID]++
	}
	for id, n := range seen {
		assert.Equalf(t, 1, n, "RPC ID %q registered %d times; must be unique", id, n)
	}
}
