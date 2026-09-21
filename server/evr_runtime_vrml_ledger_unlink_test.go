package server

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// vrmlLedgerStoreNK keeps the system-owned VRML ledger object in memory and
// applies storage versions the way core_storage.go does: an empty Version is an
// unconditional write, "*" only creates, and any other value must match the
// stored version or the whole MultiUpdate is rejected with
// runtime.ErrStorageRejectedVersion. Every accepted write bumps the version.
//
// It embeds a nil runtime.NakamaModule (AGENTS.md defect class 5), so a call
// the code under test makes that is not defined here panics. No method here
// calls another of its own methods (defect class 1).
type vrmlLedgerStoreNK struct {
	runtime.NakamaModule

	ledgerValue   string
	ledgerVersion int

	// beforeVerifierCommit, when set, runs once, inside the first MultiUpdate
	// that carries the player summary (i.e. the verifier's commit), before
	// that commit is applied. It models a write that lands between the
	// verifier's read of the ledger and its write.
	beforeVerifierCommit func()

	verifierCommits int
}

func (m *vrmlLedgerStoreNK) StorageRead(ctx context.Context, reads []*runtime.StorageRead) ([]*api.StorageObject, error) {
	if m.ledgerVersion == 0 {
		return nil, nil
	}
	return []*api.StorageObject{{
		Collection: StorageCollectionVRML,
		Key:        StorageKeyVRMLVerificationLedger,
		UserId:     SystemUserID,
		Value:      m.ledgerValue,
		Version:    strconv.Itoa(m.ledgerVersion),
	}}, nil
}

func (m *vrmlLedgerStoreNK) MultiUpdate(ctx context.Context, accountUpdates []*runtime.AccountUpdate, storageWrites []*runtime.StorageWrite, storageDeletes []*runtime.StorageDelete, walletUpdates []*runtime.WalletUpdate, updateLedger bool) ([]*api.StorageObjectAck, []*runtime.WalletUpdateResult, error) {
	isVerifierCommit := false
	for _, w := range storageWrites {
		if w.Key == StorageKeyVRMLSummary {
			isVerifierCommit = true
		}
	}
	if isVerifierCommit && m.beforeVerifierCommit != nil {
		hook := m.beforeVerifierCommit
		m.beforeVerifierCommit = nil
		hook()
	}

	for _, w := range storageWrites {
		if w.Key != StorageKeyVRMLVerificationLedger {
			continue
		}
		switch w.Version {
		case "":
		case "*":
			if m.ledgerVersion != 0 {
				return nil, nil, runtime.ErrStorageRejectedVersion
			}
		default:
			if w.Version != strconv.Itoa(m.ledgerVersion) {
				return nil, nil, runtime.ErrStorageRejectedVersion
			}
		}
	}

	acks := make([]*api.StorageObjectAck, 0, len(storageWrites))
	for _, w := range storageWrites {
		ack := &api.StorageObjectAck{Collection: w.Collection, Key: w.Key, UserId: w.UserID, Version: "summary"}
		if w.Key == StorageKeyVRMLVerificationLedger {
			m.ledgerVersion++
			m.ledgerValue = w.Value
			ack.Version = strconv.Itoa(m.ledgerVersion)
		}
		acks = append(acks, ack)
	}
	if isVerifierCommit {
		m.verifierCommits++
	}
	return acks, nil, nil
}

// The unlink path's remaining calls. The wallet is empty, so there is nothing
// to revoke and WalletUpdate is never reached.
func (m *vrmlLedgerStoreNK) AccountGetId(ctx context.Context, userID string) (*api.Account, error) {
	return &api.Account{Wallet: "{}"}, nil
}

func (m *vrmlLedgerStoreNK) StorageDelete(ctx context.Context, deletes []*runtime.StorageDelete) error {
	return nil
}

func (m *vrmlLedgerStoreNK) UnlinkDevice(ctx context.Context, userID, deviceID string) error {
	return nil
}

func (m *vrmlLedgerStoreNK) storedLedgerUserIDs(t *testing.T) []string {
	t.Helper()
	var ledger VRMLEntitlementLedger
	if err := json.Unmarshal([]byte(m.ledgerValue), &ledger); err != nil {
		t.Fatalf("stored ledger does not decode: %v", err)
	}
	ids := make([]string, 0, len(ledger.Entries))
	for _, e := range ledger.Entries {
		ids = append(ids, e.UserID)
	}
	return ids
}

func seedVRMLLedger(t *testing.T, nk *vrmlLedgerStoreNK, userIDs ...string) {
	t.Helper()
	ledger := VRMLEntitlementLedger{Entries: make([]*VRMLEntitlementLedgerEntry, 0, len(userIDs))}
	for _, id := range userIDs {
		ledger.Entries = append(ledger.Entries, &VRMLEntitlementLedgerEntry{UserID: id, VRMLUserID: "vrml-" + id, VRMLPlayerID: "player-" + id})
	}
	data, err := json.Marshal(ledger)
	if err != nil {
		t.Fatal(err)
	}
	nk.ledgerValue = string(data)
	nk.ledgerVersion = 1
}

// TestVRMLLedger_UnlinkSurvivesVerification is #604: UnlinkVRMLAccount removes
// a user's entry from the ledger, and the verifier's next commit must not put
// it back. The verifier holds a copy of the ledger loaded before the unlink
// landed; it wrote that copy back unconditionally, resurrecting the entry.
func TestVRMLLedger_UnlinkSurvivesVerification(t *testing.T) {
	const unlinked, kept, verified = "user-a", "user-b", "user-c"

	unlink := func(t *testing.T, nk *vrmlLedgerStoreNK) {
		t.Helper()
		if err := UnlinkVRMLAccount(context.Background(), drainTestLogger(), nk, SystemUserID, "moderator", unlinked, "vrml-"+unlinked); err != nil {
			t.Fatalf("UnlinkVRMLAccount: %v", err)
		}
	}

	cases := []struct {
		name string
		// run performs the unlink at the point under test and then the
		// verification pass, using the ledger the verifier loaded first.
		run func(t *testing.T, nk *vrmlLedgerStoreNK, ledger *VRMLEntitlementLedger) error
	}{
		{
			// The reported case: the verifier loaded the ledger at startup,
			// then a moderator unlinked a user, then a verification ran.
			name: "unlink after the verifier's startup load",
			run: func(t *testing.T, nk *vrmlLedgerStoreNK, ledger *VRMLEntitlementLedger) error {
				unlink(t, nk)
				return recordVRMLVerification(context.Background(), nk, ledger, verified, "vrml-"+verified, "player-"+verified, []byte(`{}`), nil)
			},
		},
		{
			// The unlink commits while the verification pass is already under
			// way, immediately before its transaction is applied.
			name: "unlink immediately before the verifier's commit",
			run: func(t *testing.T, nk *vrmlLedgerStoreNK, ledger *VRMLEntitlementLedger) error {
				nk.beforeVerifierCommit = func() { unlink(t, nk) }
				return recordVRMLVerification(context.Background(), nk, ledger, verified, "vrml-"+verified, "player-"+verified, []byte(`{}`), nil)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nk := &vrmlLedgerStoreNK{}
			seedVRMLLedger(t, nk, unlinked, kept)

			ledger, err := VRMLEntitlementLedgerLoad(context.Background(), nk)
			if err != nil {
				t.Fatalf("VRMLEntitlementLedgerLoad: %v", err)
			}

			if err := tc.run(t, nk, ledger); err != nil {
				t.Fatalf("verification pass failed: %v", err)
			}

			if nk.verifierCommits != 1 {
				t.Fatalf("%d verifier commits applied, want exactly 1", nk.verifierCommits)
			}
			got := nk.storedLedgerUserIDs(t)
			want := []string{kept, verified}
			if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
				t.Fatalf("stored ledger users = %v, want %v — the unlinked user %q must stay removed and the verification must be recorded", got, want, unlinked)
			}
			if len(ledger.Entries) != 2 || ledger.Entries[0].UserID != kept || ledger.Entries[1].UserID != verified {
				ids := make([]string, 0, len(ledger.Entries))
				for _, e := range ledger.Entries {
					ids = append(ids, e.UserID)
				}
				t.Fatalf("verifier's in-memory ledger = %v, want %v", ids, want)
			}
		})
	}
}
