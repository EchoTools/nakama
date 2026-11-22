package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc/codes"
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

// errInviteStorageDeleteRejected is the error RuntimeGoNakamaModule.StorageDelete
// returns for a versioned delete that matched no row (core_storage.go:831, unwrapped
// to its cause by StorageDeleteObjects).
var errInviteStorageDeleteRejected = errors.New("Storage delete rejected - not found, version check failed, or permission denied.")

func (n *inviteTestNakama) StorageDelete(_ context.Context, dels []*runtime.StorageDelete) error {
	n.occTestNakamaModule.mu.Lock()
	defer n.occTestNakamaModule.mu.Unlock()
	for _, d := range dels {
		k := occStorageKey(d.UserID, d.Collection, d.Key)
		o, ok := n.occTestNakamaModule.objects[k]
		if ok && d.Version != "" && d.Version != o.Version {
			// What production returns: StorageDeleteObjects hands the runtime the cause of
			// its StatusError, a bare error, not the write sentinel (see inviteDeleteRejected).
			return errInviteStorageDeleteRejected
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

	if _, err := pruneInviteOwner(context.Background(), zap.NewNop(), w.nk, userA, listed.Value, listed.Version, w.now); err != nil {
		t.Fatal(err)
	}
	if got := w.ownerIndex(userA); strings.Join(got, ",") != "LIVE1,LIVE2" {
		t.Errorf("owner index = %v, want [LIVE1 LIVE2]", got)
	}
}

// deleteHookNK runs before runs before each StorageDelete, then deletes through the
// double. The method is defined on this type, so the sweep's call through
// runtime.NakamaModule reaches it.
type deleteHookNK struct {
	*inviteTestNakama
	before func(dels []*runtime.StorageDelete)
	// fail, when set, answers a delete of that collection with the error instead.
	failCollection string
	fail           error
}

func (n *deleteHookNK) StorageDelete(ctx context.Context, dels []*runtime.StorageDelete) error {
	if n.before != nil {
		n.before(dels)
	}
	for _, d := range dels {
		if n.fail != nil && d.Collection == n.failCollection {
			return n.fail
		}
	}
	return n.inviteTestNakama.StorageDelete(ctx, dels)
}

// Every shape a rejected versioned delete can reach the sweep in.
func rejectedDeleteErrors() map[string]error {
	return map[string]error{
		"production: bare cause": errInviteStorageDeleteRejected,
		"status error":           StatusError(codes.InvalidArgument, "Storage delete rejected.", errInviteStorageDeleteRejected),
		"wrapped status error":   fmt.Errorf("delete: %w", StatusError(codes.InvalidArgument, "Storage delete rejected.", errInviteStorageDeleteRejected)),
		"write version sentinel": runtime.ErrStorageRejectedVersion,
	}
}

// A stale version on a code delete skips that object; it does not abort the sweep.
func TestSweepInviteCodes_RejectedCodeDeleteSkipsTheObjectAndContinues(t *testing.T) {
	for name, rejection := range rejectedDeleteErrors() {
		t.Run(name, func(t *testing.T) {
			w := newInviteWorld(t)
			w.seedInvite("DEAD2", userA, StoredInvite{ExpiresAt: w.now.Add(-24 * time.Hour)})
			w.seedInvite("DEAD3", userA, StoredInvite{ExpiresAt: w.now.Add(-24 * time.Hour)})
			w.seedInvite("DEAD4", userB, StoredInvite{ExpiresAt: w.now.Add(-24 * time.Hour)})
			nk := &deleteHookNK{inviteTestNakama: w.nk}
			nk.before = func(dels []*runtime.StorageDelete) {
				if dels[0].Collection == InviteCodeCollection && dels[0].Key == "DEAD2" {
					nk.failCollection, nk.fail = InviteCodeCollection, rejection
				} else {
					nk.fail = nil
				}
			}
			res, err := sweepInviteCodes(context.Background(), zap.NewNop(), nk)
			if err != nil {
				t.Fatalf("a rejected delete aborted the sweep: %v", err)
			}
			if !w.hasInvite("DEAD2") {
				t.Error("the rejected code was removed anyway")
			}
			if w.hasInvite("DEAD3") || w.hasInvite("DEAD4") {
				t.Error("the sweep stopped at the rejected code: later dead codes are still stored")
			}
			if res.Codes != 2 {
				t.Errorf("codes removed = %d, want 2", res.Codes)
			}
			// Owner pass still ran: userB's index went with its only code.
			if got := w.ownerIndex(userB); len(got) != 0 {
				t.Errorf("userB's owner index = %v, want it pruned", got)
			}
		})
	}
}

// A stale version on an owner index delete leaves the index for the next sweep.
func TestSweepInviteCodes_RejectedOwnerDeleteLeavesTheIndexAndContinues(t *testing.T) {
	for name, rejection := range rejectedDeleteErrors() {
		t.Run(name, func(t *testing.T) {
			w := newInviteWorld(t)
			w.seedInvite("DEAD2", userA, StoredInvite{ExpiresAt: w.now.Add(-24 * time.Hour)})
			w.seedInvite("DEAD3", userB, StoredInvite{ExpiresAt: w.now.Add(-24 * time.Hour)})
			nk := &deleteHookNK{inviteTestNakama: w.nk}
			nk.before = func(dels []*runtime.StorageDelete) {
				if dels[0].Collection == InviteOwnerCollection && dels[0].Key == userA {
					nk.failCollection, nk.fail = InviteOwnerCollection, rejection
				} else {
					nk.fail = nil
				}
			}
			res, err := sweepInviteCodes(context.Background(), zap.NewNop(), nk)
			if err != nil {
				t.Fatalf("a rejected owner delete aborted the sweep: %v", err)
			}
			if n := len(w.ownerIndex(userB)); n != 0 {
				t.Errorf("userB's index was not pruned after userA's was rejected (%d codes left)", n)
			}
			if res.Owners != 1 {
				t.Errorf("owner indexes changed = %d, want 1", res.Owners)
			}
			// The next sweep, with no rejection, finishes the job.
			w.sweep()
			if n := w.nk.objectCount(InviteOwnerCollection); n != 0 {
				t.Errorf("owner indexes left after the next sweep = %d, want 0", n)
			}
		})
	}
}

// A revoke that lands between the list and the delete wins: the version-guarded
// delete is rejected, the revoked object stays, and the next sweep removes it.
func TestSweepInviteCodes_RevokeRacingTheDeleteIsNotLost(t *testing.T) {
	w := newInviteWorld(t)
	w.seedInvite("DEAD2", userA, StoredInvite{ExpiresAt: w.now.Add(-24 * time.Hour)})
	raced := false
	nk := &deleteHookNK{inviteTestNakama: w.nk}
	nk.before = func(dels []*runtime.StorageDelete) {
		if raced || dels[0].Collection != InviteCodeCollection {
			return
		}
		raced = true
		objs, err := w.nk.StorageRead(context.Background(), []*runtime.StorageRead{{Collection: InviteCodeCollection, Key: "DEAD2", UserID: SystemUserID}})
		if err != nil || len(objs) != 1 {
			t.Errorf("race setup: %v %d", err, len(objs))
			return
		}
		inv := &StoredInvite{}
		if err := json.Unmarshal([]byte(objs[0].Value), inv); err != nil {
			t.Errorf("race setup: %v", err)
			return
		}
		inv.Revoked = true // what invite/revoke writes, under the version it read
		if err := writeInvite(context.Background(), w.nk, "DEAD2", inv, objs[0].Version); err != nil {
			t.Errorf("race setup: revoke: %v", err)
		}
	}
	if _, err := sweepInviteCodes(context.Background(), zap.NewNop(), nk); err != nil {
		t.Fatalf("the race aborted the sweep: %v", err)
	}
	if !raced {
		t.Fatal("the revoke never raced the delete")
	}
	inv, err := loadInvite(context.Background(), w.nk, "DEAD2")
	if err != nil || inv == nil || !inv.Revoked {
		t.Fatalf("after the raced sweep the code = %+v (%v), want it kept and revoked", inv, err)
	}
	w.sweep()
	if w.hasInvite("DEAD2") {
		t.Error("the next sweep did not remove the revoked code")
	}
	if n := w.nk.objectCount(InviteOwnerCollection); n != 0 {
		t.Errorf("owner indexes left = %d, want 0", n)
	}
}

// One unreadable code object does not stop the owner pass: the owner after it
// still has its dead code pruned.
func TestSweepInviteCodes_UnreadableCodeDoesNotStopOwnerPruning(t *testing.T) {
	w := newInviteWorld(t)
	// userA sorts before userB, so its index is pruned first.
	w.seedInvite("DEAD2", userA, StoredInvite{ExpiresAt: w.now.Add(-24 * time.Hour)})
	w.seedInvite("DEAD3", userB, StoredInvite{ExpiresAt: w.now.Add(-24 * time.Hour)})
	w.nk.seedObject(SystemUserID, InviteCodeCollection, "BADJS", "{not json")
	if err := updateInviteOwner(context.Background(), w.nk, userA, func(idx *storedInviteOwner) {
		idx.Codes = append(idx.Codes, "BADJS")
	}); err != nil {
		t.Fatal(err)
	}

	res, err := sweepInviteCodes(context.Background(), zap.NewNop(), w.nk)
	if err != nil {
		t.Fatalf("an unreadable code aborted the sweep: %v", err)
	}
	if got := w.ownerIndex(userB); len(got) != 0 {
		t.Errorf("userB's index = %v, want it pruned although userA's index names an unreadable code", got)
	}
	if got := w.ownerIndex(userA); strings.Join(got, ",") != "BADJS" {
		t.Errorf("userA's index = %v, want [BADJS]: the dead code goes, the unreadable one stays", got)
	}
	if n := w.nk.objectCount(InviteCodeCollection); n != 1 {
		t.Errorf("codes left = %d, want only the unreadable one", n)
	}
	if res.Owners != 2 {
		t.Errorf("owner indexes changed = %d, want 2", res.Owners)
	}
}

// A sweep cut short by shutdown is not a failure to warn about.
func TestInviteCleanupLoop_ShutdownIsNotWarned(t *testing.T) {
	w := newInviteWorld(t)
	core, logs := observer.New(zapcore.DebugLevel)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	nk := &listCancelledNK{inviteTestNakama: w.nk}
	inviteCleanupLoop(ctx, zap.New(core), nk, time.Hour) // returns because ctx is done
	if n := nk.lists; n != 1 {
		t.Fatalf("StorageList calls = %d, want the one sweep to have run", n)
	}
	for _, e := range logs.All() {
		if e.Level >= zapcore.WarnLevel {
			t.Errorf("shutdown logged %s %q", e.Level, e.Message)
		}
	}
}

// A sweep that fails while the server is running is still warned about. The
// second sweep cancels the context, so the loop ends; only the first is warned.
func TestInviteCleanupLoop_FailureWhileRunningIsWarned(t *testing.T) {
	w := newInviteWorld(t)
	core, logs := observer.New(zapcore.DebugLevel)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	nk := &listCancelledNK{inviteTestNakama: w.nk, failWith: errors.New("storage down"), cancelOnList: 2, cancel: cancel}
	inviteCleanupLoop(ctx, zap.New(core), nk, time.Millisecond)
	if n := logs.FilterLevelExact(zapcore.WarnLevel).Len(); n != 1 {
		t.Errorf("warnings = %d, want 1 (the failure before shutdown)", n)
	}
}

// listCancelledNK fails every StorageList: with failWith when set, else with the
// context's own error. The listCancelledNK.cancel runs on list number cancelOnList.
type listCancelledNK struct {
	*inviteTestNakama
	lists        int
	failWith     error
	cancelOnList int
	cancel       func()
}

func (n *listCancelledNK) StorageList(ctx context.Context, _, _, _ string, _ int, _ string) ([]*api.StorageObject, string, error) {
	n.lists++
	if n.cancel != nil && n.lists == n.cancelOnList {
		n.cancel()
	}
	if n.failWith != nil {
		return nil, "", n.failWith
	}
	return nil, "", ctx.Err()
}
