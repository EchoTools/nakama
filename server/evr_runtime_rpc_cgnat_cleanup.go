package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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
		logger.WithField("summary", summary).Info("CGNAT cleanup summary")
	}

	resp := CGNATCleanupResponse{
		BrokenLinks:   brokenLinks,
		AffectedUsers: affectedUsers,
		Details:       details,
	}
	data, _ := json.Marshal(resp)
	return string(data), nil
}

// cgnatStartupReadyWait bounds how long the startup cleanup waits for ASN
// data. Stored ranges make it ready as soon as settings arrive; the bound
// covers a first boot with nothing stored, which needs both downloads (each
// capped at asnDownloadTimeout) to succeed.
const cgnatStartupReadyWait = 10 * time.Minute

// runCGNATStartupCleanup runs the retroactive cleanup once ASN data is ready,
// if settings enable it. Not being ready within cgnatStartupReadyWait is logged
// whether or not cleanup is enabled: until it is, every address outside the
// configured CIDRs is treated as shared and IP-based alt signals are dark.
func runCGNATStartupCleanup(logger runtime.Logger, nk runtime.NakamaModule, detector *CGNATDetector) {
	ctx, cancel := context.WithTimeout(context.Background(), cgnatStartupReadyWait)
	defer cancel()
	if err := detector.WaitASNDataReady(ctx); err != nil {
		logger.WithField("error", err).Warn("CGNAT: ASN data not ready after startup; addresses outside the configured CIDRs are treated as shared until a refresh succeeds")
		return
	}
	if !ServiceSettings().CGNAT.CleanupOnStartup {
		return
	}
	brokenLinks, affectedUsers, _, err := runCGNATCleanup(context.Background(), logger, nk, detector)
	if err != nil {
		logger.WithField("error", err).Warn("CGNAT: startup cleanup failed")
	} else if brokenLinks > 0 {
		logger.WithFields(map[string]any{"broken_links": brokenLinks, "affected_users": affectedUsers}).Info("CGNAT: startup cleanup completed")
	}
}

// runCGNATCleanup scans all LoginHistory records and breaks alt links based
// entirely on weak signals. Uses versioned writes with retry on conflict.
//
// Refuses with ErrASNDataNotReady unless the detector is ready. This is the one
// kind of caller whose safe direction is the opposite of IsCGNAT's: it acts on
// a POSITIVE weak verdict, and without ASN data every public address is weak,
// so running would break every IP-only link in the database.
func runCGNATCleanup(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, detector *CGNATDetector) (brokenLinks, affectedUsers int, details []string, err error) {
	if !detector.ASNDataReady() {
		return 0, 0, nil, fmt.Errorf("CGNAT cleanup: %w", ErrASNDataNotReady)
	}

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
				logger.WithFields(map[string]interface{}{"user_id": obj.UserId, "error": readErr}).Warn("CGNAT cleanup: failed to unmarshal history")
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
					logger.WithFields(map[string]interface{}{"alt_id": altID, "error": readErr}).Warn("CGNAT cleanup: failed to load other history, skipping pair")
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
					logger.WithFields(map[string]interface{}{"alt_id": altID, "error": writeErr}).Warn("CGNAT cleanup: failed to write other history (version conflict?), skipping")
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
					logger.WithFields(map[string]interface{}{"user_id": obj.UserId, "error": writeErr}).Warn("CGNAT cleanup: failed to write history (version conflict?)")
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
