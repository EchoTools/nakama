package server

import (
	"context"
	"database/sql"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MigrationVRMLRelinkGroupA re-links VRML accounts for Group A users whose vrml:
// device link was removed as part of the VRML account sharing exploit investigation.
// These accounts were confirmed as legitimate owners (Discord ID on their VRML profile
// matches their Nakama account's linked Discord ID). Re-linking via the normal code
// path will re-assign their cosmetic entitlements.
type MigrationVRMLRelinkGroupA struct{}

var _ SystemMigrator = (*MigrationVRMLRelinkGroupA)(nil)

func (m *MigrationVRMLRelinkGroupA) MigrateSystem(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) error {
	const (
		sentinelCollection = "Migrations"
		sentinelKey        = "VRMLRelinkGroupA"
	)

	// Check if this migration has already run.
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{{
		Collection: sentinelCollection,
		Key:        sentinelKey,
		UserID:     SystemUserID,
	}})
	if err != nil {
		return err
	}
	if len(objs) > 0 {
		logger.Info("MigrationVRMLRelinkGroupA: already completed, skipping")
		return nil
	}
	targets := []struct {
		nakamaUserID string
		vrmlUserID   string
		username     string
	}{
		{"b6f761c8-7fb9-4084-856a-ff2b537f3e55", "rMf6HOOSPSbiaQxIPCLzvw2", ".gwow."},
		{"d2f713d0-c8fb-4fdc-9fb5-6e6608f8a7b3", "XPeIaW3bKEz-IGkcas7LvQ2", "betallic"},
		{"f8522d82-41f6-4852-8007-2bcdbb50bdbe", "EgMXf2oBkD9JFtKjiz30Fg2", "blade_the_intp"},
		{"98863f09-0084-4607-b041-636ce003f38f", "ba61Tv5IKCkIbn1SMBZV1A2", "davejtuck"},
		{"c7dc9197-e05a-4f92-8617-b71b40a49f5a", "nJj7cI2kQLnIq4MIRsRrwg2", "delight.gg"},
		{"9ee8ac07-22f6-4325-8294-645612b7b9c6", "C58Bo7WGBmodtMv8wc-PSg2", "fr0st_0900"},
		{"6080e6a3-f20e-4775-83d3-472adbbb695b", "MEHj11otoxKpAtx-v7c8kQ2", "froggio7"},
		{"4c8eab35-d2d2-4b56-bcc6-32d7dcc30aba", "SiAC06pVccsIvZj3yWk7XQ2", "gishnishgu"},
		{"34ec02ed-e196-4cde-b0ec-110bcbcf58d2", "7GiW_zN2F8ywo4PVmyAhXg2", "him_vv"},
		{"39f4de68-dd09-439f-8cce-5d2c9687a2a3", "GKsurVaHkWnhcmN1bRJihw2", "kara_0033"},
		{"d220ffbc-b7f0-411a-a3dc-6a8e27e17945", "6CrvW4ljdeV7dPw0ePbXOw2", "milkyboivr"},
		{"b36ed9de-6859-4ccd-a42b-21b075be8010", "3JDpYRdXph1aj-01zjQaRg2", "mjb7295"},
		// mockzzyy intentionally excluded: VRML account BVMH3TZUYWjF2RgGYwvmuQ2 belongs to pyfb per VRML discordTag.
		{"abb2bcda-2c21-4a6d-bab8-f7f4049f8a60", "JBjajmjkfnVm9kAkrVeobw2", "mrmarcus04"},
		{"e14aac13-2ccf-4082-8393-a7f2d40fcf68", "Yy5SD1gFUsHv5Ltnxrtdog2", "neighborsneighborr"},
		{"a20d5f91-a4f4-4df0-bc96-6b195a8ca3ae", "svenhutFO9KkOiIN07309A2", "purpld"},
		{"ac622c8f-acca-4b4a-b2f7-0b14c3483261", "C6JkKOVCxA72V2gPc71EOg2", "reallyreals"},
		{"ee88aeda-b3f2-4b90-97a4-209f478e3f5e", "6OTgXNBl8AkEYBCu5RhGMw2", "soapyyyy."},
		{"85c74362-2d9e-4bff-9d0f-72f7d10d48c3", "npGg2LXsM8oNytWAxHAbIQ2", "unscrambledegg1"},
		{"9094075d-9c87-41bb-b533-e9c79210bd87", "Myu0uvAqyGaI1tlUHiHM5A2", "xodazed"}, // no VRMLUser storage record; VRML player ID confirmed via API
	}

	for _, t := range targets {
		l := logger.WithField("nakama_user_id", t.nakamaUserID).WithField("vrml_user_id", t.vrmlUserID).WithField("username", t.username)
		if err := LinkVRMLAccount(ctx, db, nk, t.nakamaUserID, t.vrmlUserID); err != nil {
			l.WithField("error", err.Error()).Warn("MigrationVRMLRelinkGroupA: failed to re-link account")
			continue
		}
		l.Info("MigrationVRMLRelinkGroupA: successfully re-linked VRML account")
	}

	// Clean up mockzzyy's stale Social/VRMLUser storage record — it references a VRML
	// account (BVMH3TZUYWjF2RgGYwvmuQ2) whose discordTag is "pyfb", not mockzzyy.
	if err := nk.StorageDelete(ctx, []*runtime.StorageDelete{{
		Collection: "Social",
		Key:        "VRMLUser",
		UserID:     "d3c22198-50a4-4409-9591-cb069d6900fa", // mockzzyy
	}}); err != nil {
		logger.WithField("error", err).Warn("MigrationVRMLRelinkGroupA: failed to delete mockzzyy stale VRMLUser record")
	}

	// Write the sentinel so this migration never runs again.
	ts := time.Now().UTC().Format(time.RFC3339)
	if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      sentinelCollection,
		Key:             sentinelKey,
		UserID:          SystemUserID,
		Value:           `{"completed_at":"` + ts + `"}`,
		Version:         "*", // only write if it doesn't exist
		PermissionRead:  0,
		PermissionWrite: 0,
	}}); err != nil && status.Code(err) != codes.AlreadyExists {
		return err
	}

	return nil
}
