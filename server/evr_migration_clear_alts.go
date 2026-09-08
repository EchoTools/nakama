package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

// migrationPageSize is the number of storage rows fetched per StorageList
// call. It is also the blast radius of one rejected batch write.
const migrationPageSize = 100

// MigrationClearAlternateMatches clears every account's stored
// alternate-account links and REBUILDS them against the current detection
// code, so global operators see correct data without waiting for each
// player to log in.
//
// Why it exists, first time: v3.27.2-evr.321 (56e9a9c2d) promoted
// SystemProfile to an alt DISCOVERY key. The profile string is
// headset_model::network_type::video_card::cpu_model plus four integers —
// nothing machine-unique, so accounts sharing a headset model were falsely
// linked, up to 146 links on a single account.
//
// Why it exists now: #589. `commodity_profile_prefixes` carried an empty
// string, so strings.HasPrefix(x, "") was true for every x and
// CGNATDetector.IsWeakSignal returned true for every non-IP item.
// matchIgnoredAltPattern (evr_authenticate_history.go:50) consults
// IsWeakSignal for non-IP patterns, so XPIDs, HMD serials and system profiles
// were stripped out of both the indexed cache (rebuildCache, :607) and the
// discovery keys (AltSearchPatterns, evr_authenticate_alts.go:106). All 7,254
// production alt links came to rest on a shared IP and not one carries an XPID
// or a serial. The detection code is fixed; the stored links are not, and they
// stay wrong until each account logs in and UpdateAlternates rebuilds its map.
//
// # Two phases, and the order is the whole design
//
// Phase 1 repairs every account's INDEXED CACHE. Phase 2 clears and rebuilds
// the links. They cannot be merged, and phase 1 cannot be folded into phase 2's
// page loop, because discovery is not symmetric with the data it reads:
// LoginAlternatePatternSearch (evr_authenticate_alts.go:129-139) queries
// `value.cache` — the field as it is STORED on OTHER accounts — so an account
// whose stored cache is degraded is invisible to every search, however correct
// the searcher's own freshly-computed patterns are. Two degraded accounts
// linked only by an XPID therefore find each other zero times.
//
// A single pass does not fix this. Phase 2 batches its writes to the end of
// each page, so a repair made to row 3 is not visible to row 47 of the same
// page at all, and a pair that lands in one page is missed outright. Across
// pages it only works in one direction. Running phase 1 to completion first is
// what makes the recompute find every pair rather than most of them.
//
// # Idempotence
//
// Both phases write only when the recomputed value actually differs from the
// stored one, so a second run over converged data performs zero writes. That
// is not a nicety: this migration is registered unconditionally
// (evr_runtime_migrate.go:24) and runs on every boot, so "rewrite every row
// whether or not it changed" would put a full-table write behind each restart
// forever. A row whose version was moved on by a racing login is skipped and
// counted in conflicted — that login rebuilds the account correctly either
// way. Only that row is skipped: because the batch write is one transaction, a
// rejection rolls all of it back, so the remaining rows are re-submitted
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
	startTime := time.Now()

	// Phase 1. Every account's indexed cache must be correct before the first
	// discovery query runs — see the type comment.
	cacheRepaired, cacheConflicted, err := m.repairIndexedCaches(ctx, logger, nk)
	if err != nil {
		return err
	}

	cleared := 0
	rebuilt := 0
	rebuildFailed := 0
	conflicted := cacheConflicted
	walked := 0

	var cursor string
	for {
		batchStart := time.Now()
		objects, nextCursor, listErr := nk.StorageList(ctx, SystemUserID, "", LoginStorageCollection, migrationPageSize, cursor)
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

			// Snapshot the stored state BEFORE the clear. The write decision
			// below is "did anything actually change", which is only
			// answerable against what was read.
			storedCache := slices.Clone(history.Cache)
			storedSecond := slices.Clone(history.SecondDegreeAlternates)
			storedLinks := altLinkItems(history.AlternateMatches)

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

			// Marshal BEFORE deciding, because LoginHistory.MarshalJSON is
			// where rebuildCache runs (evr_authenticate_history.go:681) — the
			// repaired cache does not exist on the struct until this call.
			data, err := json.Marshal(history)
			if err != nil {
				logger.WithFields(map[string]any{"user_id": obj.UserId, "error": err}).Warn("alt-clear migration: marshal history")
				continue
			}

			// Persist only a real change: cleared stale links, discovered new
			// ones, or a cache phase 1 could not have reached.
			if slices.Equal(storedCache, history.Cache) &&
				slices.Equal(storedSecond, history.SecondDegreeAlternates) &&
				maps.EqualFunc(storedLinks, altLinkItems(history.AlternateMatches), slices.Equal) {
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

		written, rejected := migrationWriteBatch(ctx, logger, nk, writes, "alt-clear")
		rebuilt += written
		conflicted += rejected

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
		"cache_repaired": cacheRepaired,
		"cleared":        cleared,
		"rebuilt":        rebuilt,
		"rebuild_failed": rebuildFailed,
		"conflicted":     conflicted,
		"walked":         walked,
		"total":          time.Since(startTime).String(),
	}).Info("alt-clear migration complete")

	return nil
}

// repairIndexedCaches is phase 1: it rewrites every login history whose stored
// `cache` field disagrees with what rebuildCache produces from that same
// record's History entries.
//
// It touches nothing else. No links are read, cleared or formed here, and no
// discovery query is issued — this pass exists purely so that phase 2's
// queries have a correct index to search. Under #589 the stored cache is a
// list of IP addresses where it should also carry XPIDs and HMD serials, and
// History (which the defect never touched) is the source of truth it is
// recomputed from.
//
// A row that already agrees is not written, so on converged data this pass is
// read-only.
func (m *MigrationClearAlternateMatches) repairIndexedCaches(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule) (repaired, conflicted int, err error) {
	startTime := time.Now()
	walked := 0

	var cursor string
	for {
		batchStart := time.Now()
		objects, nextCursor, listErr := nk.StorageList(ctx, SystemUserID, "", LoginStorageCollection, migrationPageSize, cursor)
		if listErr != nil {
			return repaired, conflicted, fmt.Errorf("storage list: %w", listErr)
		}

		writes := make([]*runtime.StorageWrite, 0, len(objects))
		for _, obj := range objects {
			if obj.Key != LoginHistoryStorageKey {
				continue
			}
			walked++

			history := NewLoginHistory(obj.UserId)
			if err := json.Unmarshal([]byte(obj.Value), history); err != nil {
				logger.WithFields(map[string]any{"user_id": obj.UserId, "error": err}).Warn("alt-cache repair: unmarshal history")
				continue
			}
			history.SetStorageMeta(StorableMetadata{
				UserID:  obj.UserId,
				Version: obj.Version,
			})

			storedCache := slices.Clone(history.Cache)

			// MarshalJSON runs rebuildCache, which is the recompute.
			data, err := json.Marshal(history)
			if err != nil {
				logger.WithFields(map[string]any{"user_id": obj.UserId, "error": err}).Warn("alt-cache repair: marshal history")
				continue
			}

			if slices.Equal(storedCache, history.Cache) {
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

		written, rejected := migrationWriteBatch(ctx, logger, nk, writes, "alt-cache repair")
		repaired += written
		conflicted += rejected

		if nextCursor == "" {
			break
		}
		cursor = nextCursor

		// Same 50% duty cycle as phase 2.
		<-time.After(time.Since(batchStart))
	}

	logger.WithFields(map[string]any{
		"repaired":   repaired,
		"conflicted": conflicted,
		"walked":     walked,
		"total":      time.Since(startTime).String(),
	}).Info("alt-cache repair complete")

	return repaired, conflicted, nil
}

// migrationWriteBatch submits writes as one batch and, if that batch is
// rejected, re-submits the rows individually. It returns how many rows
// committed and how many were rejected on their own merits.
//
// The batch is all-or-nothing. StorageWriteObjects (core_storage.go:583-613)
// runs it inside ExecuteInTxPgx and converts a version rejection into a
// returned error, so the transaction rolled back and NOT ONE of these rows
// committed — not just the row that raced. Nor was it retried:
// executeInTxPostgresPgx (db.go:418-447) retries only when errors.As finds a
// *pgconn.PgError with SQLSTATE class 40, and a version rejection is a Go
// sentinel wrapped in a statusError, so it is terminal on the first attempt.
// The response carries nil acks, so there is no way to learn WHICH row raced.
//
// Without the row-by-row retry the cursor advances and the caller credits
// itself the full batch, so one racing login silently costs up to
// migrationPageSize-1 uninvolved accounts their correction and overstates the
// count by the same amount. The retry runs only on the error path, so a
// healthy batch still costs exactly one write.
func migrationWriteBatch(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, writes []*runtime.StorageWrite, label string) (written, rejected int) {
	if len(writes) == 0 {
		return 0, 0
	}

	_, writeErr := nk.StorageWrite(ctx, writes)
	if writeErr == nil {
		return len(writes), 0
	}
	logger.WithFields(map[string]any{"error": writeErr, "batch": len(writes)}).Warn(label + ": batch write rejected and rolled back; retrying rows individually")

	for _, w := range writes {
		if _, rowErr := nk.StorageWrite(ctx, []*runtime.StorageWrite{w}); rowErr != nil {
			rejected++
			logger.WithFields(map[string]any{"user_id": w.UserID, "error": rowErr}).Warn(label + ": row write rejected; a racing login rebuilds this account")
			continue
		}
		written++
	}
	return written, rejected
}

// altLinkItems reduces an AlternateMatches map to one sorted, deduplicated
// item list per linked user, which is the comparable form of "what this
// account is linked to and on what evidence".
//
// AlternateMatches cannot be compared directly: its values are slices of
// pointers, so equality would be pointer identity, and a rebuild that produced
// the identical set would look like a change.
func altLinkItems(matches map[string][]*AlternateSearchMatch) map[string][]string {
	out := make(map[string][]string, len(matches))
	for userID, ms := range matches {
		items := make([]string, 0, len(ms))
		for _, m := range ms {
			items = append(items, m.Items...)
		}
		slices.Sort(items)
		out[userID] = slices.Compact(items)
	}
	return out
}
