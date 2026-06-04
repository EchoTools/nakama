package server

import (
	"context"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// fkEnforcingNakamaModule is a storage mock that emulates the real Postgres
// storage table's `FOREIGN KEY (user_id) REFERENCES users(id)` constraint:
// a write whose owner (UserID) is not a known user row is rejected, exactly
// like the production failure behind #417 / #420.
type fkEnforcingNakamaModule struct {
	runtime.NakamaModule
	knownUsers     map[string]bool
	storageObjects map[string]*api.StorageObject
}

func newFKEnforcingNakamaModule(userIDs ...string) *fkEnforcingNakamaModule {
	m := &fkEnforcingNakamaModule{
		knownUsers:     make(map[string]bool, len(userIDs)),
		storageObjects: make(map[string]*api.StorageObject),
	}
	for _, id := range userIDs {
		m.knownUsers[id] = true
	}
	return m
}

func (m *fkEnforcingNakamaModule) key(userID, collection, key string) string {
	return userID + ":" + collection + ":" + key
}

func (m *fkEnforcingNakamaModule) StorageWrite(ctx context.Context, writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	acks := make([]*api.StorageObjectAck, 0, len(writes))
	for _, write := range writes {
		// Emulate the FK constraint: the owner must be an existing user row.
		if !m.knownUsers[write.UserID] {
			return nil, runtime.ErrStorageRejectedPermission
		}
		obj := &api.StorageObject{
			Collection: write.Collection,
			Key:        write.Key,
			UserId:     write.UserID,
			Value:      write.Value,
			Version:    "v1",
		}
		m.storageObjects[m.key(write.UserID, write.Collection, write.Key)] = obj
		acks = append(acks, &api.StorageObjectAck{
			Collection: write.Collection,
			Key:        write.Key,
			UserId:     write.UserID,
			Version:    "v1",
		})
	}
	return acks, nil
}

func (m *fkEnforcingNakamaModule) StorageRead(ctx context.Context, reads []*runtime.StorageRead) ([]*api.StorageObject, error) {
	objects := make([]*api.StorageObject, 0, len(reads))
	for _, read := range reads {
		if obj, ok := m.storageObjects[m.key(read.UserID, read.Collection, read.Key)]; ok {
			objects = append(objects, obj)
		}
	}
	return objects, nil
}

// TestEnforcementMetricsStorageMeta_OwnerIsBotKeyIsGroup verifies the storage
// addressing: the per-guild metrics object is keyed by groupID, while the
// owner is supplied at write time as the bot user (mirroring GuildGroupState).
func TestEnforcementMetricsStorageMeta_OwnerIsBotKeyIsGroup(t *testing.T) {
	t.Parallel()

	groupID := uuid.Must(uuid.NewV4()).String()
	m := NewEnforcementActionMetrics(groupID)
	meta := m.StorageMeta()

	if meta.Collection != StorageCollectionEnforcementMetrics {
		t.Fatalf("collection = %q, want %q", meta.Collection, StorageCollectionEnforcementMetrics)
	}
	if meta.Key != groupID {
		t.Fatalf("Key = %q, want groupID %q (per-guild object keyed by group)", meta.Key, groupID)
	}
	// The owner must NOT be the groupID; it is set to the bot user at write time.
	if meta.UserID == groupID {
		t.Fatalf("StorageMeta().UserID = groupID %q; group must not be the storage owner (FK violation)", groupID)
	}
}

// TestEnforcementMetrics_RoundTripWritesWithBotOwner is the core regression
// test for #417 / #420: a save no longer FK-fails because the owner is the
// (existing) bot user, not the guild groupID; and the group is read back from
// the storage Key, not the owner.
func TestEnforcementMetrics_RoundTripWritesWithBotOwner(t *testing.T) {
	botUserID := uuid.Must(uuid.NewV4()).String()
	groupID := uuid.Must(uuid.NewV4()).String()

	ServiceSettingsUpdate(&ServiceSettingsData{DiscordBotUserID: botUserID})
	t.Cleanup(func() { ServiceSettingsUpdate(&ServiceSettingsData{}) })

	ctx := context.Background()
	// Only the bot user exists; the groupID is deliberately NOT a user row.
	nk := newFKEnforcingNakamaModule(botUserID)

	record := GuildEnforcementRecord{
		GroupID:      groupID,
		UserID:       uuid.Must(uuid.NewV4()).String(),
		RuleViolated: "rule-1",
	}

	// This previously failed with ErrStorageRejectedPermission (groupID owner).
	if err := RecordEnforcementMetrics(ctx, nk, record, true); err != nil {
		t.Fatalf("RecordEnforcementMetrics: unexpected error (FK violation regression): %v", err)
	}

	// The object must be owned by the bot user and keyed by the groupID.
	if _, ok := nk.storageObjects[nk.key(botUserID, StorageCollectionEnforcementMetrics, groupID)]; !ok {
		t.Fatalf("expected stored object owned by bot %q keyed by group %q; have keys %v", botUserID, groupID, keysOf(nk.storageObjects))
	}

	// Read it back: GroupID must come from the Key, not the owner.
	loaded, err := LoadEnforcementMetrics(ctx, nk, groupID)
	if err != nil {
		t.Fatalf("LoadEnforcementMetrics: %v", err)
	}
	if loaded.GroupID != groupID {
		t.Fatalf("loaded GroupID = %q, want %q (must derive from Key, not owner)", loaded.GroupID, groupID)
	}
	if loaded.TotalKicks != 1 {
		t.Fatalf("loaded TotalKicks = %d, want 1 (round-trip lost data)", loaded.TotalKicks)
	}
	if loaded.GroupID == botUserID {
		t.Fatalf("loaded GroupID == botUserID %q; group must not be derived from owner", botUserID)
	}
}

// TestEnforcementMetricsFromStorageObject_GroupFromKey verifies the
// reconstruction path reads the group from the object Key, not the owner.
func TestEnforcementMetricsFromStorageObject_GroupFromKey(t *testing.T) {
	t.Parallel()

	botUserID := uuid.Must(uuid.NewV4()).String()
	groupID := uuid.Must(uuid.NewV4()).String()

	obj := &api.StorageObject{
		Collection: StorageCollectionEnforcementMetrics,
		Key:        groupID,
		UserId:     botUserID,
		Value:      `{"group_id":"","total_kicks":3}`,
		Version:    "v1",
	}

	m, err := EnforcementActionMetricsFromStorageObject(obj)
	if err != nil {
		t.Fatalf("EnforcementActionMetricsFromStorageObject: %v", err)
	}
	if m.GroupID != groupID {
		t.Fatalf("GroupID = %q, want %q (from Key)", m.GroupID, groupID)
	}
	if m.TotalKicks != 3 {
		t.Fatalf("TotalKicks = %d, want 3", m.TotalKicks)
	}
}

func keysOf(m map[string]*api.StorageObject) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
