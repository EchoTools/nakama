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

// cgnatStartupCleanupDeps is what runCGNATStartupCleanup depends on, injected
// so the boot ordering can be tested without a database.
type cgnatStartupCleanupDeps struct {
	logger runtime.Logger
	// settings returns the current service settings; ServiceSettings in production.
	settings func() *ServiceSettingsData
	// settingsLoaded is closed once the service settings have been loaded.
	settingsLoaded <-chan struct{}
	// settingsWait bounds how long to wait for settingsLoaded.
	settingsWait time.Duration
	// cleanup runs the retroactive cleanup; runCGNATCleanup in production.
	cleanup func(ctx context.Context) (brokenLinks, affectedUsers int, err error)
}

// newCGNATStartupCleanupDeps wires runCGNATStartupCleanup to the process-wide
// settings, the signal ServiceSettingsLoad closes, and runCGNATCleanup.
func newCGNATStartupCleanupDeps(logger runtime.Logger, nk runtime.NakamaModule, detector *CGNATDetector) cgnatStartupCleanupDeps {
	return cgnatStartupCleanupDeps{
		logger:         logger,
		settings:       ServiceSettings,
		settingsLoaded: serviceSettingsLoaded.done(),
		settingsWait:   cgnatStartupSettingsWait,
		cleanup: func(ctx context.Context) (int, int, error) {
			brokenLinks, affectedUsers, _, err := runCGNATCleanup(ctx, logger, nk, detector)
			return brokenLinks, affectedUsers, err
		},
	}
}

// cgnatStartupSettingsWait bounds how long the startup cleanup waits for the
// first ServiceSettingsLoad. That load runs synchronously in NewEvrPipeline,
// moments after InitializeEvrRuntimeModule returns, and its failure is fatal,
// so the bound is reached only if the load never runs or is stalled for
// minutes (a hung storage read). Either way the cleanup is skipped for that
// boot and the skip is logged as a Warn.
const cgnatStartupSettingsWait = 5 * time.Minute

// runCGNATStartupCleanup runs the retroactive cleanup once at startup, if the
// loaded settings enable it, and logs the outcome on every path.
//
// It waits for the first settings load before reading CleanupOnStartup. The
// goroutine that runs it is started from InitializeEvrRuntimeModule, inside
// server.NewRuntime, before NewEvrPipeline does the first ServiceSettingsLoad;
// until then ServiceSettings() is a zero struct, so reading the flag at once
// always saw false and the cleanup never ran. Until #653 the RefreshASNData
// download ahead of this check took long enough to hide that ordering.
//
// Waiting here, rather than starting the cleanup from ServiceSettingsLoad,
// keeps a LoginHistory scan out of the synchronous boot load and out of the
// 30 s poll that shares it, and leaves the cleanup and detector wiring as
// they were.
func runCGNATStartupCleanup(ctx context.Context, d cgnatStartupCleanupDeps) {
	timer := time.NewTimer(d.settingsWait)
	defer timer.Stop()
	select {
	case <-d.settingsLoaded:
	case <-timer.C:
		d.logger.WithField("waited", d.settingsWait.String()).Warn("CGNAT: startup cleanup skipped: service settings did not load in time")
		return
	}

	if s := d.settings(); s == nil || !s.CGNAT.CleanupOnStartup {
		d.logger.Info("CGNAT: startup cleanup skipped: cleanup_on_startup is off")
		return
	}

	brokenLinks, affectedUsers, cleanupErr := d.cleanup(ctx)
	if cleanupErr != nil {
		d.logger.WithField("error", cleanupErr).Warn("CGNAT: startup cleanup failed")
		return
	}
	// Logged at zero too, so that silence cannot mean either "found nothing"
	// or "never ran".
	d.logger.WithFields(map[string]any{"broken_links": brokenLinks, "affected_users": affectedUsers}).Info("CGNAT: startup cleanup completed")
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
