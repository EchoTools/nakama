package server

import (
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
)

// gameServerSaveLoadoutRequest writes the profile through
// evrProfileUpdateWithRetry, so its apply closure can run a second time against a
// profile re-read after a version conflict. applyGameServerLoadout is that
// closure's body.
//
// The clobber these tests guard against: if the BASE loadout is captured from the
// profile read before the conflict, a retry writes every slot of that stale copy
// back, undoing whatever the concurrent writer committed — precisely what
// evrProfileUpdateWithRetry exists to prevent. Only the slots the request
// actually named may change.

const retryTestOwnedTag = "rwd_tag_s1_vrml_s1_finalist"

func newLoadoutRetryProfile(loadout evr.CosmeticLoadout) *EVRProfile {
	return &EVRProfile{
		account: &api.Account{
			Wallet: `{"cosmetic:arena:` + retryTestOwnedTag + `":1}`,
		},
		LoadoutCosmetics: AccountCosmetics{Loadout: loadout},
	}
}

// TestApplyGameServerLoadout_UsesBaseFromProfilePassedIn is the core of finding 3.
//
// The game server equips one slot (emote). While that write is in flight, another
// writer equips the player's legitimately owned VRML tag and commits first. Our
// write conflicts and is retried against the re-read profile. The tag must
// survive: this request never mentioned the tag slot.
func TestApplyGameServerLoadout_UsesBaseFromProfilePassedIn(t *testing.T) {
	equips := []gameServerLoadoutEquip{
		{slot: "emote", equipped: evr.DefaultCosmeticLoadout().Emote},
	}

	// The profile as re-read AFTER the conflict: the concurrent writer's tag is on it.
	freshLoadout := evr.DefaultCosmeticLoadout()
	freshLoadout.Tag = retryTestOwnedTag
	fresh := newLoadoutRetryProfile(freshLoadout)

	written, err := applyGameServerLoadout(fresh, equips, -1)
	require.NoError(t, err)

	require.Equal(t, retryTestOwnedTag, written.Tag,
		"a retry must compose the request's equips onto the FRESH loadout; the "+
			"concurrent writer's tag must not be overwritten")
	require.Equal(t, retryTestOwnedTag, fresh.LoadoutCosmetics.Loadout.Tag)
	require.Equal(t, evr.DefaultCosmeticLoadout().Emote, written.Emote,
		"the slot the request did name must still be applied")

	// Discriminator: the same equips against the PRE-conflict profile yield the
	// default tag. That is exactly what would have been written back had the base
	// loadout been captured before the conflict instead of taken from the argument.
	stale := newLoadoutRetryProfile(evr.DefaultCosmeticLoadout())
	staleWritten, err := applyGameServerLoadout(stale, equips, -1)
	require.NoError(t, err)
	require.Equal(t, evr.DefaultCosmeticLoadout().Tag, staleWritten.Tag)
	require.NotEqual(t, written.Tag, staleWritten.Tag,
		"the two profiles must produce different results, or this test proves nothing")
}

// TestApplyGameServerLoadout_StillStripsUnownedCosmetics pins that moving the base
// loadout into the closure did not weaken the COSMETIC-1 ownership check.
func TestApplyGameServerLoadout_StillStripsUnownedCosmetics(t *testing.T) {
	// An empty wallet: the player owns no VRML tag.
	profile := &EVRProfile{
		account:          &api.Account{Wallet: "{}"},
		LoadoutCosmetics: AccountCosmetics{Loadout: evr.DefaultCosmeticLoadout()},
	}

	equips := []gameServerLoadoutEquip{{slot: "tag", equipped: retryTestOwnedTag}}

	written, err := applyGameServerLoadout(profile, equips, -1)
	require.NoError(t, err)
	require.Equal(t, evr.DefaultCosmeticLoadout().Tag, written.Tag,
		"an unowned cosmetic must still be stripped (COSMETIC-1)")
}

// TestApplyGameServerLoadout_IsIdempotent pins evrProfileUpdateWithRetry's
// contract that apply may run more than once.
func TestApplyGameServerLoadout_IsIdempotent(t *testing.T) {
	equips := []gameServerLoadoutEquip{
		{slot: "tag", equipped: retryTestOwnedTag},
	}
	profile := newLoadoutRetryProfile(evr.DefaultCosmeticLoadout())

	first, err := applyGameServerLoadout(profile, equips, 7)
	require.NoError(t, err)
	second, err := applyGameServerLoadout(profile, equips, 7)
	require.NoError(t, err)

	require.Equal(t, first, second)
	require.Equal(t, int64(7), profile.LoadoutCosmetics.JerseyNumber)
}

// TestApplyGameServerLoadout_NegativeJerseyNumberLeavesItAlone pins the
// payload.Number >= 0 guard that moved into the helper.
func TestApplyGameServerLoadout_NegativeJerseyNumberLeavesItAlone(t *testing.T) {
	profile := newLoadoutRetryProfile(evr.DefaultCosmeticLoadout())
	profile.LoadoutCosmetics.JerseyNumber = 42

	_, err := applyGameServerLoadout(profile, nil, -1)
	require.NoError(t, err)
	require.Equal(t, int64(42), profile.LoadoutCosmetics.JerseyNumber)
}

// TestSetLoadoutSlot_ReportsUnknownSlots pins the return value the request handler
// uses to log "Unknown slot type" exactly once per request rather than once per
// write attempt.
func TestSetLoadoutSlot_ReportsUnknownSlots(t *testing.T) {
	var l evr.CosmeticLoadout

	require.True(t, setLoadoutSlot(&l, "tag", "some_tag"))
	require.Equal(t, "some_tag", l.Tag)

	require.False(t, setLoadoutSlot(&l, "not_a_real_slot", "whatever"),
		"an unrecognised slot must be reported, not silently ignored")
}
