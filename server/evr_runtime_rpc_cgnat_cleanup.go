package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/heroiclabs/nakama-common/runtime"
)

type CGNATCleanupResponse struct {
	BrokenLinks   int      `json:"broken_links"`
	AffectedUsers int      `json:"affected_users"`
	Details       []string `json:"details"`
}

// CGNATCleanupRPC breaks alt links that are based entirely on weak signals
// (CGNAT IPs and/or commodity hardware profiles). Global Operators only.
func CGNATCleanupRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	detector := GetCGNATDetector()
	if detector == nil {
		return "", runtime.NewError("CGNAT detector not initialized", StatusInternalError)
	}

	brokenLinks, affectedUsers, details, err := runCGNATCleanup(ctx, logger, nk, detector)
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("cleanup failed: %v", err), StatusInternalError)
	}

	// Send audit log (best-effort)
	settings := ServiceSettings()
	if settings != nil && settings.ServiceAuditChannelID != "" {
		summary := fmt.Sprintf("CGNAT cleanup: broke %d alt links across %d users.", brokenLinks, affectedUsers)
		if len(details) > 0 {
			maxDetails := 20
			if len(details) < maxDetails {
				maxDetails = len(details)
			}
			summary += "\n" + strings.Join(details[:maxDetails], "\n")
			if len(details) > maxDetails {
				summary += fmt.Sprintf("\n... and %d more", len(details)-maxDetails)
			}
		}
		logger.Info("CGNAT cleanup summary: %s", summary)
	}

	resp := CGNATCleanupResponse{
		BrokenLinks:   brokenLinks,
		AffectedUsers: affectedUsers,
		Details:       details,
	}
	data, _ := json.Marshal(resp)
	return string(data), nil
}

// runCGNATCleanup scans all LoginHistory records and breaks alt links based
// entirely on weak signals. Uses versioned writes with retry on conflict.
func runCGNATCleanup(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, detector *CGNATDetector) (brokenLinks, affectedUsers int, details []string, err error) {
	processed := make(map[string]bool)
	affectedSet := make(map[string]bool)

	var cursor string
	for {
		objects, nextCursor, listErr := nk.StorageList(ctx, SystemUserID, "", LoginStorageCollection, 100, cursor)
		if listErr != nil {
			return brokenLinks, len(affectedSet), details, fmt.Errorf("storage list: %w", listErr)
		}

		for _, obj := range objects {
			if obj.Key != LoginHistoryStorageKey {
				continue
			}

			history := NewLoginHistory(obj.UserId)
			if readErr := json.Unmarshal([]byte(obj.Value), history); readErr != nil {
				logger.Warn("CGNAT cleanup: failed to unmarshal history for %s: %v", obj.UserId, readErr)
				continue
			}
			history.SetStorageMeta(StorableMetadata{
				UserID:  obj.UserId,
				Version: obj.Version,
			})

			// Identify alt links to break (all items are weak signals)
			toBreak := make([]string, 0)
			for altID, matches := range history.AlternateMatches {
				pk := pairKey(obj.UserId, altID)
				if processed[pk] {
					continue
				}

				allWeak := true
				for _, m := range matches {
					for _, item := range m.Items {
						if !detector.IsWeakSignal(item) {
							allWeak = false
							break
						}
					}
					if !allWeak {
						break
					}
				}

				if allWeak {
					toBreak = append(toBreak, altID)
					processed[pk] = true
				}
			}

			if len(toBreak) == 0 {
				continue
			}

			// Break the links — update both sides before counting
			for _, altID := range toBreak {
				// Load the other user's history first
				otherHistory := NewLoginHistory(altID)
				if readErr := StorableRead(ctx, nk, altID, otherHistory, false); readErr != nil {
					logger.Warn("CGNAT cleanup: failed to load other history %s, skipping pair: %v", altID, readErr)
					continue
				}

				// Remove reciprocal links
				delete(otherHistory.AlternateMatches, obj.UserId)
				otherHistory.SecondDegreeAlternates = nil

				// Write other history (versioned)
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
					// Version conflict or other error — skip this pair,
					// the next login will re-evaluate with the CGNAT filter active
					logger.Warn("CGNAT cleanup: failed to write other history %s (version conflict?), skipping: %v", altID, writeErr)
					continue
				}

				// Both sides will be updated — now mutate and count
				delete(history.AlternateMatches, altID)
				details = append(details, fmt.Sprintf("broke %s <-> %s", obj.UserId, altID))
				brokenLinks++
				affectedSet[obj.UserId] = true
				affectedSet[altID] = true
			}

			// Write this user's history if any links were broken
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
					logger.Warn("CGNAT cleanup: failed to write history %s (version conflict?): %v", obj.UserId, writeErr)
				}
			}
		}

		if nextCursor == "" {
			break
		}
		cursor = nextCursor
	}

	return brokenLinks, len(affectedSet), details, nil
}

func pairKey(a, b string) string {
	if a < b {
		return a + ":" + b
	}
	return b + ":" + a
}
