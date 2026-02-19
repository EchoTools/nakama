package server

import (
	"context"
	"database/sql"

	"github.com/heroiclabs/nakama-common/runtime"
)

// MigrationVRMLRelink re-links VRML accounts for users who have a VRML summary
// stored but no corresponding vrml: device link (i.e. they linked then unlinked).
// Their Discord ID matched the VRML account's Discord ID, confirming they are the
// legitimate owner. Re-linking via the normal code path assigns their cosmetic
// entitlements.
type MigrationVRMLRelink struct{}

var _ SystemMigrator = (*MigrationVRMLRelink)(nil)

func (m *MigrationVRMLRelink) MigrateSystem(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) error {
	// Accounts confirmed as legitimate owners (VRML summary discord == nakama custom_id)
	// that are missing their vrml: device link.
	targets := []struct {
		nakamaUserID string
		vrmlUserID   string
		username     string
	}{
		{"7e3a3859-9dc5-4f80-87c1-402458aa943f", "evYlySTnFaLt1Fss4Va6FQ2", "bomb09"},
		{"70a5ffd7-5737-45b7-ace6-ef2f054d799d", "FAYKn90V6HYdJYxElzi1eQ2", "bootsie_wootsie."},
		{"22dff475-850d-4afb-a875-f7644e0ba737", "aMar83BegII3UpHnuxdZXw2", "bradyn_a"},
		{"0c575055-fb07-4650-b572-ed4d4131ddbe", "jpIo0E8yerXpLPyAe57DZw2", "chromat1k."},
		{"20cccd15-87ef-4193-bd3b-47c2410a0bbf", "aYwD-x8L37SgU8PUr_1vLQ2", "clown.y"},
		{"c7c14b8f-c2d8-4b85-a27e-e3ab21ee020c", "SoakBrjVcTFl1Kzhc4zGPA2", ".ilyfaint"},
		{"022b27e0-f4a4-493e-a234-57f08052f7e4", "ScpeU5Q5OWr9F4mcAsuaWQ2", "itzskyyy"},
		{"4d4abe1f-9ee2-490a-aa97-1cec14610572", "8u8ipS9T7H3XyYzeKMfXSQ2", "javabava"},
		{"12bdd6a6-8ed0-4510-870d-1fd2983ed110", "0xYR71bzLW3fiQt8Q8ZzcQ2", "mahdiisdumb."},
		{"0f93cd92-5973-4983-87b8-75a1dadf19ea", "RSPiGqXX1U27hEUwRbeG9w2", "trulyball99"},
		{"955e0179-6a17-4bec-9b58-7b08e53591ce", "2PyTLhFmEuetwKlq4F3oHQ2", "vqxpr"},
	}

	for _, t := range targets {
		l := logger.WithField("nakama_user_id", t.nakamaUserID).WithField("vrml_user_id", t.vrmlUserID).WithField("username", t.username)
		if err := LinkVRMLAccount(ctx, db, nk, t.nakamaUserID, t.vrmlUserID); err != nil {
			l.WithField("error", err.Error()).Warn("MigrationVRMLRelink: failed to re-link account")
			continue
		}
		l.Info("MigrationVRMLRelink: successfully re-linked VRML account")
	}

	return nil
}
