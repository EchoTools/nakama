package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// vrmlCommitNK records every write path a VRML verification pass could take, so
// a test can assert that all of them arrive in ONE MultiUpdate rather than as a
// sequence of independent writes.
//
// It embeds a nil runtime.NakamaModule (AGENTS.md defect class 5): a method the
// code under test calls that is not defined here panics instead of silently
// no-opping. No method it defines calls another of its own methods, so defect
// class 1 (Go embedding does not dispatch virtually, and a base method calling a
// sibling would bypass a subclass fault injection) cannot apply.
type vrmlCommitNK struct {
	runtime.NakamaModule
	wallet map[string]int64

	// failMultiUpdate, when set, is returned from MultiUpdate.
	failMultiUpdate error

	multiUpdateCalls  [][]*runtime.StorageWrite
	multiWalletCalls  [][]*runtime.WalletUpdate
	rawStorageWrites  int
	rawWalletUpdates  int
	updateLedgerFlags []bool
}

func (m *vrmlCommitNK) AccountGetId(ctx context.Context, userID string) (*api.Account, error) {
	data, err := json.Marshal(m.wallet)
	if err != nil {
		return nil, err
	}
	return &api.Account{Wallet: string(data)}, nil
}

func (m *vrmlCommitNK) StorageWrite(ctx context.Context, writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	m.rawStorageWrites++
	return nil, nil
}

func (m *vrmlCommitNK) WalletUpdate(ctx context.Context, userID string, changeset map[string]int64, metadata map[string]any, updateLedger bool) (map[string]int64, map[string]int64, error) {
	m.rawWalletUpdates++
	return nil, nil, nil
}

func (m *vrmlCommitNK) MultiUpdate(ctx context.Context, accountUpdates []*runtime.AccountUpdate, storageWrites []*runtime.StorageWrite, storageDeletes []*runtime.StorageDelete, walletUpdates []*runtime.WalletUpdate, updateLedger bool) ([]*api.StorageObjectAck, []*runtime.WalletUpdateResult, error) {
	m.multiUpdateCalls = append(m.multiUpdateCalls, storageWrites)
	m.multiWalletCalls = append(m.multiWalletCalls, walletUpdates)
	m.updateLedgerFlags = append(m.updateLedgerFlags, updateLedger)
	if m.failMultiUpdate != nil {
		return nil, nil, m.failMultiUpdate
	}
	acks := make([]*api.StorageObjectAck, 0, len(storageWrites))
	for _, w := range storageWrites {
		acks = append(acks, &api.StorageObjectAck{Collection: w.Collection, Key: w.Key, UserId: w.UserID, Version: "v1"})
	}
	return acks, nil, nil
}

func testVRMLLedger() *VRMLEntitlementLedger {
	return &VRMLEntitlementLedger{Entries: []*VRMLEntitlementLedgerEntry{{
		UserID:       "user-1",
		VRMLUserID:   "vrml-1",
		VRMLPlayerID: "player-1",
		Entitlements: []*VRMLEntitlement{{SeasonID: VRMLSeason5, Prestige: VRMLPlayer}},
	}}}
}

// TestCommitVRMLVerification_SingleTransaction is the task-3 assertion: the
// player summary, the entitlement ledger and both wallet changesets land in one
// MultiUpdate. Before this, the summary was a raw StorageWrite, the ledger was
// another raw StorageWrite, and the wallet was two more writes — four
// independent commits with three windows in which a crash left VRML state
// describing entitlements the wallet did not have.
func TestCommitVRMLVerification_SingleTransaction(t *testing.T) {
	entitlements := []*VRMLEntitlement{{SeasonID: VRMLSeason5, Prestige: VRMLPlayer}}
	// A stale cosmetic so a revocation is produced too.
	nk := &vrmlCommitNK{wallet: map[string]int64{"cosmetic:arena:" + TagVRMLS1: 1}}

	err := commitVRMLVerification(context.Background(), nk, "user-1", "vrml-1", []byte(`{"summary":true}`), testVRMLLedger(), entitlements)
	if err != nil {
		t.Fatalf("commitVRMLVerification: %v", err)
	}

	if nk.rawStorageWrites != 0 {
		t.Fatalf("%d raw StorageWrite calls, want 0 — every write must go through the transaction", nk.rawStorageWrites)
	}
	if nk.rawWalletUpdates != 0 {
		t.Fatalf("%d raw WalletUpdate calls, want 0 — every write must go through the transaction", nk.rawWalletUpdates)
	}
	if len(nk.multiUpdateCalls) != 1 {
		t.Fatalf("%d MultiUpdate calls, want exactly 1", len(nk.multiUpdateCalls))
	}

	writes := nk.multiUpdateCalls[0]
	if len(writes) != 2 {
		t.Fatalf("%d storage writes in the transaction, want 2 (summary + ledger)", len(writes))
	}
	byKey := map[string]*runtime.StorageWrite{}
	for _, w := range writes {
		byKey[w.Collection+"/"+w.Key] = w
	}
	summary, ok := byKey[StorageCollectionVRML+"/"+StorageKeyVRMLSummary]
	if !ok {
		t.Fatalf("summary write missing from the transaction: %v", byKey)
	}
	if summary.UserID != "user-1" || summary.Value != `{"summary":true}` {
		t.Fatalf("summary write is wrong: userID=%q value=%q", summary.UserID, summary.Value)
	}
	if summary.PermissionRead != 1 || summary.PermissionWrite != 0 {
		t.Fatalf("summary permissions changed: read=%d write=%d, want 1/0", summary.PermissionRead, summary.PermissionWrite)
	}
	ledgerWrite, ok := byKey[StorageCollectionVRML+"/"+StorageKeyVRMLVerificationLedger]
	if !ok {
		t.Fatalf("ledger write missing from the transaction: %v", byKey)
	}
	if ledgerWrite.UserID != SystemUserID {
		t.Fatalf("ledger write owner = %q, want the system user", ledgerWrite.UserID)
	}

	wallets := nk.multiWalletCalls[0]
	if len(wallets) != 2 {
		t.Fatalf("%d wallet updates in the transaction, want 2 (revocation + assignment)", len(wallets))
	}
	if !nk.updateLedgerFlags[0] {
		t.Fatal("updateLedger is false; the wallet_ledger rows the old WalletUpdate(…, true) calls wrote would be lost")
	}
}

// TestCommitVRMLVerification_FailurePropagates: the transaction is the unit, so
// a rejected commit must surface as an error rather than a partially applied
// pass. The caller (the verifier loop) skips the entry on error.
func TestCommitVRMLVerification_FailurePropagates(t *testing.T) {
	sentinel := errors.New("transaction rejected")
	nk := &vrmlCommitNK{wallet: map[string]int64{}, failMultiUpdate: sentinel}

	err := commitVRMLVerification(context.Background(), nk, "user-1", "vrml-1", []byte(`{}`), testVRMLLedger(), nil)
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want it to wrap %v with %%w", err, sentinel)
	}
	if len(nk.multiUpdateCalls) != 1 {
		t.Fatalf("%d MultiUpdate calls, want 1 — no retry, and no fallback to unbatched writes", len(nk.multiUpdateCalls))
	}
}

// TestVRMLEntitlementLedgerStore_UsesMultiUpdate covers the standalone ledger
// writer (the unlink path still calls it on its own).
func TestVRMLEntitlementLedgerStore_UsesMultiUpdate(t *testing.T) {
	nk := &vrmlCommitNK{wallet: map[string]int64{}}

	if err := VRMLEntitlementLedgerStore(context.Background(), nk, testVRMLLedger()); err != nil {
		t.Fatalf("VRMLEntitlementLedgerStore: %v", err)
	}
	if nk.rawStorageWrites != 0 {
		t.Fatalf("%d raw StorageWrite calls, want 0", nk.rawStorageWrites)
	}
	if len(nk.multiUpdateCalls) != 1 || len(nk.multiUpdateCalls[0]) != 1 {
		t.Fatalf("want one MultiUpdate carrying one write, got %v", nk.multiUpdateCalls)
	}
	w := nk.multiUpdateCalls[0][0]
	if w.Collection != StorageCollectionVRML || w.Key != StorageKeyVRMLVerificationLedger || w.UserID != SystemUserID {
		t.Fatalf("ledger write targets the wrong object: %+v", w)
	}
	if w.PermissionRead != 0 || w.PermissionWrite != 0 {
		t.Fatalf("ledger permissions changed: read=%d write=%d, want 0/0", w.PermissionRead, w.PermissionWrite)
	}
}
