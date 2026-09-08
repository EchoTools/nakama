package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// vrmlWalletNK is a leaf double: it serves one account wallet and counts the
// AccountGetId calls, which is the whole point of the consolidation — the
// revocation and the assignment must be computed from ONE read.
//
// It embeds a nil runtime.NakamaModule (AGENTS.md defect class 5), so any method
// the code under test calls that is not defined here panics rather than silently
// no-opping. It defines no method that another method of its own calls, so
// defect class 1 (embedding does not dispatch virtually) cannot apply to it.
type vrmlWalletNK struct {
	runtime.NakamaModule
	wallet map[string]int64

	accountGets  int
	walletCalls  []map[string]int64
	multiUpdates int
}

func (m *vrmlWalletNK) AccountGetId(ctx context.Context, userID string) (*api.Account, error) {
	m.accountGets++
	data, err := json.Marshal(m.wallet)
	if err != nil {
		return nil, err
	}
	return &api.Account{Wallet: string(data)}, nil
}

func (m *vrmlWalletNK) WalletUpdate(ctx context.Context, userID string, changeset map[string]int64, metadata map[string]any, updateLedger bool) (map[string]int64, map[string]int64, error) {
	m.walletCalls = append(m.walletCalls, changeset)
	return nil, nil, nil
}

func (m *vrmlWalletNK) MultiUpdate(ctx context.Context, accountUpdates []*runtime.AccountUpdate, storageWrites []*runtime.StorageWrite, storageDeletes []*runtime.StorageDelete, walletUpdates []*runtime.WalletUpdate, updateLedger bool) ([]*api.StorageObjectAck, []*runtime.WalletUpdateResult, error) {
	m.multiUpdates++
	return nil, nil, nil
}

// TestVRMLEntitlementChangesets_Disjoint pins the property that licenses
// computing both changesets from a single wallet read: the revocation set and
// the assignment set never touch the same wallet key.
//
// Revocation keys are, by construction, exactly the known VRML cosmetics NOT in
// the entitled set; assignment keys are exactly the entitled set. If that ever
// stops holding, the assignment changeset would have to be recomputed after the
// revocation write and the two could no longer share a transaction.
func TestVRMLEntitlementChangesets_Disjoint(t *testing.T) {
	// A wallet holding every VRML cosmetic, so revocation has the widest
	// possible reach and any overlap would show up.
	wallet := make(map[string]int64)
	for _, key := range AllVRMLCosmetics() {
		wallet[key] = 1
	}

	for _, tc := range []struct {
		name         string
		entitlements []*VRMLEntitlement
	}{
		{"none", nil},
		{"one season player", []*VRMLEntitlement{{SeasonID: VRMLSeason5, Prestige: VRMLPlayer}}},
		{"champion", []*VRMLEntitlement{{SeasonID: VRMLSeason1, Prestige: VRMLChampion}}},
		{"multi season", []*VRMLEntitlement{
			{SeasonID: VRMLPreSeason, Prestige: VRMLPlayer},
			{SeasonID: VRMLSeason3, Prestige: VRMLFinalist},
			{SeasonID: VRMLSeason7, Prestige: VRMLChampion},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			revoke := VRMLRevocationChangeset(wallet, tc.entitlements)
			assign := VRMLAssignmentChangeset(wallet, tc.entitlements)

			for key := range revoke {
				if _, ok := assign[key]; ok {
					t.Fatalf("key %q is in both the revocation and the assignment changeset; the two writes are not disjoint and cannot share one wallet read", key)
				}
			}
			for key := range assign {
				if _, ok := revoke[key]; ok {
					t.Fatalf("key %q is in both the assignment and the revocation changeset; the two writes are not disjoint and cannot share one wallet read", key)
				}
			}
		})
	}
}

// TestVRMLEntitlementChangesets_MatchSequentialApplication proves the batched
// form is arithmetically identical to the old revoke-then-assign sequence: apply
// the revocation to the wallet, recompute the assignment from that mutated
// wallet, and the result must equal the assignment computed from the ORIGINAL
// wallet. That is the read-after-write the consolidation removes.
func TestVRMLEntitlementChangesets_MatchSequentialApplication(t *testing.T) {
	entitlements := []*VRMLEntitlement{
		{SeasonID: VRMLSeason2, Prestige: VRMLFinalist},
		{SeasonID: VRMLSeason6, Prestige: VRMLPlayer},
	}

	// A wallet with a mixture: some entitled keys already correct, some stale
	// keys from a previously linked account, some at odd values.
	wallet := map[string]int64{}
	for i, key := range AllVRMLCosmetics() {
		wallet[key] = int64(i % 3) // 0, 1, 2, 0, 1, 2, ...
	}

	batchedAssign := VRMLAssignmentChangeset(wallet, entitlements)
	revoke := VRMLRevocationChangeset(wallet, entitlements)

	// Apply the revocation, exactly as updateWallets would.
	after := make(map[string]int64, len(wallet))
	for k, v := range wallet {
		after[k] = v
	}
	for k, delta := range revoke {
		after[k] += delta
	}

	sequentialAssign := VRMLAssignmentChangeset(after, entitlements)

	if len(batchedAssign) != len(sequentialAssign) {
		t.Fatalf("assignment changeset differs in size after revocation: batched=%d sequential=%d", len(batchedAssign), len(sequentialAssign))
	}
	for k, v := range sequentialAssign {
		if batchedAssign[k] != v {
			t.Fatalf("assignment for %q differs: computed from one read=%d, computed after the revocation write=%d", k, batchedAssign[k], v)
		}
	}
}

// TestVRMLEntitlementWalletUpdates_SingleRead asserts the consolidated builder
// reads the wallet once and returns both wallet operations, each carrying its
// own ledger metadata so the two actions stay distinguishable in wallet_ledger.
func TestVRMLEntitlementWalletUpdates_SingleRead(t *testing.T) {
	entitlements := []*VRMLEntitlement{{SeasonID: VRMLSeason4, Prestige: VRMLChampion}}

	// Hold a stale cosmetic the entitlements do not cover, so a revocation is
	// actually produced.
	stale := "cosmetic:arena:" + TagVRMLS1
	nk := &vrmlWalletNK{wallet: map[string]int64{stale: 1}}

	updates, err := VRMLEntitlementWalletUpdates(context.Background(), nk, "assigner", "assignerName", "user-1", "vrml-1", entitlements)
	if err != nil {
		t.Fatalf("VRMLEntitlementWalletUpdates: %v", err)
	}

	if nk.accountGets != 1 {
		t.Fatalf("wallet read %d times, want exactly 1 — the whole point of the consolidation", nk.accountGets)
	}
	if len(updates) != 2 {
		t.Fatalf("got %d wallet updates, want 2 (revocation + assignment)", len(updates))
	}

	revoke, assign := updates[0], updates[1]
	if revoke.Metadata["action"] != "revoke_non_entitled" {
		t.Fatalf("first update is not the revocation: metadata=%v", revoke.Metadata)
	}
	if _, ok := assign.Metadata["entitlements"]; !ok {
		t.Fatalf("second update is not the assignment: metadata=%v", assign.Metadata)
	}
	if revoke.UserID != "user-1" || assign.UserID != "user-1" {
		t.Fatalf("wallet updates target the wrong user: %q, %q", revoke.UserID, assign.UserID)
	}
	if got := revoke.Changeset[stale]; got != -1 {
		t.Fatalf("stale cosmetic %q revocation = %d, want -1", stale, got)
	}
	if got := assign.Changeset["cosmetic:arena:"+TagVRMLS4Champion]; got != 1 {
		t.Fatalf("entitled cosmetic assignment = %d, want 1", got)
	}
	// The revocation must never reach for a key the assignment grants.
	if _, ok := revoke.Changeset["cosmetic:arena:"+TagVRMLS4Champion]; ok {
		t.Fatalf("revocation touched an entitled key")
	}
}

// TestVRMLEntitlementWalletUpdates_NothingToRevoke: a clean wallet yields only
// the assignment, matching RevokeNonEntitledVRMLCosmetics's own early return on
// an empty changeset — no empty wallet_ledger row is written.
func TestVRMLEntitlementWalletUpdates_NothingToRevoke(t *testing.T) {
	entitlements := []*VRMLEntitlement{{SeasonID: VRMLSeason5, Prestige: VRMLPlayer}}
	nk := &vrmlWalletNK{wallet: map[string]int64{}}

	updates, err := VRMLEntitlementWalletUpdates(context.Background(), nk, "assigner", "assignerName", "user-1", "vrml-1", entitlements)
	if err != nil {
		t.Fatalf("VRMLEntitlementWalletUpdates: %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("got %d wallet updates, want 1 (assignment only)", len(updates))
	}
	if _, ok := updates[0].Metadata["entitlements"]; !ok {
		t.Fatalf("the single update is not the assignment: metadata=%v", updates[0].Metadata)
	}
}
