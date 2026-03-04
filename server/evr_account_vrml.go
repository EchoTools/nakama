package server

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/echotools/vrmlgo/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	StorageKeyVRMLAccount = "VRMLAccount"
	DeviceIDPrefixVRML    = "vrml:"
)

type VRMLAccountData struct {
	User   *vrmlgo.Member `json:"user"`
	Player *vrmlgo.Player `json:"player"`
}

type AccountAlreadyLinkedError struct {
	OwnerUserID string
}

func (e *AccountAlreadyLinkedError) Error() string {
	return fmt.Sprintf("VRML Account is already linked to user: `%s`", e.OwnerUserID)
}

func (a EVRProfile) VRMLUserID() string {
	for _, d := range a.account.Devices {
		if playerID, found := strings.CutPrefix(d.Id, DeviceIDPrefixVRML); found {
			return playerID
		}
	}
	return ""
}

// VerifyOwnership verifies that the user owns the VRML account by checking the Discord ID
func LinkVRMLAccount(ctx context.Context, db *sql.DB, nk runtime.NakamaModule, userID string, vrmlUserID string) error {
	// Link the vrml account to the user
	if ownerID, err := GetUserIDByDeviceID(ctx, db, VRMLDeviceID(vrmlUserID)); err != nil {
		if status.Code(err) != codes.NotFound {
			return fmt.Errorf("failed to get user ID by device ID %s: %w", VRMLDeviceID(vrmlUserID), err)
		}
	} else if ownerID != userID {
		return &AccountAlreadyLinkedError{OwnerUserID: ownerID}
	}
	if err := nk.LinkDevice(ctx, userID, VRMLDeviceID(vrmlUserID)); err != nil {
		return fmt.Errorf("failed to link VRML account: %w", err)
	}
	// Queue the event to count matches and assign entitlements
	if err := SendEvent(ctx, nk, &EventVRMLAccountLink{
		UserID:     userID,
		VRMLUserID: vrmlUserID,
	}); err != nil {
		return fmt.Errorf("failed to queue VRML account linked event: %w", err)
	}
	return nil
}

// UnlinkVRMLAccount removes the VRML device link and all associated data:
// wallet cosmetics are zeroed, the VRML player summary is deleted, the user's
// entry is removed from the system EntitlementLedger, and the vrml: device ID
// is unlinked.
func UnlinkVRMLAccount(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, assignerID, assignerUsername, userID, vrmlUserID string) error {
	// 1. Zero all VRML wallet cosmetics.
	if err := RevokeNonEntitledVRMLCosmetics(ctx, logger, nk, assignerID, assignerUsername, userID, vrmlUserID, nil); err != nil {
		return fmt.Errorf("failed to revoke VRML cosmetics: %w", err)
	}

	// 2. Delete the VRML player summary.
	if err := nk.StorageDelete(ctx, []*runtime.StorageDelete{
		{Collection: StorageCollectionVRML, Key: StorageKeyVRMLSummary, UserID: userID},
		{Collection: "Social", Key: "VRMLUser", UserID: userID},
	}); err != nil {
		logger.WithField("error", err).Warn("failed to delete VRML storage objects on unlink")
	}

	// 3. Remove the user's entry from the system EntitlementLedger.
	ledger, err := VRMLEntitlementLedgerLoad(ctx, nk)
	if err != nil {
		return fmt.Errorf("failed to load entitlement ledger: %w", err)
	}
	filteredEntries := ledger.Entries[:0]
	for _, e := range ledger.Entries {
		if e.UserID != userID {
			filteredEntries = append(filteredEntries, e)
		}
	}
	if len(filteredEntries) != len(ledger.Entries) {
		ledger.Entries = filteredEntries
		if err := VRMLEntitlementLedgerStore(ctx, nk, ledger); err != nil {
			return fmt.Errorf("failed to store entitlement ledger: %w", err)
		}
	}

	// 4. Remove the vrml: device link.
	if err := nk.UnlinkDevice(ctx, userID, VRMLDeviceID(vrmlUserID)); err != nil {
		return fmt.Errorf("failed to unlink VRML device: %w", err)
	}

	logger.WithFields(map[string]any{
		"assigner_id":       assignerID,
		"assigner_username": assignerUsername,
		"user_id":           userID,
		"vrml_user_id":      vrmlUserID,
	}).Info("unlinked VRML account")

	return nil
}
