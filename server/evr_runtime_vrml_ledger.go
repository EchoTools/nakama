package server

import (
	"context"
	"encoding/json"

	"github.com/heroiclabs/nakama-common/runtime"
)

type VRMLEntitlementLedgerEntry struct {
	UserID       string             `json:"user_id"`
	VRMLUserID   string             `json:"vrml_user_id"`
	VRMLPlayerID string             `json:"vrml_player_id"`
	Entitlements []*VRMLEntitlement `json:"entitlements"`
}

type VRMLEntitlementLedger struct {
	Entries []*VRMLEntitlementLedgerEntry `json:"entries"`
}

func VRMLEntitlementLedgerLoad(ctx context.Context, nk runtime.NakamaModule) (*VRMLEntitlementLedger, error) {
	// Get the VRML entitlements from the storage
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: StorageCollectionVRML,
			Key:        StorageKeyVRMLVerificationLedger,
			UserID:     SystemUserID,
		},
	})
	if err != nil {
		return nil, err
	}

	ledger := VRMLEntitlementLedger{
		Entries: make([]*VRMLEntitlementLedgerEntry, 0),
	}

	if len(objs) == 0 {
		return &ledger, nil
	}

	// Parse the VRML entitlements
	if objs != nil {
		err = json.Unmarshal([]byte(objs[0].Value), &ledger)
		if err != nil {
			return nil, err
		}
	}

	return &ledger, nil
}

// vrmlEntitlementLedgerWriteOp renders the ledger as a storage write operation
// without committing it, so a caller that has other work to commit in the same
// transaction can carry it along. VRMLEntitlementLedgerStore is the standalone
// form for callers that do not.
func vrmlEntitlementLedgerWriteOp(ledger *VRMLEntitlementLedger) (*runtime.StorageWrite, error) {
	data, err := json.Marshal(ledger)
	if err != nil {
		return nil, err
	}

	return &runtime.StorageWrite{
		Collection: StorageCollectionVRML,
		Key:        StorageKeyVRMLVerificationLedger,
		UserID:     SystemUserID,
		Value:      string(data),

		PermissionRead:  0,
		PermissionWrite: 0,
	}, nil
}

// VRMLEntitlementLedgerStore commits the ledger on its own.
//
// It goes through MultiUpdate rather than StorageWrite for the reason recorded
// on StorableWriteMany: MultiUpdate is the single entry point that can also
// carry account updates, deletes and wallet updates, so a caller that later
// needs to widen the atomic unit does not have to change shape. The VRML
// verifier has already done exactly that — see commitVRMLVerification.
func VRMLEntitlementLedgerStore(ctx context.Context, nk runtime.NakamaModule, ledger *VRMLEntitlementLedger) error {
	op, err := vrmlEntitlementLedgerWriteOp(ledger)
	if err != nil {
		return err
	}

	if _, _, err := nk.MultiUpdate(ctx, nil, []*runtime.StorageWrite{op}, nil, nil, false); err != nil {
		return err
	}

	return nil
}
