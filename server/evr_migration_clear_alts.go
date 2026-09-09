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

// migrationClearAltsFloorFraction is the share of the links this migration has
// examined that it may DESTROY -- clear and then fail to rebuild -- before the
// run is treated as evidence that the searcher is broken rather than evidence
// that the links were wrong.
//
// It is deliberately high, because clearing a large majority is the EXPECTED
// outcome here. Under #589 all 7,254 production links rested on a shared IP and
// not one carried an XPID or a serial, so a great many of them are false
// positives that correctly do not come back. A floor at 50% or 70% would abort
// this migration for doing exactly the job it exists to do.
//
// What the floor is for is the other shape, where the destruction is TOTAL
// because nothing can be found at all: an unavailable storage index, a CGNAT
// config that filters every strong signal out of the discovery keys (that is
// #589 itself, and it was one character of config), an ASN dataset that never
// loaded. Every one of those clears ~100% and rebuilds ~0% -- not 90%. So 0.90
// sits in the gap between "most of these links were wrong", which is plausible,
// and "no link could be formed for anyone", which is a broken world. That gap
// is the only distinction available from inside the run.
//
// It is a ceiling on DESTRUCTION, not on clearing: an account that is cleared
// and rebuilt to the same links costs nothing against it.
const migrationClearAltsFloorFraction = 0.90

// migrationClearAltsFloorMinLinks is how many links must have been examined
// before the fraction above means anything.
//
// The floor is evaluated once per page, so without a minimum the first page
// decides the run: three linked accounts that are all correctly cleared read as
// 100% destruction and abort a healthy migration. 100 links is ~1.4% of the
// production total -- small enough that a genuinely broken run is stopped in
// its first page or two, large enough that the ratio is not one account's worth
// of noise.
const migrationClearAltsFloorMinLinks = 100

// migrationPacer is the migration's clock and its brake, together, so that a
// test can drive both without waiting on wall time.
//
// The brake is "pause for as long as the work just done took", which holds the
// migration to roughly half of one core's worth of the box and self-adjusts: a
// slow unit on a busy server earns an equally long rest.
//
// The UNIT is one ACCOUNT. It used to be one page, which produced the same 50%
// average with a square-wave profile -- migrationPageSize accounts at full
// tilt, then a stop of exactly the same length. Averages do not queue; a live
// server does. Pacing per account spreads the same total pause across the page
// and flattens the profile to something a running service can absorb.
//
// The one step not paced is the page's batch write, which is a single round
// trip per page against O(links) round trips per account inside
// UpdateAlternates. The duty cycle is therefore marginally above 50%, by that
// remainder, and the alternative -- a pause after the batch write too -- buys
// exactness at the cost of reintroducing a per-page pause to reason about.
type migrationPacer struct {
	now   func() time.Time
	sleep func(d time.Duration)
}

// wallClockPacer is what production uses.
var wallClockPacer = &migrationPacer{
	now:   time.Now,
	sleep: func(d time.Duration) { <-time.After(d) },
}

// pace pauses for as long as the work that began at start took.
func (p *migrationPacer) pace(start time.Time) {
	if d := p.now().Sub(start); d > 0 {
		p.sleep(d)
	}
}

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
// stored one, so a second run over converged data performs zero writes.
//
// This used to be load-bearing for a different reason: the migration was
// registered unconditionally and ran on every boot, so "rewrite every row
// whether or not it changed" would have put a full-table write behind each
// restart forever. A completion marker now holds it to one run (see
// MigrateSystem), so what idempotence protects is the OPERATOR RE-RUN path:
// clearing the marker has to be a safe thing to do, and it is only safe
// because a second pass over converged data changes nothing.
//
// A row whose version was moved on by a racing login is skipped and counted in
// conflicted — that login rebuilds the account correctly either way. Only that
// row is skipped: because the batch write is one transaction, a rejection rolls
// all of it back, so the remaining rows are re-submitted individually rather
// than lost when the cursor advances.
//
// An account whose rebuild FAILS is not written at all. The clear happens in
// memory before the rebuild, so persisting after a failure would store an
// empty map as though the account genuinely had no alternates — a transient
// I/O error and a real "no alternates found" would be indistinguishable in
// storage. Those accounts are counted in rebuild_failed.
//
// Note what the marker costs here, because it is a real trade and not a free
// win: a rebuild_failed account is no longer retried by the next process
// start. The run as a whole still completed, so the marker is written, and
// those accounts stay as they were until either the account logs in — which
// rebuilds it through the same path — or an operator clears the marker. The
// count is in the completion log and in the marker's own summary so the number
// is not lost.
//
// Note: UpdateAlternates itself persists the OTHER side of each link
// bidirectionally; this migration persists the account's own history after
// the rebuild, which is the step the login flow performs separately.
type MigrationClearAlternateMatches struct {
	// pacer is nil in production, which means the wall clock. Tests inject one
	// to assert the pacing SHAPE -- how many pauses, and how long each is
	// relative to the work it follows -- without waiting on, or flaking on,
	// real elapsed time.
	pacer *migrationPacer
}

// clock returns the pacer this run should use.
func (m *MigrationClearAlternateMatches) clock() *migrationPacer {
	if m.pacer != nil {
		return m.pacer
	}
	return wallClockPacer
}

// MigrateSystem gates the run on a completion marker, so this migration walks
// production storage ONCE rather than on every process start.
//
// Every entry in evr_runtime_migrate.go's slice runs on every boot; there is no
// version table and no has-this-run check anywhere in the tree. For a migration
// whose body is two full-table walks over ~46,000 login histories, each account
// paced against its own processing time, that is a standing cost paid forever
// for work that converged after the first run.
//
// Three outcomes, and all three are visible in the log:
//
//   - marker unreadable -> refuse. See migrationMarkerRead.
//   - marker present    -> skip, naming the object to delete for a re-run.
//   - marker absent     -> run, and record the marker only on success.
//
// The marker is written after the run returns cleanly, never before and never
// partway. Both abort paths -- a storage error and the safety floor -- return
// an error, and neither reaches the write, so a run that did not finish leaves
// the migration owed.
func (m *MigrationClearAlternateMatches) MigrateSystem(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) error {
	// Every line below names the object an operator would delete, so the
	// instruction is in the log rather than only in this comment.
	markerFields := func(extra map[string]any) map[string]any {
		fields := map[string]any{
			"collection": MigrationStateStorageCollection,
			"key":        MigrationClearAltsStateKey,
			"owner":      SystemUserID,
		}
		maps.Copy(fields, extra)
		return fields
	}

	marker, err := migrationMarkerRead(ctx, nk, MigrationClearAltsStateKey)
	if err != nil {
		logger.WithFields(markerFields(map[string]any{"error": err})).
			Error("alt-clear migration: cannot determine whether this migration has already run; refusing to run")
		return err
	}
	if marker != nil {
		logger.WithFields(markerFields(map[string]any{
			"completed_at": marker.CompletedAt.Format(time.RFC3339),
			"previous_run": marker.Summary,
		})).Info("alt-clear migration: already complete, skipping; delete this storage object to force a re-run")
		return nil
	}

	logger.WithFields(markerFields(nil)).Info("alt-clear migration: no completion marker found, starting a fresh run")

	summary, err := m.run(ctx, logger, nk)
	if err != nil {
		return err
	}

	if err := migrationMarkerWrite(ctx, nk, MigrationClearAltsStateKey, &migrationCompletionMarker{
		Migration:   fmt.Sprintf("%T", m),
		CompletedAt: time.Now().UTC(),
		Summary:     summary,
	}); err != nil {
		// The work landed; the record of it did not. Loud, because the
		// consequence is another full walk on the next boot -- and this time
		// over converged data, where the floor's own numbers look different.
		logger.WithFields(markerFields(map[string]any{"error": err})).
			Error("alt-clear migration: the run completed but its completion marker could not be recorded; the next boot will walk the whole table again")
		return err
	}

	return nil
}

// run is the migration body. It is separate from MigrateSystem so the
// completion marker gates it from the outside: run has no opinion about whether
// it should have been called, and every path out of it is either a clean
// summary or an error.
func (m *MigrationClearAlternateMatches) run(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule) (map[string]any, error) {
	pacer := m.clock()
	startTime := pacer.now()

	// Phase 1. Every account's indexed cache must be correct before the first
	// discovery query runs — see the type comment.
	cacheRepaired, cacheConflicted, err := m.repairIndexedCaches(ctx, logger, nk)
	if err != nil {
		return nil, err
	}

	cleared := 0
	rebuilt := 0
	rebuildFailed := 0
	unsearchable := 0
	conflicted := cacheConflicted
	walked := 0

	// The two numbers the safety floor is computed from. Only accounts whose
	// rebuild actually SUCCEEDED are counted: a skipped or failed account has
	// nothing taken from it, so folding it in either way would dilute the very
	// ratio the floor exists to read.
	linksExamined := 0
	linksRebuilt := 0

	var cursor string
	for {
		batchStart := pacer.now()
		objects, nextCursor, listErr := nk.StorageList(ctx, SystemUserID, "", LoginStorageCollection, migrationPageSize, cursor)
		if listErr != nil {
			return nil, fmt.Errorf("storage list: %w", listErr)
		}

		writes := make([]*runtime.StorageWrite, 0, len(objects))
		for _, obj := range objects {
			if obj.Key != LoginHistoryStorageKey {
				continue
			}
			walked++

			// One pause per ACCOUNT, sized to that account's own work. The
			// body is a closure so that every early exit inside it -- an
			// unparseable record, an unsearchable account, a failed rebuild --
			// still passes through the pause below, rather than the pause
			// applying only to the accounts that made it all the way through.
			acctStart := pacer.now()
			func() {
				history := NewLoginHistory(obj.UserId)
				if err := json.Unmarshal([]byte(obj.Value), history); err != nil {
					logger.WithFields(map[string]any{"user_id": obj.UserId, "error": err}).Warn("alt-clear migration: unmarshal history")
					return
				}
				history.SetStorageMeta(StorableMetadata{
					UserID:  obj.UserId,
					Version: obj.Version,
				})

				// An account with no discovery patterns cannot be searched, so it
				// cannot be rebuilt, so it must not be cleared.
				//
				// This is the silent-erasure path, and every step of it looks like
				// success. AltSearchPatterns drops every item matchIgnoredAltPattern
				// covers and returns nil when nothing survives
				// (evr_authenticate_alts.go:112-114); LoginAlternateSearch sees the
				// empty pattern set and returns (nil, nil, nil) with NO ERROR
				// (:120-122); UpdateAlternates takes its len(matches) == 0 early
				// return (evr_authenticate_history.go:488-490), which reports
				// (false, nil) and leaves the maps exactly as it found them --
				// cleared, because the clear happened here a few lines below.
				// The rebuild-failed branch never fires, the write-decision
				// comparison sees a real difference, and the erasure is committed.
				//
				// The population this destroys is not exotic. A Quest player behind
				// CGNAT presents an RFC1918 or carrier-NAT client IP, a Meta-issued
				// placeholder HMD serial (VRLINKHMDQUEST*, literal entries in
				// IgnoredLoginValues) and, absent an XPID, the "UNK-0" token -- all
				// three ignored, so the pattern set is empty and the account's
				// stored links, including a genuine disabled-alt link, are wiped.
				// That is a fail-open on a fail-closed control: enforcement goes
				// blind and nothing says so.
				//
				// Skipping leaves the row byte-identical. Phase 1 has already
				// repaired this account's indexed cache, so nothing is owed here.
				// Links that rest only on ignored values are still removable, by an
				// operator, through CGNATCleanupRPC.
				if len(history.AltSearchPatterns()) == 0 {
					unsearchable++
					return
				}

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
					// control. Leaving the row untouched keeps the stored links.
					//
					// The completion marker changed what happens next, and not
					// for the better: this account is NO LONGER retried by the
					// following process start, because there is no following
					// run. It is repaired when the account next logs in, or
					// when an operator clears the marker. That is why the
					// failure is logged at ERROR with the user ID, and why
					// rebuild_failed is carried into the marker's summary --
					// the retry that used to be automatic is now something a
					// human has to decide to do.
					rebuildFailed++
					logger.WithFields(map[string]any{"user_id": obj.UserId, "error": err}).Error("alt-clear migration: rebuild alternates failed; stored state left untouched")
					return
				}

				if hadLinks {
					cleared++
				}

				// Feed the safety floor. storedLinks is what this account had
				// before the clear; AlternateMatches is what the search put back.
				linksExamined += len(storedLinks)
				linksRebuilt += len(history.AlternateMatches)

				// Marshal BEFORE deciding, because LoginHistory.MarshalJSON is
				// where rebuildCache runs (evr_authenticate_history.go:681) — the
				// repaired cache does not exist on the struct until this call.
				data, err := json.Marshal(history)
				if err != nil {
					logger.WithFields(map[string]any{"user_id": obj.UserId, "error": err}).Warn("alt-clear migration: marshal history")
					return
				}

				// Persist only a real change: cleared stale links, discovered new
				// ones, or a cache phase 1 could not have reached.
				if slices.Equal(storedCache, history.Cache) &&
					slices.Equal(storedSecond, history.SecondDegreeAlternates) &&
					maps.EqualFunc(storedLinks, altLinkItems(history.AlternateMatches), slices.Equal) {
					return
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
			}()
			pacer.pace(acctStart)
		}

		// The safety floor, checked BEFORE this page is submitted so a tripped
		// run does not commit the page that tripped it.
		//
		// Earlier pages have already been written and cannot be taken back — a
		// paged walk has no transaction spanning it — so the abort reports how
		// far it got rather than pretending the run was atomic. Returning an
		// error also means the completion marker is not recorded, so the run is
		// still owed and an operator can re-attempt it once the cause is fixed.
		if destroyed := linksExamined - linksRebuilt; linksExamined >= migrationClearAltsFloorMinLinks &&
			float64(destroyed) > float64(linksExamined)*migrationClearAltsFloorFraction {
			logger.WithFields(map[string]any{
				"links_examined":  linksExamined,
				"links_rebuilt":   linksRebuilt,
				"links_destroyed": destroyed,
				"ceiling":         migrationClearAltsFloorFraction,
				"already_written": rebuilt,
				"walked":          walked,
			}).Error("alt-clear migration: safety floor tripped; aborting the run without writing this page")
			return nil, fmt.Errorf("alt-clear migration aborted: safety floor tripped, %d of %d examined links destroyed without rebuild (ceiling %.0f%%)",
				destroyed, linksExamined, migrationClearAltsFloorFraction*100)
		}

		written, rejected := migrationWriteBatch(ctx, logger, nk, writes, "alt-clear")
		rebuilt += written
		conflicted += rejected

		logger.WithFields(map[string]any{
			"batch":          len(writes),
			"cleared":        cleared,
			"rebuilt":        rebuilt,
			"rebuild_failed": rebuildFailed,
			"unsearchable":   unsearchable,
			"conflicted":     conflicted,
			"walked":         walked,
			"batch_time":     pacer.now().Sub(batchStart).String(),
			"total_time":     pacer.now().Sub(startTime).String(),
		}).Info("alt-clear migration: progress")

		if nextCursor == "" {
			break
		}
		cursor = nextCursor
	}

	summary := map[string]any{
		"cache_repaired": cacheRepaired,
		"cleared":        cleared,
		"rebuilt":        rebuilt,
		"rebuild_failed": rebuildFailed,
		"unsearchable":   unsearchable,
		"conflicted":     conflicted,
		"walked":         walked,
		"total":          pacer.now().Sub(startTime).String(),
	}
	logger.WithFields(summary).Info("alt-clear migration complete")

	return summary, nil
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
	pacer := m.clock()
	startTime := pacer.now()
	walked := 0

	var cursor string
	for {
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

			// Paced per ACCOUNT, exactly as phase 2 is. This pass is CPU-only
			// -- unmarshal, recompute, compare -- so its per-account cost is
			// small and uniform, and spreading the pause across the page keeps
			// it that way instead of stalling for a page at a time.
			acctStart := pacer.now()
			func() {
				history := NewLoginHistory(obj.UserId)
				if err := json.Unmarshal([]byte(obj.Value), history); err != nil {
					logger.WithFields(map[string]any{"user_id": obj.UserId, "error": err}).Warn("alt-cache repair: unmarshal history")
					return
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
					return
				}

				if slices.Equal(storedCache, history.Cache) {
					return
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
			}()
			pacer.pace(acctStart)
		}

		written, rejected := migrationWriteBatch(ctx, logger, nk, writes, "alt-cache repair")
		repaired += written
		conflicted += rejected

		if nextCursor == "" {
			break
		}
		cursor = nextCursor
	}

	logger.WithFields(map[string]any{
		"repaired":   repaired,
		"conflicted": conflicted,
		"walked":     walked,
		"total":      pacer.now().Sub(startTime).String(),
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
