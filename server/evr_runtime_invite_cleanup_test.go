package server

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

// StorageList and StorageDelete for the invite double: the cleanup is the first
// thing here that lists or deletes. They are defined on this leaf type, so a call
// through runtime.NakamaModule reaches them; the embedded base defines neither.
func (n *inviteTestNakama) StorageList(_ context.Context, _, userID, collection string, limit int, cursor string) ([]*api.StorageObject, string, error) {
	n.occTestNakamaModule.mu.Lock()
	defer n.occTestNakamaModule.mu.Unlock()
	var keys []string
	for _, o := range n.occTestNakamaModule.objects {
		if o.Collection == collection && o.UserId == userID && o.Key > cursor {
			keys = append(keys, o.Key)
		}
	}
	sort.Strings(keys)
	next := ""
	if len(keys) > limit {
		keys, next = keys[:limit], keys[limit-1]
	}
	out := make([]*api.StorageObject, 0, len(keys))
	for _, k := range keys {
		o := n.occTestNakamaModule.objects[occStorageKey(userID, collection, k)]
		out = append(out, &api.StorageObject{Collection: o.Collection, Key: o.Key, UserId: o.UserId, Value: o.Value, Version: o.Version})
	}
	return out, next, nil
}

func (n *inviteTestNakama) StorageDelete(_ context.Context, dels []*runtime.StorageDelete) error {
	n.occTestNakamaModule.mu.Lock()
	defer n.occTestNakamaModule.mu.Unlock()
	for _, d := range dels {
		k := occStorageKey(d.UserID, d.Collection, d.Key)
		o, ok := n.occTestNakamaModule.objects[k]
		if ok && d.Version != "" && d.Version != o.Version {
			return runtime.ErrStorageRejectedVersion
		}
		delete(n.occTestNakamaModule.objects, k)
	}
	return nil
}

// seedInvite stores a code, as invite/create does, and lists it in its owner's index.
func (w *inviteTestWorld) seedInvite(code, owner string, inv StoredInvite) {
	w.t.Helper()
	inv.OwnerID = owner
	if err := writeInvite(context.Background(), w.nk, code, &inv, "*"); err != nil {
		w.t.Fatalf("seed %s: %v", code, err)
	}
	if err := updateInviteOwner(context.Background(), w.nk, owner, func(idx *storedInviteOwner) {
		idx.Codes = append(idx.Codes, code)
	}); err != nil {
		w.t.Fatalf("index %s: %v", code, err)
	}
}

func (w *inviteTestWorld) hasInvite(code string) bool {
	w.t.Helper()
	inv, err := loadInvite(context.Background(), w.nk, code)
	if err != nil {
		w.t.Fatal(err)
	}
	return inv != nil
}

func (w *inviteTestWorld) sweep() inviteSweepResult {
	w.t.Helper()
	res, err := sweepInviteCodes(context.Background(), zap.NewNop(), w.nk)
	if err != nil {
		w.t.Fatalf("sweep: %v", err)
	}
	return res
}

func TestSweepInviteCodes_RemovesDeadCodesAndKeepsLiveOnes(t *testing.T) {
	w := newInviteWorld(t)
	live := StoredInvite{ExpiresAt: w.now.Add(time.Hour)}
	w.seedInvite("LIVE2", userA, live)
	w.seedInvite("LIVE3", userA, StoredInvite{ExpiresAt: w.now.Add(10 * time.Minute)})
	w.seedInvite("DEAD2", userA, StoredInvite{ExpiresAt: w.now.Add(-2 * inviteExpiredRetention)})
	w.seedInvite("DEAD3", userA, StoredInvite{ExpiresAt: w.now.Add(-inviteExpiredRetention)}) // exactly at the retention
	w.seedInvite("REVOK", userA, StoredInvite{ExpiresAt: w.now.Add(time.Hour), Revoked: true})
	w.seedInvite("RECNT", userA, StoredInvite{ExpiresAt: w.now.Add(-time.Minute)}) // lapsed, still inside the retention

	res := w.sweep()

	for _, code := range []string{"DEAD2", "DEAD3", "REVOK"} {
		if w.hasInvite(code) {
			t.Errorf("%s: a dead code is still in InviteCodes", code)
		}
	}
	for _, code := range []string{"LIVE2", "LIVE3", "RECNT"} {
		if !w.hasInvite(code) {
			t.Errorf("%s: a code that is still live or was just lapsed was removed", code)
		}
	}
	if got := w.ownerIndex(userA); strings.Join(got, ",") != "LIVE2,LIVE3,RECNT" {
		t.Errorf("owner index = %v, want [LIVE2 LIVE3 RECNT]", got)
	}
	if res.Codes != 3 {
		t.Errorf("codes removed = %d, want 3", res.Codes)
	}
}

func TestSweepInviteCodes_DeletesAnOwnerIndexThatEmptiesAndOneThatPointsAtNothing(t *testing.T) {
	w := newInviteWorld(t)
	w.seedInvite("DEAD2", userA, StoredInvite{ExpiresAt: w.now.Add(-24 * time.Hour)})
	// userB's index names a code whose object is already gone.
	if err := updateInviteOwner(context.Background(), w.nk, userB, func(idx *storedInviteOwner) {
		idx.Codes = []string{"GONE2"}
	}); err != nil {
		t.Fatal(err)
	}
	w.sweep()
	if n := w.nk.objectCount(InviteOwnerCollection); n != 0 {
		t.Errorf("owner indexes left = %d, want 0", n)
	}
	if n := w.nk.objectCount(InviteCodeCollection); n != 0 {
		t.Errorf("codes left = %d, want 0", n)
	}
}

// The pile on production was cleaned in one sweep, across list pages.
func TestSweepInviteCodes_CleansAPileAcrossPages(t *testing.T) {
	w := newInviteWorld(t)
	const dead = 2*inviteCleanupPage + 37
	for i := range dead {
		w.seedInvite(fmt.Sprintf("D%04d", i), userA, StoredInvite{ExpiresAt: w.now.Add(-48 * time.Hour)})
	}
	w.seedInvite("LIVE2", userA, StoredInvite{ExpiresAt: w.now.Add(time.Hour)})

	w.sweep()

	if n := w.nk.objectCount(InviteCodeCollection); n != 1 || !w.hasInvite("LIVE2") {
		t.Errorf("codes left = %d, want only LIVE2", n)
	}
	if got := w.ownerIndex(userA); len(got) != 1 || got[0] != "LIVE2" {
		t.Errorf("owner index = %v, want [LIVE2]", got)
	}
}

func TestSweepInviteCodes_ASecondSweepChangesNothing(t *testing.T) {
	w := newInviteWorld(t)
	w.seedInvite("DEAD2", userA, StoredInvite{ExpiresAt: w.now.Add(-24 * time.Hour)})
	w.seedInvite("LIVE2", userA, StoredInvite{ExpiresAt: w.now.Add(time.Hour)})
	w.sweep()
	if res := w.sweep(); res != (inviteSweepResult{}) {
		t.Errorf("second sweep = %+v, want nothing", res)
	}
}

// A code created while the sweep runs is not dropped from the index by it.
func TestPruneInviteOwner_KeepsACodeAddedSinceTheList(t *testing.T) {
	w := newInviteWorld(t)
	w.seedInvite("DEAD2", userA, StoredInvite{ExpiresAt: w.now.Add(-24 * time.Hour)})
	w.seedInvite("LIVE1", userA, StoredInvite{ExpiresAt: w.now.Add(time.Hour)})
	objs, err := w.nk.StorageRead(context.Background(), []*runtime.StorageRead{{Collection: InviteOwnerCollection, Key: userA, UserID: SystemUserID}})
	if err != nil || len(objs) != 1 {
		t.Fatalf("read owner: %v %d", err, len(objs))
	}
	listed := objs[0]
	w.seedInvite("LIVE2", userA, StoredInvite{ExpiresAt: w.now.Add(time.Hour)}) // after the list

	if _, err := pruneInviteOwner(context.Background(), w.nk, userA, listed.Value, listed.Version, w.now); err != nil {
		t.Fatal(err)
	}
	if got := w.ownerIndex(userA); strings.Join(got, ",") != "LIVE1,LIVE2" {
		t.Errorf("owner index = %v, want [LIVE1 LIVE2]", got)
	}
}
