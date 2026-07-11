package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// JSON-encodes profile and seeds it as the current stored EVRProfile object
func seedEVRProfile(t *testing.T, nk *occTestNakamaModule, userID string, profile *EVRProfile) {
	t.Helper()
	data, err := json.Marshal(profile)
	if err != nil {
		t.Fatalf("marshal seed profile: %v", err)
	}
	nk.seedObject(userID, StorageCollectionEVRProfile, StorageKeyEVRProfile, string(data))
}

// stubs the extra NakamaModule methods
// EVRProfileUpdate(WithRetry) needs beyond StorageRead/Write
type failOnceAccountUpdateNoopNakamaModule struct {
	*failOnceNakamaModule
}

func (m *failOnceAccountUpdateNoopNakamaModule) AccountUpdateId(ctx context.Context, userID, username string, metadata map[string]interface{}, displayName, timezone, location, langTag, avatarUrl string) error {
	return nil
}

func (m *failOnceAccountUpdateNoopNakamaModule) StorageDelete(ctx context.Context, deletes []*runtime.StorageDelete) error {
	return nil
}

func (m *failOnceAccountUpdateNoopNakamaModule) AccountGetId(ctx context.Context, userID string) (*api.Account, error) {
	return &api.Account{User: &api.User{Id: userID}}, nil
}

// pins down EVRProfileUpdate's behavior, it drops the caller's mutation on conflict but still reports success
func TestEVRProfileUpdate_LosesMutationOnVersionConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	base := newOCCTestNakamaModule()

	// Concurrent writer already advanced the stored profile
	winner := &EVRProfile{}
	winner.TeamName = "concurrent-writer-value"
	seedEVRProfile(t, base, occTestUserID, winner)

	failing := &failOnceAccountUpdateNoopNakamaModule{&failOnceNakamaModule{occTestNakamaModule: base}}

	// Caller holds a stale copy and equips a cosmetic
	profile := &EVRProfile{}
	profile.LoadoutCosmetics.Loadout.Tag = "rwd_tag_s1_a_secondary"

	if err := EVRProfileUpdate(ctx, failing, occTestUserID, profile); err != nil {
		t.Fatalf("EVRProfileUpdate: %v", err)
	}

	final := &EVRProfile{}
	if err := StorableRead(ctx, failing, occTestUserID, final, false); err != nil {
		t.Fatalf("final read: %v", err)
	}
	if final.LoadoutCosmetics.Loadout.Tag == "rwd_tag_s1_a_secondary" {
		t.Fatal("expected EVRProfileUpdate to drop the equip on conflict; it persisted instead — this test's premise is stale")
	}
}

// this is the regression test for the fix, the same conflict must not lose the equip, and must still pick up the concurrent writer's own change
func TestEVRProfileUpdateWithRetry_PreservesCosmeticEquipOnConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	base := newOCCTestNakamaModule()

	winner := &EVRProfile{}
	winner.TeamName = "concurrent-writer-value"
	seedEVRProfile(t, base, occTestUserID, winner)

	failing := &failOnceAccountUpdateNoopNakamaModule{&failOnceNakamaModule{occTestNakamaModule: base}}

	profile := &EVRProfile{}
	const equippedTag = "rwd_tag_s1_a_secondary"
	reapply := func() error {
		profile.LoadoutCosmetics.Loadout.Tag = equippedTag
		return nil
	}
	if err := reapply(); err != nil {
		t.Fatalf("reapply: %v", err)
	}

	if err := EVRProfileUpdateWithRetry(ctx, failing, occTestUserID, profile, reapply); err != nil {
		t.Fatalf("EVRProfileUpdateWithRetry: %v", err)
	}

	final := &EVRProfile{}
	if err := StorableRead(ctx, failing, occTestUserID, final, false); err != nil {
		t.Fatalf("final read: %v", err)
	}
	if final.LoadoutCosmetics.Loadout.Tag != equippedTag {
		t.Errorf("cosmetic equip was lost on version conflict: got tag %q, want %q", final.LoadoutCosmetics.Loadout.Tag, equippedTag)
	}
	if final.TeamName != "concurrent-writer-value" {
		t.Errorf("concurrent writer's change was clobbered: got TeamName %q, want %q", final.TeamName, "concurrent-writer-value")
	}
}
