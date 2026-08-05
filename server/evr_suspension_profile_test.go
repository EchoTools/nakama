package server

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// newSuspensionProfileIndex builds a LocalStorageIndex registered with the REAL
// production SuspensionProfile index configuration -- whatever
// (*SuspensionProfile).StorageIndexes() currently returns. Nothing here is
// hard-coded, so a regression in the production config surfaces as a test
// failure rather than being papered over by a test-local copy.
//
// No database is required: CreateIndex (storage_index.go:661), Write
// (storage_index.go:81) and the IndexOnly branch of List (storage_index.go:328)
// are all served entirely from the in-process bluge index.
func newSuspensionProfileIndex(t *testing.T) (StorageIndex, StorableIndexMeta) {
	t.Helper()

	indexes := (&SuspensionProfile{}).StorageIndexes()
	require.Len(t, indexes, 1, "SuspensionProfile should declare exactly one storage index")
	meta := indexes[0]

	si, err := NewLocalStorageIndex(logger, nil, &StorageConfig{}, metrics)
	require.NoError(t, err)

	require.NoError(t, si.CreateIndex(context.Background(), meta.Name, meta.Collection, meta.Key,
		meta.Fields, meta.SortableFields, meta.MaxEntries, meta.IndexOnly))

	return si, meta
}

// writeProfileToIndex marshals the profile exactly the way StorableWrite does
// (json.Marshal of the Storable) and pushes it through the index writer exactly
// the way storageIndexWrite does (core_storage.go:838).
func writeProfileToIndex(t *testing.T, si StorageIndex, meta StorableIndexMeta, profile *SuspensionProfile) {
	t.Helper()

	data, err := json.Marshal(profile)
	require.NoError(t, err)

	now := time.Now().UTC()
	si.Write(context.Background(), []*api.StorageObject{{
		Collection:      meta.Collection,
		Key:             meta.Key,
		UserId:          profile.UserID,
		Value:           string(data),
		Version:         "v1",
		PermissionRead:  int32(runtimeStoragePermissionOwnerRead),
		PermissionWrite: 0,
		CreateTime:      timestamppb.New(now),
		UpdateTime:      timestamppb.New(now),
	}})
}

const runtimeStoragePermissionOwnerRead = 1

// suspensionProfileFixture builds a profile carrying one active suspension.
func suspensionProfileFixture(userID, groupID string) *SuspensionProfile {
	profile := NewSuspensionProfile(userID)
	profile.Suspensions = []SuspensionProfileRecord{{
		ID:                "record-1",
		GroupID:           groupID,
		UserNotice:        "Toxic Behavior",
		CreatedAt:         time.Now().UTC().Add(-time.Hour),
		ExpiryAt:          time.Now().UTC().Add(24 * time.Hour),
		Duration:          "25h",
		EnforcerUserID:    "enforcer-1",
		EnforcerDiscordID: "1234",
	}}
	return profile
}

// TestSuspensionProfileIndex_ServesSuspensionData is the headline regression
// test for the index configuration.
//
// SuspensionProfile exists for exactly one reason: to be a pre-compiled
// projection of the enforcement journal that can be served straight out of the
// storage index. With IndexOnly: true, storage_index.go:349 returns
// idxResult.Value verbatim -- and idxResult.Value is the FIELD-FILTERED map
// built at storage_index.go:528-536, not the stored object. So if "suspensions"
// is not in Fields, the array is discarded before the document is ever written,
// and every query returns a profile with zero suspensions no matter how many
// bans the user actually has.
func TestSuspensionProfileIndex_ServesSuspensionData(t *testing.T) {
	userID := uuid.Must(uuid.NewV4()).String()
	groupID := uuid.Must(uuid.NewV4()).String()

	si, meta := newSuspensionProfileIndex(t)
	writeProfileToIndex(t, si, meta, suspensionProfileFixture(userID, groupID))

	query := fmt.Sprintf("+value.user_id:%s", Query.EscapeIndexValue(userID))
	objs, _, err := si.List(context.Background(), uuid.Nil, meta.Name, query, 10, nil, "")
	require.NoError(t, err)
	require.Len(t, objs.Objects, 1, "the suspended user's profile should be findable in the index")

	got, err := SuspensionProfileFromStorageObject(objs.Objects[0])
	require.NoError(t, err, "index payload should unmarshal as a SuspensionProfile; raw payload was: %s", objs.Objects[0].Value)

	require.Len(t, got.Suspensions, 1,
		"index served a profile with NO suspension data; raw payload was: %s", objs.Objects[0].Value)
	assert.Equal(t, "record-1", got.Suspensions[0].ID)
	assert.Equal(t, groupID, got.Suspensions[0].GroupID)
	assert.Equal(t, "Toxic Behavior", got.Suspensions[0].UserNotice)
}

// TestSuspensionProfileIndex_QueryableByGroupID pins the other half of the
// Fields contract: Fields governs what is SEARCHABLE as well as what is
// returned, because BlugeWalkDocument (storage_index.go:560) only walks the
// already-filtered map. A portal that cannot ask "who is suspended in guild X"
// cannot use this projection at all.
func TestSuspensionProfileIndex_QueryableByGroupID(t *testing.T) {
	groupID := uuid.Must(uuid.NewV4()).String()
	otherGroupID := uuid.Must(uuid.NewV4()).String()

	si, meta := newSuspensionProfileIndex(t)

	suspendedUser := uuid.Must(uuid.NewV4()).String()
	writeProfileToIndex(t, si, meta, suspensionProfileFixture(suspendedUser, groupID))
	writeProfileToIndex(t, si, meta, suspensionProfileFixture(uuid.Must(uuid.NewV4()).String(), otherGroupID))

	query := fmt.Sprintf("+value.suspensions.group_id:%s", Query.EscapeIndexValue(groupID))
	objs, _, err := si.List(context.Background(), uuid.Nil, meta.Name, query, 10, nil, "")
	require.NoError(t, err)

	require.Len(t, objs.Objects, 1, "index should be queryable by the group a suspension belongs to")
	assert.Equal(t, suspendedUser, objs.Objects[0].UserId)
}

// TestSyncFromJournal_PopulatesAffectedModes covers the mode axis.
//
// The seat check keys suspensions by mode -- recordsByMode[label.Mode]
// (evr_lobby_joinentrant_enforce.go:60) -- and lobbyAuthorize skips records
// whose mode differs (evr_lobby_joinentrant.go:377-380). A projection with no
// mode information cannot answer "is this player suspended from THIS mode".
//
// Note there is no mode FIELD on GuildEnforcementRecord to copy. The mode set
// is DERIVED: CheckEnforcementSuspensions (evr_enforcement_journal.go:399-402)
// applies a record to evr.AllModes normally, and to evr.PublicModes only when
// AllowPrivateLobbies is set. The projection must derive it the same way.
func TestSyncFromJournal_PopulatesAffectedModes(t *testing.T) {
	userID := uuid.Must(uuid.NewV4()).String()
	groupID := uuid.Must(uuid.NewV4()).String()

	for _, tc := range []struct {
		name                string
		allowPrivateLobbies bool
		wantModes           []evr.Symbol
	}{
		{"full suspension applies to all modes", false, evr.AllModes},
		{"private-lobbies-allowed applies to public modes only", true, evr.PublicModes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			journal := NewGuildEnforcementJournal(userID)
			journal.AddRecord(groupID, "enforcer", "1234", "Toxic Behavior", "notes",
				false, tc.allowPrivateLobbies, 24*time.Hour)

			profile := NewSuspensionProfile(userID)
			profile.SyncFromJournal(journal)

			require.Len(t, profile.Suspensions, 1)

			want := make([]string, 0, len(tc.wantModes))
			for _, m := range tc.wantModes {
				want = append(want, m.String())
			}
			assert.ElementsMatch(t, want, profile.Suspensions[0].AffectedModes,
				"profile must record which game modes the suspension applies to")
			assert.Equal(t, tc.allowPrivateLobbies, profile.Suspensions[0].AllowPrivateLobbies)
		})
	}
}

// TestSyncFromJournal_AffectedModesAgreeWithAuthority is the anti-drift test.
//
// The projection is only trustworthy if its mode set is identical to the one
// the authoritative check produces. Rather than restating the derivation, this
// compares the projection against CheckEnforcementSuspensions itself, so any
// future change to the authority that is not mirrored here fails the build.
func TestSyncFromJournal_AffectedModesAgreeWithAuthority(t *testing.T) {
	userID := uuid.Must(uuid.NewV4()).String()
	groupID := uuid.Must(uuid.NewV4()).String()

	for _, allowPrivate := range []bool{false, true} {
		journal := NewGuildEnforcementJournal(userID)
		journal.AddRecord(groupID, "enforcer", "1234", "Toxic Behavior", "notes",
			false, allowPrivate, 24*time.Hour)

		authoritative, err := CheckEnforcementSuspensions(
			GuildEnforcementJournalList{userID: journal}, nil)
		require.NoError(t, err)

		wantModes := make([]string, 0)
		for mode := range authoritative[groupID] {
			wantModes = append(wantModes, mode.String())
		}

		profile := NewSuspensionProfile(userID)
		profile.SyncFromJournal(journal)
		require.Len(t, profile.Suspensions, 1)

		assert.ElementsMatch(t, wantModes, profile.Suspensions[0].AffectedModes,
			"projection mode set must match CheckEnforcementSuspensions (allow_private_lobbies=%v)", allowPrivate)
	}
}

// TestSyncFromJournal_ExcludesVoidedRecords pins the fail-closed contract for
// retracted suspensions.
//
// SyncFromJournal used to COPY VoidedAt/VoidedBy/VoidNotes onto the projected
// record while still emitting the record itself. Every consumer then had to
// remember to check VoidedAt before acting -- and a consumer that forgot would
// enforce a ban a moderator had explicitly retracted. The authoritative path
// has no such trap: ActiveSuspensions drops voided records outright
// (evr_enforcement_journal.go:166).
//
// The projection now matches the authority: voided records do not appear.
func TestSyncFromJournal_ExcludesVoidedRecords(t *testing.T) {
	userID := uuid.Must(uuid.NewV4()).String()
	groupID := uuid.Must(uuid.NewV4()).String()

	journal := NewGuildEnforcementJournal(userID)
	kept := journal.AddRecord(groupID, "enforcer", "1234", "Still banned", "", false, false, 24*time.Hour)
	voided := journal.AddRecord(groupID, "enforcer", "1234", "Retracted ban", "", false, false, 24*time.Hour)
	journal.VoidRecord(groupID, voided.ID, "moderator", "5678", "issued in error")

	profile := NewSuspensionProfile(userID)
	profile.SyncFromJournal(journal)

	ids := make([]string, 0, len(profile.Suspensions))
	for _, s := range profile.Suspensions {
		ids = append(ids, s.ID)
	}

	assert.NotContains(t, ids, voided.ID,
		"a voided suspension must not appear in the projection; a consumer that forgets to check VoidedAt would enforce a retracted ban")
	assert.Contains(t, ids, kept.ID, "non-voided suspensions must still be projected")
	assert.Len(t, profile.Suspensions, 1)
}

// TestSyncFromJournal_VoidedExclusionMatchesAuthority cross-checks the void
// filter against the authoritative check rather than against a literal, so the
// two cannot drift.
func TestSyncFromJournal_VoidedExclusionMatchesAuthority(t *testing.T) {
	userID := uuid.Must(uuid.NewV4()).String()
	groupID := uuid.Must(uuid.NewV4()).String()

	journal := NewGuildEnforcementJournal(userID)
	voided := journal.AddRecord(groupID, "enforcer", "1234", "Retracted ban", "", false, false, 24*time.Hour)
	journal.VoidRecord(groupID, voided.ID, "moderator", "5678", "issued in error")

	authoritative, err := CheckEnforcementSuspensions(GuildEnforcementJournalList{userID: journal}, nil)
	require.NoError(t, err)
	require.Empty(t, authoritative[groupID],
		"precondition: the authority treats a fully-voided journal as carrying no active suspension")

	profile := NewSuspensionProfile(userID)
	profile.SyncFromJournal(journal)

	assert.Empty(t, profile.Suspensions,
		"the projection must agree with the authority that nothing is enforceable here")
}

// TestSyncFromJournal_DoesNotApplyInheritance pins the SCOPE contract of the
// projection: it is self-only and inheritance-free, deliberately.
//
// This is a characterization test for a decision, not a bug. Guild inheritance
// cannot be baked into a per-user projection at enforcement-write time:
// InheritanceByParentGroupID reads an atomic that a registry rebuild REPLACES
// wholesale (evr_guild_group_registry.go:121), so a guild admin re-parenting a
// guild changes the correct answer for every already-written profile, with no
// enforcement write to trigger a resync.
//
// If this test ever fails, someone has started applying inheritance at sync
// time and must first solve the invalidation problem it creates.
func TestSyncFromJournal_DoesNotApplyInheritance(t *testing.T) {
	userID := uuid.Must(uuid.NewV4()).String()
	parentGroupID := uuid.Must(uuid.NewV4()).String()
	childGroupID := uuid.Must(uuid.NewV4()).String()

	journal := NewGuildEnforcementJournal(userID)
	journal.AddRecord(parentGroupID, "enforcer", "1234", "Toxic Behavior", "", false, false, 24*time.Hour)

	// The authority, given an inheritance map, DOES fan the suspension out to
	// the child guild. This is the behaviour the projection deliberately omits.
	inheritance := map[string][]string{parentGroupID: {childGroupID}}
	authoritative, err := CheckEnforcementSuspensions(GuildEnforcementJournalList{userID: journal}, inheritance)
	require.NoError(t, err)
	require.NotEmpty(t, authoritative[childGroupID],
		"precondition: the authority DOES apply inheritance to the child guild")

	profile := NewSuspensionProfile(userID)
	profile.SyncFromJournal(journal)

	groups := make([]string, 0, len(profile.Suspensions))
	for _, s := range profile.Suspensions {
		groups = append(groups, s.GroupID)
	}

	assert.Contains(t, groups, parentGroupID, "the guild that issued the suspension must be present")
	assert.NotContains(t, groups, childGroupID,
		"the projection is inheritance-free by contract; callers needing inherited suspensions must consult CheckEnforcementSuspensions with a live registry inheritance map")
}

// TestSyncFromJournal_IsSelfOnly pins the other half of the scope contract:
// one profile describes exactly one user, never their alts.
//
// Alts cannot be baked in either. enforcementUserIDs is rebuilt on every login
// from dynamically-discovered alternates (evr_pipeline_login.go:603-606), so
// alt membership changes without any enforcement write occurring, and encoding
// it would require fanning a write out to every alt whenever alt detection
// changed its mind.
func TestSyncFromJournal_IsSelfOnly(t *testing.T) {
	userID := uuid.Must(uuid.NewV4()).String()
	groupID := uuid.Must(uuid.NewV4()).String()

	journal := NewGuildEnforcementJournal(userID)
	journal.AddRecord(groupID, "enforcer", "1234", "Toxic Behavior", "", false, false, 24*time.Hour)

	profile := NewSuspensionProfile(userID)
	profile.SyncFromJournal(journal)

	assert.Equal(t, userID, profile.UserID)
	for _, s := range profile.Suspensions {
		assert.Equal(t, groupID, s.GroupID,
			"the projection carries only this user's own records")
	}
}
