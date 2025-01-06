package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

type MigrationCombineStoredCosmeticLoadouts struct{}

// Combine all the stored cosmetic loadouts into one object
func (m *MigrationCombineStoredCosmeticLoadouts) MigrateSystem(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) error {
	objs, _, err := nk.StorageList(ctx, uuid.Nil.String(), uuid.Nil.String(), CosmeticLoadoutCollection, 100, "")
	if err != nil {
		return fmt.Errorf("failed to list objects: %w", err)
	}

	combined := make([]*StoredCosmeticLoadout, 0, len(objs))

	for _, obj := range objs {

		loadout := &StoredCosmeticLoadout{}
		if err := json.Unmarshal([]byte(obj.Value), loadout); err != nil {
			return fmt.Errorf("failed to unmarshal object: %w", err)
		}

		combined = append(combined, loadout)
	}

	if len(combined) == 0 {
		logger.Info("No stored cosmetic loadouts to combine.")
		return nil
	}
	obj := StarterCosmeticLoadouts{
		Loadouts: combined,
	}

	data, err := json.Marshal(obj)
	if err != nil {
		return fmt.Errorf("failed to marshal combined loadouts: %w", err)
	}

	if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection:     CosmeticLoadoutCollection,
			Key:            CosmeticLoadoutKey,
			UserID:         uuid.Nil.String(),
			Value:          string(data),
			PermissionRead: 2,
		},
	}); err != nil {
		return fmt.Errorf("failed to write combined loadouts: %w", err)
	}

	// Remove the old loadout
	ops := make([]*runtime.StorageDelete, 0, len(objs))
	for _, obj := range objs {
		ops = append(ops, &runtime.StorageDelete{
			Collection: obj.Collection,
			Key:        obj.Key,
			UserID:     obj.UserId,
			Version:    obj.Version,
		})
	}

	if len(ops) > 0 {
		if err := nk.StorageDelete(ctx, ops); err != nil {
			return fmt.Errorf("failed to delete old loadouts: %w", err)
		}
	}

	logger.Info("Migrated stored cosmetic loadouts.")
	return nil
}
