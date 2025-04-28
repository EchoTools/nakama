package server

import (
	"context"
	"database/sql"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type disabledAccount struct {
	UserID     string    `json:"user_id"`
	Reason     string    `json:"reason"`
	DisabledAt time.Time `json:"disabled_at"`
}

var _ = SystemMigrator(&MigrationDisabledAccountsToSuspensions{})

type MigrationDisabledAccountsToSuspensions struct{}

func (m *MigrationDisabledAccountsToSuspensions) MigrateSystem(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) error {

	// Load all enforcement journals and store the
	disabledAccounts, err := retrieveAllDisabledUserIDs(ctx, db)
	if err != nil {
		return err
	}

	// Wait for the service settings to load
	var settings *ServiceSettingsData
	for {
		if s := serviceSettings.Load(); s != nil {
			settings = s
			break
		}
		<-time.After(1 * time.Second)
	}

	groupID := "c67fa242-31d8-4cb1-937a-ad6763343e21"
	enforcerUserID := settings.DiscordBotUserID
	enforcerDiscordID := "1180461747488956496"
	expiry := time.Now().AddDate(100, 0, 0)
	count := 0
	userUUIDs := make([]uuid.UUID, 0, len(disabledAccounts))

	for _, a := range disabledAccounts {
		count++
		journal := NewGuildEnforcementJournal(a.UserID, groupID)
		if err := StorageRead(ctx, nk, a.UserID, journal, false); err != nil && status.Code(err) != codes.NotFound {
			return err
		}

		record := NewGuildEnforcementRecord(enforcerUserID, enforcerDiscordID, a.UserID, groupID, "Account is Globally Banned", a.Reason, false, expiry)
		journal.AddRecord(record)

		if err := StorageWrite(ctx, nk, a.UserID, journal); err != nil {
			logger.WithFields(map[string]any{
				"error":       err,
				"user_id":     a.UserID,
				"reason":      a.Reason,
				"disabled_at": a.DisabledAt,
			}).Error("Failed to write enforcement journal")
			continue
		}

		userUUIDs = append(userUUIDs, uuid.FromStringOrNil(a.UserID))
	}
	sessionCache := NewLocalSessionCache(100, 100)

	if err := UnbanUsers(ctx, RuntimeLoggerToZapLogger(logger), db, sessionCache, userUUIDs); err != nil {
		logger.WithFields(map[string]any{
			"error": err,
		}).Error("Failed to unban users")
		return err
	}

	sessionCache.Stop()
	logger.WithFields(map[string]any{
		"count": count,
	}).Info("Migrated disabled accounts to suspensions")
	return nil
}

func retrieveAllDisabledUserIDs(ctx context.Context, db *sql.DB) ([]disabledAccount, error) {
	query := "SELECT id, disable_time, metadata->>'global_ban_reason' FROM users WHERE disable_time != '1970-01-01 00:00:00+00';"

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	accounts := make([]disabledAccount, 0)
	for rows.Next() {
		var userID string
		var disable_time pgtype.Timestamptz
		var reasonStr sql.NullString
		if err := rows.Scan(&userID, &disable_time, &reasonStr); err != nil {
			return nil, err
		}
		var reason string
		if reasonStr.Valid {
			reason = reasonStr.String
		} else {
			reason = "No reason provided"
		}

		accounts = append(accounts, disabledAccount{
			UserID:     userID,
			Reason:     reason,
			DisabledAt: disable_time.Time,
		})

	}

	return accounts, nil
}
