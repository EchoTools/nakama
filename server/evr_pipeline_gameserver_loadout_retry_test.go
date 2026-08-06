package server

import (
	"context"
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

// ---------------------------------------------------------------------------
// Call-site coverage.
//
// The tests above exercise applyGameServerLoadout as a unit. They say nothing
// about whether the handler WIRES it correctly, and that wiring is where the bug
// actually lives: the retry callback has to use the profile it is HANDED. Drop
// the parameter (`func(_ *EVRProfile)`) so the body closes over the caller's
// pre-conflict profile instead, and every unit test above still passes, because
// none of them ever runs a real version conflict through the real retry helper.
//
// TestPersistGameServerLoadout_* below drive persistGameServerLoadout end to end
// against an OCC-correct module that rejects the first write. That is the only
// thing in this package that can tell the two wirings apart.
// ---------------------------------------------------------------------------

// retryTestOwnedEmote is a second wallet-granted cosmetic. It differs from the
// default emote on purpose: equipping the DEFAULT value would leave the assertion
// true no matter which profile the callback composed onto, and prove nothing.
const retryTestOwnedEmote = "rwd_emote_test_owned_a"

// loadoutRetryModule is profileUpdateTestModule with a wallet on the account it
// returns, so the profile RELOADED after a conflict still passes the COSMETIC-1
// ownership check. Without it the reload yields an empty wallet and sanitizing
// would strip both cosmetics, hiding the behaviour under test.
type loadoutRetryModule struct {
	*profileUpdateTestModule
	wallet string
}

func (m *loadoutRetryModule) AccountGetId(ctx context.Context, userID string) (*api.Account, error) {
	return &api.Account{User: &api.User{Id: userID, Username: "tester"}, Wallet: m.wallet}, nil
}

func newLoadoutRetryModule() *loadoutRetryModule {
	return &loadoutRetryModule{
		profileUpdateTestModule: newProfileUpdateTestModule(),
		wallet: `{"cosmetic:arena:` + retryTestOwnedTag + `":1,` +
			`"cosmetic:arena:` + retryTestOwnedEmote + `":1}`,
	}
}

// TestPersistGameServerLoadout_RetryComposesEquipsOntoFreshProfile is the
// call-site test for finding 3.
//
// Scenario: a player on a NativeSupport game server equips an emote at the
// character customization screen. While that write is in flight another writer
// commits the player's legitimately owned VRML finalist tag. Our write loses the
// version race and is retried.
//
// Both halves must hold afterwards, and they fail under DIFFERENT miswirings:
//
//   - the tag must survive — a callback that composes onto a base loadout
//     captured BEFORE the conflict stamps all 22 stale slots back over it;
//   - the emote must be stored — a callback that ignores its parameter and
//     mutates the caller's profile leaves the re-read object untouched, so the
//     write succeeds having silently dropped the player's equip.
//
// Neither failure surfaces an error or a log line, which is why only an
// end-to-end assertion against stored state can catch them.
func TestPersistGameServerLoadout_RetryComposesEquipsOntoFreshProfile(t *testing.T) {
	ctx := context.Background()
	const userID = "77777777-7777-4777-8777-777777777777"

	nk := newLoadoutRetryModule()

	// Storage as it stood when the handler read the profile.
	base := evr.DefaultCosmeticLoadout()
	staleVersion := seedStoredProfile(t, nk.profileUpdateTestModule, userID,
		&EVRProfile{LoadoutCosmetics: AccountCosmetics{Loadout: base}})

	// The concurrent writer commits the tag first, bumping the stored version.
	concurrent := evr.DefaultCosmeticLoadout()
	concurrent.Tag = retryTestOwnedTag
	seedStoredProfile(t, nk.profileUpdateTestModule, userID,
		&EVRProfile{LoadoutCosmetics: AccountCosmetics{Loadout: concurrent}})

	// What the handler is holding: the pre-conflict read, carrying the now-stale
	// version and knowing nothing about the tag.
	inHand := newLoadoutRetryProfile(base)
	inHand.SetStorageMeta(StorableMetadata{Version: staleVersion})

	equips := []gameServerLoadoutEquip{{slot: "emote", equipped: retryTestOwnedEmote}}

	written, err := persistGameServerLoadout(ctx, nk, userID, inHand, equips, 8)
	require.NoError(t, err, "a version conflict must not cost the player the equip")

	// Precondition: the conflict really happened, so the retry path really ran.
	// Without this the assertions below would also pass on a first-try success.
	require.Equal(t, 2, nk.calls(),
		"expected one rejected write and one successful retry; the conflict is the "+
			"whole point of this test")

	stored := nk.storedProfile(t, userID)

	require.Equal(t, retryTestOwnedTag, stored.LoadoutCosmetics.Loadout.Tag,
		"the concurrent writer's tag must survive: the retry must compose onto the "+
			"RE-READ loadout, not stamp back the pre-conflict copy")
	require.Equal(t, retryTestOwnedEmote, stored.LoadoutCosmetics.Loadout.Emote,
		"the equip this request asked for must actually be stored: the retry "+
			"callback must mutate the profile it is HANDED, not the caller's")
	require.Equal(t, int64(8), stored.LoadoutCosmetics.JerseyNumber,
		"the jersey number must be re-applied to the fresh profile too")

	// The returned loadout is what the success log reports; it must describe what
	// landed in storage, not the attempt that lost.
	require.Equal(t, stored.LoadoutCosmetics.Loadout, written,
		"the returned loadout must match what was persisted")
}

// TestPersistGameServerLoadout_NoConflictWritesEquipsOnce is the control: with no
// concurrent writer, one write, and the equip lands. This is what makes the test
// above a statement about the RETRY rather than about persistence in general.
func TestPersistGameServerLoadout_NoConflictWritesEquipsOnce(t *testing.T) {
	ctx := context.Background()
	const userID = "88888888-8888-4888-8888-888888888888"

	nk := newLoadoutRetryModule()
	version := seedStoredProfile(t, nk.profileUpdateTestModule, userID,
		&EVRProfile{LoadoutCosmetics: AccountCosmetics{Loadout: evr.DefaultCosmeticLoadout()}})

	inHand := newLoadoutRetryProfile(evr.DefaultCosmeticLoadout())
	inHand.SetStorageMeta(StorableMetadata{Version: version})

	equips := []gameServerLoadoutEquip{{slot: "tag", equipped: retryTestOwnedTag}}

	written, err := persistGameServerLoadout(ctx, nk, userID, inHand, equips, -1)
	require.NoError(t, err)
	require.Equal(t, 1, nk.calls(), "an uncontended write must not retry")

	stored := nk.storedProfile(t, userID)
	require.Equal(t, retryTestOwnedTag, stored.LoadoutCosmetics.Loadout.Tag)
	require.Equal(t, retryTestOwnedTag, written.Tag)
}

// TestPersistGameServerLoadout_StripsUnownedCosmeticOnRetry pins that COSMETIC-1
// still holds on the retry path, where the ownership check runs against the
// RE-READ profile's wallet rather than the caller's.
func TestPersistGameServerLoadout_StripsUnownedCosmeticOnRetry(t *testing.T) {
	ctx := context.Background()
	const userID = "99999999-9999-4999-8999-999999999999"

	nk := newLoadoutRetryModule()
	nk.wallet = "{}" // the player owns nothing beyond the defaults

	staleVersion := seedStoredProfile(t, nk.profileUpdateTestModule, userID,
		&EVRProfile{LoadoutCosmetics: AccountCosmetics{Loadout: evr.DefaultCosmeticLoadout()}})
	seedStoredProfile(t, nk.profileUpdateTestModule, userID,
		&EVRProfile{LoadoutCosmetics: AccountCosmetics{Loadout: evr.DefaultCosmeticLoadout()}})

	inHand := newLoadoutRetryProfile(evr.DefaultCosmeticLoadout())
	inHand.SetStorageMeta(StorableMetadata{Version: staleVersion})

	equips := []gameServerLoadoutEquip{{slot: "tag", equipped: retryTestOwnedTag}}

	_, err := persistGameServerLoadout(ctx, nk, userID, inHand, equips, -1)
	require.NoError(t, err)
	require.Equal(t, 2, nk.calls(), "precondition: the retry path must have run")

	stored := nk.storedProfile(t, userID)
	require.Equal(t, evr.DefaultCosmeticLoadout().Tag, stored.LoadoutCosmetics.Loadout.Tag,
		"an unowned cosmetic must still be stripped on the retry path (COSMETIC-1)")
}
