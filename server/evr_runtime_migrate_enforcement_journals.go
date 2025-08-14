package server

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/heroiclabs/nakama-common/runtime"
)

var _ = SystemMigrator(&MigrationEnforcementJournals{})

type MigrationEnforcementJournals struct{}

func (m *MigrationEnforcementJournals) MigrateSystem(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) error {

	// Load all enforcement journals and store the
	journals, err := retrieveAllEnforcementJournals(ctx, db)
	if err != nil {
		return err
	}

	count := 0
	for _, journal := range journals {
		adapter := journal.CreateStorableAdapter()
		if err := StorableWrite(ctx, nk, journal.UserID, adapter); err != nil {
			return err
		}
	}

	logger.WithFields(map[string]interface{}{
		"count": count,
	}).Info("Migrated enforcement journals to new storage format")
	return nil
}

func retrieveAllEnforcementJournals(ctx context.Context, db *sql.DB) ([]*GuildEnforcementJournal, error) {
	query := "SELECT userID, value FROM storage WHERE collection = 'EnforcementJournal'"

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var journals []*GuildEnforcementJournal
	for rows.Next() {
		var userID string
		var value string
		var version string
		if err := rows.Scan(&userID, &value, &version); err != nil {
			return nil, err
		}
		journal := GuildEnforcementJournal{}
		if err := json.Unmarshal([]byte(value), &journal); err != nil {
			return nil, err
		}
		journal.version = version
		journals = append(journals, &journal)
	}

	return journals, nil
}
