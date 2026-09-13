package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/heroiclabs/nakama-common/runtime"
)

// MigrationBreakIgnoredAlts scans every LoginHistory and breaks any alt link
// whose matched items are all covered by matchIgnoredAltPattern (i.e. exact
// matches in IgnoredLoginValues, private/CGNAT IPs, or commodity HMD
// profiles).
//
// Idempotent: a second run finds zero work because dirty links were removed
// reciprocally on the first run. Add new entries to IgnoredLoginValues (e.g.
// new Meta-issued VRLINKHMDQUEST* serials) and the next boot will purge alt
// false-positives created from logins predating the addition.
type MigrationBreakIgnoredAlts struct{}

func (m *MigrationBreakIgnoredAlts) MigrateSystem(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) error {
	processed := make(map[string]bool)
	affectedSet := make(map[string]bool)
	brokenLinks := 0

	var cursor string
	for {
		objects, nextCursor, listErr := nk.StorageList(ctx, SystemUserID, "", LoginStorageCollection, 100, cursor)
		if listErr != nil {
			return fmt.Errorf("storage list: %w", listErr)
		}

		for _, obj := range objects {
			if obj.Key != LoginHistoryStorageKey {
				continue
			}

			history := NewLoginHistory(obj.UserId)
			if readErr := json.Unmarshal([]byte(obj.Value), history); readErr != nil {
				logger.WithFields(map[string]any{"user_id": obj.UserId, "error": readErr}).Warn("ignored-alts migration: unmarshal history")
				continue
			}
			history.SetStorageMeta(StorableMetadata{
				UserID:  obj.UserId,
				Version: obj.Version,
			})

			toBreak := make([]string, 0)
			for altID, matches := range history.AlternateMatches {
				pk := pairKey(obj.UserId, altID)
				if processed[pk] {
					continue
				}
				if allItemsIgnored(matches) {
					toBreak = append(toBreak, altID)
					processed[pk] = true
				}
			}

			if len(toBreak) == 0 {
				continue
			}

			for _, altID := range toBreak {
				otherHistory := NewLoginHistory(altID)
				if readErr := StorableRead(ctx, nk, altID, otherHistory, false); readErr != nil {
					logger.WithFields(map[string]any{"alt_id": altID, "error": readErr}).Warn("ignored-alts migration: load other history, skipping pair")
					continue
				}

				delete(otherHistory.AlternateMatches, obj.UserId)
				otherHistory.SecondDegreeAlternates = nil

				otherData, _ := json.Marshal(otherHistory)
				otherMeta := otherHistory.StorageMeta()
				if _, writeErr := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
					Collection:      otherMeta.Collection,
					Key:             otherMeta.Key,
					UserID:          altID,
					Value:           string(otherData),
					Version:         otherMeta.Version,
					PermissionRead:  otherMeta.PermissionRead,
					PermissionWrite: otherMeta.PermissionWrite,
				}}); writeErr != nil {
					// Version conflict — next login on either side will
					// re-evaluate with the ignored filter active.
					logger.WithFields(map[string]any{"alt_id": altID, "error": writeErr}).Warn("ignored-alts migration: write other history (version conflict?), skipping")
					continue
				}

				delete(history.AlternateMatches, altID)
				brokenLinks++
				affectedSet[obj.UserId] = true
				affectedSet[altID] = true
			}

			if affectedSet[obj.UserId] {
				history.SecondDegreeAlternates = nil
				histData, _ := json.Marshal(history)
				histMeta := history.StorageMeta()
				if _, writeErr := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
					Collection:      histMeta.Collection,
					Key:             histMeta.Key,
					UserID:          obj.UserId,
					Value:           string(histData),
					Version:         histMeta.Version,
					PermissionRead:  histMeta.PermissionRead,
					PermissionWrite: histMeta.PermissionWrite,
				}}); writeErr != nil {
					logger.WithFields(map[string]any{"user_id": obj.UserId, "error": writeErr}).Warn("ignored-alts migration: write history (version conflict?)")
				}
			}
		}

		if nextCursor == "" {
			break
		}
		cursor = nextCursor
	}

	logger.WithFields(map[string]any{
		"broken_links":   brokenLinks,
		"affected_users": len(affectedSet),
	}).Info("ignored-alts migration complete")

	return nil
}

// allItemsIgnored returns true if every Items entry across every match is
// covered by matchIgnoredAltPattern. Returns false on empty input (nothing
// to break).
func allItemsIgnored(matches []*AlternateSearchMatch) bool {
	saw := false
	for _, m := range matches {
		for _, item := range m.Items {
			saw = true
			if !matchIgnoredAltPattern(item) {
				return false
			}
		}
	}
	return saw
}
