package server

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
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
// No database is required: CreateIndex (storage_index.go:695), Write
// (storage_index.go:83) and the IndexOnly branch of List (storage_index.go:340)
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
// storage index. With IndexOnly: true, storage_index.go:360 returns
// idxResult.Value verbatim -- and idxResult.Value is the FIELD-FILTERED map
// built at storage_index.go:562-570, not the stored object. So if "suspensions"
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
// returned, because BlugeWalkDocument (storage_index.go:599) only walks the
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
// whose mode differs (evr_lobby_joinentrant.go:372-375). A projection with no
// mode information cannot answer "is this player suspended from THIS mode".
//
// Note there is no mode FIELD on GuildEnforcementRecord to copy. The mode set
// is DERIVED: EnforcementRecordAffectedModes (evr_enforcement_journal.go:395)
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

// syncTestNK is a storage double that models the ONE property these tests care
// about: the transaction boundary.
//
// Both write paths funnel into storageWriteObjects inside a single
// ExecuteInTxPgx -- StorageWriteObjects at core_storage.go:587 and MultiUpdate
// at core_multi.go:37. So one call of either kind is all-or-nothing, and two
// calls are two independent transactions. The double applies a batch only if
// every op in it is accepted, and records each batch so a test can assert how
// many transactions a code path actually used.
type syncTestNK struct {
	runtime.NakamaModule
	objects map[string]string // "collection/key" -> value
	batches [][]string        // collections seen, per write call
	// rejectCollection fails any batch containing this collection.
	rejectCollection string
	// failWritesUntil rejects the first N write calls with a version conflict,
	// to drive the retry loop.
	failWritesUntil int
	writeCalls      int
	// enforceVersions turns on optimistic-concurrency checking, so the double
	// can model a genuine lost race rather than just a canned error.
	enforceVersions bool
	versions        map[string]string // "collection/key" -> current version
}

func newSyncTestNK() *syncTestNK {
	return &syncTestNK{
		objects:  make(map[string]string),
		versions: make(map[string]string),
	}
}

// applyBatch is the shared transaction body for both StorageWrite and
// MultiUpdate, so the double cannot accidentally give one path stronger
// guarantees than the other.
func (m *syncTestNK) applyBatch(writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	collections := make([]string, 0, len(writes))
	for _, w := range writes {
		collections = append(collections, w.Collection)
	}
	m.batches = append(m.batches, collections)
	m.writeCalls++

	if m.writeCalls <= m.failWritesUntil {
		return nil, runtime.ErrStorageRejectedVersion
	}

	// Transaction semantics: reject the WHOLE batch, persisting nothing.
	for _, w := range writes {
		if m.rejectCollection != "" && w.Collection == m.rejectCollection {
			return nil, runtime.ErrStorageRejectedVersion
		}
		if m.enforceVersions {
			k := w.Collection + "/" + w.Key
			current, exists := m.versions[k]
			switch {
			case w.Version == "":
				// Unconditional write.
			case w.Version == "*":
				if exists {
					return nil, runtime.ErrStorageRejectedVersion
				}
			case !exists || w.Version != current:
				return nil, runtime.ErrStorageRejectedVersion
			}
		}
	}

	acks := make([]*api.StorageObjectAck, 0, len(writes))
	for i, w := range writes {
		k := w.Collection + "/" + w.Key
		m.objects[k] = w.Value
		newVersion := fmt.Sprintf("w%d-%d", m.writeCalls, i)
		m.versions[k] = newVersion
		acks = append(acks, &api.StorageObjectAck{
			Collection: w.Collection, Key: w.Key, UserId: w.UserID, Version: newVersion,
		})
	}
	return acks, nil
}

// seed installs an object at a known version, standing in for a concurrent
// writer that won the race before our caller attempted its write.
func (m *syncTestNK) seed(collection, key, value, version string) {
	m.objects[collection+"/"+key] = value
	m.versions[collection+"/"+key] = version
}

func (m *syncTestNK) MultiUpdate(ctx context.Context, accountUpdates []*runtime.AccountUpdate, storageWrites []*runtime.StorageWrite, storageDeletes []*runtime.StorageDelete, walletUpdates []*runtime.WalletUpdate, updateLedger bool) ([]*api.StorageObjectAck, []*runtime.WalletUpdateResult, error) {
	acks, err := m.applyBatch(storageWrites)
	return acks, nil, err
}

func (m *syncTestNK) StorageRead(ctx context.Context, reads []*runtime.StorageRead) ([]*api.StorageObject, error) {
	objs := make([]*api.StorageObject, 0, len(reads))
	for _, r := range reads {
		if v, ok := m.objects[r.Collection+"/"+r.Key]; ok {
			version := m.versions[r.Collection+"/"+r.Key]
			if version == "" {
				version = "v1"
			}
			objs = append(objs, &api.StorageObject{
				Collection: r.Collection, Key: r.Key, UserId: r.UserID, Value: v, Version: version,
			})
		}
	}
	return objs, nil
}

func (m *syncTestNK) StorageWrite(ctx context.Context, writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	return m.applyBatch(writes)
}

// TestSyncJournalAndProfile_IsAtomic covers the split-write bug.
//
// SyncJournalAndProfile issued the journal write and the profile write as two
// separate StorableWrite calls, hence two separate transactions. If the second
// failed, the journal -- the AUTHORITY -- had already advanced while the
// projection still described the previous state. The projection lagged in the
// PERMISSIVE direction: a freshly-issued ban was absent from the profile.
//
// Writing both objects in a single StorageWrite call puts them in one
// ExecuteInTxPgx transaction (core_storage.go:587), so the pair cannot split.
func TestSyncJournalAndProfile_IsAtomic(t *testing.T) {
	userID := uuid.Must(uuid.NewV4()).String()
	groupID := uuid.Must(uuid.NewV4()).String()

	nk := newSyncTestNK()
	journal := NewGuildEnforcementJournal(userID)
	journal.AddRecord(groupID, "enforcer", "1234", "Toxic Behavior", "", false, false, 24*time.Hour)

	require.NoError(t, SyncJournalAndProfile(context.Background(), nk, userID, journal))

	// Ignore the read-through create of an absent profile; look at the batch
	// that actually carries the journal.
	var journalBatch []string
	for _, b := range nk.batches {
		for _, c := range b {
			if c == StorageCollectionEnforcementJournal {
				journalBatch = b
			}
		}
	}
	require.NotNil(t, journalBatch, "the journal must have been written")

	assert.ElementsMatch(t,
		[]string{StorageCollectionEnforcementJournal, StorageCollectionSuspensionProfile},
		journalBatch,
		"journal and profile must be written in ONE StorageWrite call so they share a transaction; got batches %v", nk.batches)
}

// TestSyncJournalAndProfile_ProfileFailureDoesNotAdvanceJournal is the
// consequence that actually matters in production: if the projection write
// fails, the authority must not have advanced without it.
func TestSyncJournalAndProfile_ProfileFailureDoesNotAdvanceJournal(t *testing.T) {
	userID := uuid.Must(uuid.NewV4()).String()
	groupID := uuid.Must(uuid.NewV4()).String()

	nk := newSyncTestNK()
	nk.rejectCollection = StorageCollectionSuspensionProfile

	journal := NewGuildEnforcementJournal(userID)
	journal.AddRecord(groupID, "enforcer", "1234", "Toxic Behavior", "", false, false, 24*time.Hour)

	err := SyncJournalAndProfile(context.Background(), nk, userID, journal)
	require.Error(t, err, "a failed projection write must surface as an error, not be swallowed")

	_, journalPersisted := nk.objects[StorageCollectionEnforcementJournal+"/"+StorageKeyEnforcementJournal]
	assert.False(t, journalPersisted,
		"the journal must NOT have advanced when the profile write failed; a suspension recorded in the authority but missing from the projection is a silent permissive drift")
}

// journalBatchOf returns the write batch that carried the enforcement journal.
func journalBatchOf(t *testing.T, nk *syncTestNK) []string {
	t.Helper()
	var found []string
	for _, b := range nk.batches {
		for _, c := range b {
			if c == StorageCollectionEnforcementJournal {
				found = b
			}
		}
	}
	require.NotNil(t, found, "the journal must have been written; batches were %v", nk.batches)
	return found
}

// TestSyncJournalAndProfileWithRetry_IsAtomic is the retry-path counterpart to
// TestSyncJournalAndProfile_IsAtomic.
//
// This is the MORE exposed of the two functions -- it has five call sites to
// the plain variant's three -- and it carried the same split-write defect: the
// journal and the projection went out as two separate StorableWrite calls,
// hence two independent transactions. A failure between them advanced the
// authority while leaving the projection describing the previous state, in the
// PERMISSIVE direction.
//
// Both objects must now go out in a single nk.MultiUpdate, which commits
// everything inside one ExecuteInTxPgx (core_multi.go:37).
func TestSyncJournalAndProfileWithRetry_IsAtomic(t *testing.T) {
	userID := uuid.Must(uuid.NewV4()).String()
	groupID := uuid.Must(uuid.NewV4()).String()

	nk := newSyncTestNK()
	journal := NewGuildEnforcementJournal(userID)
	journal.AddRecord(groupID, "enforcer", "1234", "Toxic Behavior", "", false, false, 24*time.Hour)

	require.NoError(t, SyncJournalAndProfileWithRetry(context.Background(), nk, userID, journal))

	assert.ElementsMatch(t,
		[]string{StorageCollectionEnforcementJournal, StorageCollectionSuspensionProfile},
		journalBatchOf(t, nk),
		"journal and profile must be written in ONE call so they share a transaction; got batches %v", nk.batches)
}

// TestSyncJournalAndProfileWithRetry_ProfileFailureDoesNotAdvanceJournal is the
// consequence that matters in production: a failed projection write must not
// leave the authority advanced without it.
func TestSyncJournalAndProfileWithRetry_ProfileFailureDoesNotAdvanceJournal(t *testing.T) {
	userID := uuid.Must(uuid.NewV4()).String()
	groupID := uuid.Must(uuid.NewV4()).String()

	nk := newSyncTestNK()
	nk.rejectCollection = StorageCollectionSuspensionProfile

	journal := NewGuildEnforcementJournal(userID)
	journal.AddRecord(groupID, "enforcer", "1234", "Toxic Behavior", "", false, false, 24*time.Hour)

	err := SyncJournalAndProfileWithRetry(context.Background(), nk, userID, journal)
	require.Error(t, err, "a failed projection write must surface as an error, not be swallowed")

	_, journalPersisted := nk.objects[StorageCollectionEnforcementJournal+"/"+StorageKeyEnforcementJournal]
	assert.False(t, journalPersisted,
		"the journal must NOT have advanced when the profile write failed; a suspension in the authority but missing from the projection is a silent permissive drift")
}

// TestSyncJournalAndProfileWithRetry_RetriesAtomically pins that the retry loop
// survives the switch to MultiUpdate.
//
// The version-conflict sentinel must still be recognised through the new write
// path -- isVersionConflictError matches on the "version check failed"
// substring (evr_server_profile_storage.go:354), and MultiUpdate returns
// runtime.ErrStorageRejectedVersion whose text carries it. If that link broke,
// a conflict would fail immediately instead of retrying, and this test would
// see a single batch and no persisted journal.
func TestSyncJournalAndProfileWithRetry_RetriesAtomically(t *testing.T) {
	userID := uuid.Must(uuid.NewV4()).String()
	groupID := uuid.Must(uuid.NewV4()).String()

	nk := newSyncTestNK()
	nk.failWritesUntil = 1 // first write call conflicts, second succeeds

	journal := NewGuildEnforcementJournal(userID)
	journal.AddRecord(groupID, "enforcer", "1234", "Toxic Behavior", "", false, false, 24*time.Hour)

	require.NoError(t, SyncJournalAndProfileWithRetry(context.Background(), nk, userID, journal),
		"a single version conflict must be retried, not surfaced; batches were %v", nk.batches)

	// Every batch that touched storage must have carried BOTH objects: a retry
	// must not degrade into single-object writes.
	for i, b := range nk.batches {
		assert.ElementsMatch(t,
			[]string{StorageCollectionEnforcementJournal, StorageCollectionSuspensionProfile}, b,
			"batch %d was not atomic; batches were %v", i, nk.batches)
	}

	value, ok := nk.objects[StorageCollectionEnforcementJournal+"/"+StorageKeyEnforcementJournal]
	require.True(t, ok, "the journal must be persisted after a successful retry")
	assert.Contains(t, value, "Toxic Behavior",
		"the retry must re-apply the caller's mutation, not write a payload that lost it")
}

// TestSyncJournalAndProfileWithRetry_NeverSucceedsAfterDiscardingAMutation is
// the invariant that actually matters in a race, stated by the repo owner as:
// "errors are okay, retries are picking a winner in a race."
//
// Losing a race is fine. Reporting SUCCESS while silently dropping the winner's
// record is not. The original loop re-read the stored journal, adopted only its
// VERSION, and then wrote the caller's stale in-memory contents on top at that
// version -- destroying whatever the winner had recorded and returning nil.
//
// This function cannot re-apply on conflict even in principle: it receives an
// ALREADY-MUTATED journal, so it does not know which records the caller just
// added. Re-applying is the caller's job because only the caller knows the
// mutation. A low-level helper that retries anyway has no recourse but to try
// blindly, which is exactly how a ban record gets lost.
//
// So the bar is: either return an error, or leave the winner's data intact.
// Never both-succeed-and-discard.
func TestSyncJournalAndProfileWithRetry_NeverSucceedsAfterDiscardingAMutation(t *testing.T) {
	userID := uuid.Must(uuid.NewV4()).String()
	groupID := uuid.Must(uuid.NewV4()).String()

	nk := newSyncTestNK()
	nk.enforceVersions = true

	// A concurrent moderator already recorded a ban and won the race.
	winner := NewGuildEnforcementJournal(userID)
	winner.AddRecord(groupID, "enforcer-A", "1111", "WINNER RECORD", "", false, false, 48*time.Hour)
	winnerJSON, err := json.Marshal(winner)
	require.NoError(t, err)
	nk.seed(StorageCollectionEnforcementJournal, StorageKeyEnforcementJournal, string(winnerJSON), "winner-version")

	// Our caller mutated a journal it read BEFORE the winner committed, so it
	// holds a stale version and does not contain the winner's record.
	loser := NewGuildEnforcementJournal(userID)
	loser.AddRecord(groupID, "enforcer-B", "2222", "LOSER RECORD", "", false, false, 24*time.Hour)
	meta := loser.StorageMeta()
	meta.Version = "stale-version"
	loser.SetStorageMeta(meta)

	syncErr := SyncJournalAndProfileWithRetry(context.Background(), nk, userID, loser)

	stored := nk.objects[StorageCollectionEnforcementJournal+"/"+StorageKeyEnforcementJournal]

	if syncErr == nil {
		// Success is only honest if the winner's record survived.
		assert.Contains(t, stored, "WINNER RECORD",
			"returned success while DISCARDING the concurrent winner's ban record -- a silently lost enforcement action. Either fail the race honestly or preserve the winner's data. Stored journal was: %s", stored)
	} else {
		// Losing the race and saying so is correct. The winner must be intact.
		assert.Contains(t, stored, "WINNER RECORD",
			"the winner's record must survive a lost race; stored journal was: %s", stored)
	}
}

// TestSyncFromJournal_IsSelfOnly pins the other half of the scope contract:
// one profile describes exactly one user, never their alts.
//
// Alts cannot be baked in either. enforcementUserIDs is rebuilt on every login
// from dynamically-discovered alternates (evr_pipeline_login.go:602-606), so
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
