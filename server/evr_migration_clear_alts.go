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
// values. A row whose version was moved on by a racing login is skipped and
// counted in conflicted — that login rebuilds the account correctly either
// way. Only that row is skipped: because the batch write is one transaction,
// a rejection rolls all of it back, so the remaining rows are re-submitted
// individually rather than lost when the cursor advances.
//
// An account whose rebuild FAILS is not written at all. The clear happens in
// memory before the rebuild, so persisting after a failure would store an
// empty map as though the account genuinely had no alternates — a transient
// I/O error and a real "no alternates found" would be indistinguishable in
// storage. Those accounts are counted in rebuild_failed and retried on the
// next run.
//
// Note: UpdateAlternates itself persists the OTHER side of each link
// bidirectionally; this migration persists the account's own history after
// the rebuild, which is the step the login flow performs separately.
type MigrationClearAlternateMatches struct{}

func (m *MigrationClearAlternateMatches) MigrateSystem(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) error {
	cleared := 0
	rebuilt := 0
	rebuildFailed := 0
	conflicted := 0
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
			}

			// Rebuild against the current detection code. This is the same
			// path a login runs (evr_pipeline_login.go:638), including the
			// bidirectional writes to linked accounts.
			if _, err := history.UpdateAlternates(ctx, logger, nk); err != nil {
				// Do NOT persist. The maps were cleared in memory just above,
				// and every error UpdateAlternates can return is I/O-backed —
				// the alt index list (evr_authenticate_alts.go:139-141),
				// AccountsGetId, or a StorableRead — so a context deadline, a
				// reset connection or an unavailable index all land here.
				// Writing the cleared state now would turn a transient failure
				// into the permanent erasure of a genuine disabled-alt link,
				// and alt-based enforcement would be blind to it until that
				// account logged in again: a fail-open on a moderation
				// control. Leaving the row untouched keeps the stored links,
				// and the migration re-runs from a fresh cursor on the next
				// process start, which retries this account.
				rebuildFailed++
				logger.WithFields(map[string]any{"user_id": obj.UserId, "error": err}).Error("alt-clear migration: rebuild alternates failed; stored state left untouched for retry")
				continue
			}

			if hadLinks {
				cleared++
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
				// The batch is all-or-nothing. StorageWriteObjects
				// (core_storage.go:583-613) runs it inside ExecuteInTxPgx and
				// converts a version rejection into a returned error, so the
				// transaction rolled back and NOT ONE of these rows committed
				// — not just the row that raced. Nor was it retried:
				// executeInTxPostgresPgx (db.go:418-447) retries only when
				// errors.As finds a *pgconn.PgError with SQLSTATE class 40,
				// and a version rejection is a Go sentinel wrapped in a
				// statusError, so it is terminal on the first attempt. The
				// response carries nil acks, so there is no way to learn WHICH
				// row raced.
				//
				// Previously the cursor then advanced and rebuilt was credited
				// the full batch, so one racing login silently cost up to 99
				// uninvolved accounts their correction and overstated the
				// count by the same amount. Retry the rows one at a time: the
				// row whose version actually moved on fails again on its own
				// merits, and the rest are written. This runs only on the
				// error path, so a healthy batch still costs exactly one
				// write.
				logger.WithFields(map[string]any{"error": writeErr, "batch": len(writes)}).Warn("alt-clear migration: batch write rejected and rolled back; retrying rows individually")

				for _, w := range writes {
					if _, rowErr := nk.StorageWrite(ctx, []*runtime.StorageWrite{w}); rowErr != nil {
						conflicted++
						logger.WithFields(map[string]any{"user_id": w.UserID, "error": rowErr}).Warn("alt-clear migration: row write rejected; a racing login rebuilds this account")
						continue
					}
					rebuilt++
				}
			} else {
				rebuilt += len(writes)
			}
		}

		logger.WithFields(map[string]any{
			"batch":          len(writes),
			"cleared":        cleared,
			"rebuilt":        rebuilt,
			"rebuild_failed": rebuildFailed,
			"conflicted":     conflicted,
			"walked":         walked,
			"batch_time":     time.Since(batchStart).String(),
			"total_time":     time.Since(startTime).String(),
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
		"cleared":        cleared,
		"rebuilt":        rebuilt,
		"rebuild_failed": rebuildFailed,
		"conflicted":     conflicted,
		"walked":         walked,
		"total":          time.Since(startTime).String(),
	}).Info("alt-clear migration complete")

	return nil
}
