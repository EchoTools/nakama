package server

import (
	"context"
	"encoding/json"

	"github.com/heroiclabs/nakama-common/runtime"
)

type VRMLEntitlementLedgerEntry struct {
	UserID       string             `json:"user_id"`
	VRMLUserID   string             `json:"vrml_user_id"`
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

func VRMLEntitlementLedgerStore(ctx context.Context, nk runtime.NakamaModule, ledger *VRMLEntitlementLedger) error {

	data, err := json.Marshal(ledger)
	if err != nil {
		return err
	}

	if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection: StorageCollectionVRML,
			Key:        StorageKeyVRMLVerificationLedger,
			UserID:     SystemUserID,
			Value:      string(data),

			PermissionRead:  0,
			PermissionWrite: 0,
		},
	}); err != nil {
		return err
	}

	return nil
}
