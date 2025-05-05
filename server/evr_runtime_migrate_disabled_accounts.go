package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var _ = SystemMigrator(&MigrationSuspensions{})

type MigrationSuspensions struct{}

func (m MigrationSuspensions) MigrateSystem(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) error {

	// Load all enforcement journals and store the
	byGroupIDbyUserID, err := retrieveAllLegacySuspensionUserIDs(ctx, db)
	if err != nil {
		return err
	}
	if len(byGroupIDbyUserID) == 0 {
		return nil
	}

	count := 0
	for userID, byGroupID := range byGroupIDbyUserID {
		for groupID, data := range byGroupID {
			logger = logger.WithFields(map[string]any{
				"user_id":  userID,
				"group_id": groupID,
			})
			// Convert the legacy data to the new format
			journal := NewGuildEnforcementJournal(userID)
			if err := StorageRead(ctx, nk, userID, journal, false); err != nil && status.Code(err) != codes.NotFound {
				logger.Warn("Failed to read enforcement journal", err)
				continue
			}
			if err := convertLegacySuspensionToJournal(journal, groupID, data); err != nil {
				logger.Warn("Failed to convert legacy suspension to journal", err)
				continue
			}
			if err := StorageWrite(ctx, nk, userID, journal); err != nil {
				logger.WithFields(map[string]any{
					"error":    err,
					"user_id":  userID,
					"group_id": groupID,
				}).Error("Failed to write enforcement journal")
				continue
			}
			count++

			// Remove the legacy data
			if err := nk.StorageDelete(ctx, []*runtime.StorageDelete{
				{
					Collection: "EnforcementJournal",
					Key:        groupID,
					UserID:     userID,
				},
			}); err != nil {
				logger.WithFields(map[string]any{
					"error":    err,
					"user_id":  userID,
					"group_id": groupID,
				}).Error("Failed to delete legacy enforcement journal")
				continue
			}
		}
	}
	logger.WithFields(map[string]any{
		"count": count,
		"total": len(byGroupIDbyUserID),
	}).Info("Migrated all legacy suspensions to the new format")

	return nil
}

func convertLegacySuspensionToJournal(j *GuildEnforcementJournal, groupID, data string) error {

	type legacyFormat struct {
		Records []struct {
			ID                      string    `json:"id"`
			Notes                   string    `json:"notes"`
			IsVoid                  bool      `json:"is_void"`
			UserID                  string    `json:"user_id"`
			GroupID                 string    `json:"group_id"`
			CreatedAt               time.Time `json:"created_at"`
			UpdatedAt               time.Time `json:"updated_at"`
			EnforcerUserID          string    `json:"enforcer_user_id"`
			SuspensionExpiry        time.Time `json:"suspension_expiry"`
			SuspensionNotice        string    `json:"suspension_notice"`
			EnforcerDiscordID       string    `json:"enforcer_discord_id"`
			CommunityValuesRequired bool      `json:"community_values_required"`
		} `json:"records"`
		UserID                     string    `json:"user_id"`
		GroupID                    string    `json:"group_id"`
		SuspensionExpiry           time.Time `json:"suspension_expiry"`
		IsCommunityValuesRequired  bool      `json:"is_community_values_required"`
		CommunityValuesCompletedAt time.Time `json:"community_values_completed_at"`
	}

	var legacyData legacyFormat
	if err := json.Unmarshal([]byte(data), &legacyData); err != nil {
		return err
	}
	// Convert to the new format

	for _, r := range legacyData.Records {

		record := GuildEnforcementRecord{
			ID:                      r.ID,
			EnforcerUserID:          r.EnforcerUserID,
			EnforcerDiscordID:       r.EnforcerDiscordID,
			CreatedAt:               r.CreatedAt,
			UpdatedAt:               r.UpdatedAt,
			UserNoticeText:          r.SuspensionNotice,
			SuspensionExpiry:        r.SuspensionExpiry,
			AuditorNotes:            r.Notes,
			CommunityValuesRequired: false,
		}

		if j.RecordsByGroupID == nil {
			j.RecordsByGroupID = make(map[string][]GuildEnforcementRecord)
		}

		if j.RecordsByGroupID[r.GroupID] == nil {
			j.RecordsByGroupID[r.GroupID] = make([]GuildEnforcementRecord, 0)
		}

		j.RecordsByGroupID[legacyData.GroupID] = append(j.RecordsByGroupID[legacyData.GroupID], record)

		if r.IsVoid {
			// Add Voided record
			j.VoidRecord(legacyData.GroupID, record.ID, r.EnforcerUserID, r.EnforcerDiscordID, "")
		}
	}

	return nil
}

func retrieveAllLegacySuspensionUserIDs(ctx context.Context, db *sql.DB) (map[string]map[string]string, error) {
	query := "SELECT user_id, key, value FROM storage WHERE collection = 'EnforcementJournal';"

	results := make(map[string]map[string]string, 100)
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var userID, groupID, value string

		if err := rows.Scan(&userID, &groupID, &value); err != nil {
			return nil, err
		}

		if _, ok := results[userID]; !ok {
			results[userID] = make(map[string]string)
		}
		results[userID][groupID] = value
	}

	return results, nil
}
