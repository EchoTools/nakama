package server

import (
	"context"
	"database/sql"

	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MigrationAuditEntitlementLedger scans the system EntitlementLedger and removes
// any entries whose vrml: device link no longer exists on the referenced Nakama
// account. This cleans up orphaned entries left by manual unlinks or by accounts
// that were unlinked before UnlinkVRMLAccount was updated to prune the ledger.
type MigrationAuditEntitlementLedger struct{}

var _ SystemMigrator = (*MigrationAuditEntitlementLedger)(nil)

func (m *MigrationAuditEntitlementLedger) MigrateSystem(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) error {
	const (
		sentinelCollection = "Migrations"
		sentinelKey        = "AuditEntitlementLedger"
	)

	// Check if this migration has already run.
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{{
		Collection: sentinelCollection,
		Key:        sentinelKey,
		UserID:     SystemUserID,
	}})
	if err != nil {
		return err
	}
	if len(objs) > 0 {
		logger.Info("MigrationAuditEntitlementLedger: already completed, skipping")
		return nil
	}

	ledger, err := VRMLEntitlementLedgerLoad(ctx, nk)
	if err != nil {
		return err
	}

	before := len(ledger.Entries)
	kept := ledger.Entries[:0]

	for _, entry := range ledger.Entries {
		account, err := nk.AccountGetId(ctx, entry.UserID)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				logger.WithFields(map[string]any{
					"user_id":      entry.UserID,
					"vrml_user_id": entry.VRMLUserID,
				}).Info("MigrationAuditEntitlementLedger: dropping entry for deleted account")
				continue
			}
			return err
		}

		// Check whether the vrml: device link still exists on this account.
		deviceID := VRMLDeviceID(entry.VRMLUserID)
		linked := false
		for _, d := range account.Devices {
			if d.Id == deviceID {
				linked = true
				break
			}
		}

		if !linked {
			logger.WithFields(map[string]any{
				"user_id":      entry.UserID,
				"vrml_user_id": entry.VRMLUserID,
			}).Info("MigrationAuditEntitlementLedger: dropping orphaned entry (device unlinked)")
			continue
		}

		kept = append(kept, entry)
	}

	removed := before - len(kept)
	logger.WithFields(map[string]any{
		"entries_before": before,
		"entries_after":  len(kept),
		"removed":        removed,
	}).Info("MigrationAuditEntitlementLedger: audit complete")

	if removed > 0 {
		ledger.Entries = kept
		if err := VRMLEntitlementLedgerStore(ctx, nk, ledger); err != nil {
			return err
		}
	}

	// Write sentinel.
	if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      sentinelCollection,
		Key:             sentinelKey,
		UserID:          SystemUserID,
		Value:           `{"completed":true}`,
		Version:         "*",
		PermissionRead:  0,
		PermissionWrite: 0,
	}}); err != nil && status.Code(err) != codes.AlreadyExists {
		return err
	}

	return nil
}
