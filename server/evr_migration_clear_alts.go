package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

// MigrationClearAlternateMatches clears every account's stored
// alternate-account links and REBUILDS them against the current detection
// code, so global operators see correct data without waiting for each
// player to log in.
//
// Why it exists: v3.27.2-evr.321 (56e9a9c2d) promoted SystemProfile to an
// alt DISCOVERY key. The profile string is headset_model::network_type::
// video_card::cpu_model plus four integers — nothing machine-unique, so
// accounts sharing a headset model were falsely linked, up to 146 links on a
// single account. The detection code is fixed (SystemProfile is comparison-
// only again), but the stored links persist until each account logs in and
// UpdateAlternates rebuilds its map.
//
// This migration clears each account's links, then calls UpdateAlternates —
// the exact same rebuild path a login runs — so the stored data is corrected
// immediately rather than gradually.
//
// Idempotent: a second run finds the maps correct and rebuilds to the same
// values. Version conflicts are skipped — a racing login rebuilds that
// account correctly either way.
//
// Note: UpdateAlternates itself persists the OTHER side of each link
// bidirectionally; this migration persists the account's own history after
// the rebuild, which is the step the login flow performs separately.
type MigrationClearAlternateMatches struct{}

func (m *MigrationClearAlternateMatches) MigrateSystem(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) error {
	cleared := 0
	rebuilt := 0
	walked := 0
	startTime := time.Now()

	var cursor string
	for {
		batchStart := time.Now()
		objects, nextCursor, listErr := nk.StorageList(ctx, SystemUserID, "", LoginStorageCollection, 100, cursor)
		if listErr != nil {
			return fmt.Errorf("storage list: %w", listErr)
		}

		writes := make([]*runtime.StorageWrite, 0, len(objects))
		for _, obj := range objects {
			if obj.Key != LoginHistoryStorageKey {
				continue
			}
			walked++

			history := NewLoginHistory(obj.UserId)
			if err := json.Unmarshal([]byte(obj.Value), history); err != nil {
				logger.WithFields(map[string]any{"user_id": obj.UserId, "error": err}).Warn("alt-clear migration: unmarshal history")
				continue
			}
			history.SetStorageMeta(StorableMetadata{
				UserID:  obj.UserId,
				Version: obj.Version,
			})

			hadLinks := len(history.AlternateMatches) > 0 || len(history.SecondDegreeAlternates) > 0
			if hadLinks {
				// Clear first. UpdateAlternates returns early when a search
				// finds zero matches, leaving the existing (stale) map in
				// place — so clearing must happen before the rebuild.
				history.AlternateMatches = nil
				history.SecondDegreeAlternates = nil
				cleared++
			}

			// Rebuild against the current detection code. This is the same
			// path a login runs (evr_pipeline_login.go:638), including the
			// bidirectional writes to linked accounts.
			if _, err := history.UpdateAlternates(ctx, logger, nk); err != nil {
				logger.WithFields(map[string]any{"user_id": obj.UserId, "error": err}).Warn("alt-clear migration: rebuild alternates failed; writing cleared state")
			}

			// Persist if anything changed (cleared stale links, or rebuilt
			// new ones). Accounts with nothing on either side are skipped.
			if !hadLinks && len(history.AlternateMatches) == 0 && len(history.SecondDegreeAlternates) == 0 {
				continue
			}

			data, err := json.Marshal(history)
			if err != nil {
				logger.WithFields(map[string]any{"user_id": obj.UserId, "error": err}).Warn("alt-clear migration: marshal history")
				continue
			}

			meta := history.StorageMeta()
			writes = append(writes, &runtime.StorageWrite{
				Collection:      meta.Collection,
				Key:             meta.Key,
				UserID:          obj.UserId,
				Value:           string(data),
				Version:         meta.Version,
				PermissionRead:  meta.PermissionRead,
				PermissionWrite: meta.PermissionWrite,
			})
		}

		if len(writes) > 0 {
			if _, writeErr := nk.StorageWrite(ctx, writes); writeErr != nil {
				logger.WithField("error", writeErr).Warn("alt-clear migration: batch write (version conflict on one or more rows; next login rebuilds those accounts)")
			}
			rebuilt += len(writes)
		}

		logger.WithFields(map[string]any{
			"batch":       len(writes),
			"cleared":     cleared,
			"rebuilt":     rebuilt,
			"walked":      walked,
			"batch_time":  time.Since(batchStart).String(),
			"total_time":  time.Since(startTime).String(),
		}).Info("alt-clear migration: progress")

		if nextCursor == "" {
			break
		}
		cursor = nextCursor

		// Wait the same duration the batch took before starting the next:
		// exactly 2x wall time for the full walk, and the migration's load
		// contribution is capped at 50% of the box. Self-adjusting: a slow
		// batch on a busy server gets an equally long rest.
		<-time.After(time.Since(batchStart))
	}

	logger.WithFields(map[string]any{
		"cleared": cleared,
		"rebuilt": rebuilt,
		"walked":  walked,
		"total":   time.Since(startTime).String(),
	}).Info("alt-clear migration complete")

	return nil
}
