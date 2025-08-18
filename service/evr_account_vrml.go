package service

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
