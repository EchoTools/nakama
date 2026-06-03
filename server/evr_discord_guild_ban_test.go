package server

import (
	"context"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

// banTestNakamaModule is an in-memory NakamaModule mock with last-write-wins
// storage semantics (mirrors real Nakama: one object per Collection/Key/UserID).
type banTestNakamaModule struct {
	runtime.NakamaModule
	objects map[string]*api.StorageObject
}

func newBanTestNakamaModule() *banTestNakamaModule {
	return &banTestNakamaModule{objects: make(map[string]*api.StorageObject)}
}

func banStorageKey(userID, collection, key string) string {
	return userID + ":" + collection + ":" + key
}

func (m *banTestNakamaModule) StorageWrite(ctx context.Context, writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	acks := make([]*api.StorageObjectAck, len(writes))
	for i, w := range writes {
		k := banStorageKey(w.UserID, w.Collection, w.Key)
		m.objects[k] = &api.StorageObject{
			Collection: w.Collection,
			Key:        w.Key,
			UserId:     w.UserID,
			Value:      w.Value,
			Version:    "v1",
		}
		acks[i] = &api.StorageObjectAck{
			Collection: w.Collection,
			Key:        w.Key,
			UserId:     w.UserID,
			Version:    "v1",
		}
	}
	return acks, nil
}

func (m *banTestNakamaModule) StorageRead(ctx context.Context, reads []*runtime.StorageRead) ([]*api.StorageObject, error) {
	objects := make([]*api.StorageObject, 0, len(reads))
	for _, r := range reads {
		if obj, ok := m.objects[banStorageKey(r.UserID, r.Collection, r.Key)]; ok {
			objects = append(objects, obj)
		}
	}
	return objects, nil
}

// loadJournal reads back a user's enforcement journal from the mock storage.
func loadJournal(ctx context.Context, t *testing.T, nk runtime.NakamaModule, userID string) *GuildEnforcementJournal {
	t.Helper()
	journal := NewGuildEnforcementJournal(userID)
	if err := StorableRead(ctx, nk, userID, journal, false); err != nil {
		t.Fatalf("failed to read journal for %s: %v", userID, err)
	}
	return journal
}

// TestGuildBanEnforcement_BlocksAllJoinPaths proves a guild-banned user has an
// active enforcement suspension on the banning guild across ALL game modes.
// lobbyAuthorize (shared by the direct-join and matchmaking/find paths) rejects
// on exactly such a record, so a marker covering evr.AllModes blocks BOTH paths,
// regardless of guild privacy (this is a PUBLIC-guild regression — issue #467).
func TestGuildBanEnforcement_BlocksAllJoinPaths(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	nk := newBanTestNakamaModule()
	d := &DiscordIntegrator{nk: nk, logger: zap.NewNop()}

	groupID := uuid.Must(uuid.NewV4()).String()
	userID := uuid.Must(uuid.NewV4()).String()

	if err := d.writeGuildBanEnforcement(ctx, zap.NewNop(), groupID, userID, "", "", "Discord guild ban"); err != nil {
		t.Fatalf("writeGuildBanEnforcement: %v", err)
	}

	journal := loadJournal(ctx, t, nk, userID)
	enforcements, err := CheckEnforcementSuspensions(
		GuildEnforcementJournalList{userID: journal},
		map[string][]string{},
	)
	if err != nil {
		t.Fatalf("CheckEnforcementSuspensions: %v", err)
	}

	byMode, ok := enforcements[groupID]
	if !ok {
		t.Fatalf("expected an active enforcement for banning guild %s, found none", groupID)
	}

	// The ban must cover every mode the join gate can be invoked for, so that
	// both the direct-join and matchmaking/find paths are rejected.
	for _, mode := range evr.AllModes {
		rec, ok := byMode[mode]
		if !ok {
			t.Fatalf("expected ban enforcement for mode %v, found none", mode)
		}
		if rec.GroupID != groupID {
			t.Fatalf("enforcement record GroupID = %s, want %s", rec.GroupID, groupID)
		}
		if rec.UserID != userID {
			t.Fatalf("enforcement record UserID = %s, want %s", rec.UserID, userID)
		}
		if rec.IsExpired() {
			t.Fatalf("expected a non-expired (lifetime) ban record, got expiry %s", rec.Expiry)
		}
	}
}

// TestGuildBanEnforcement_GuildIsolation proves the HARD guild-isolation
// invariant (AGENTS.md): a ban in guild A must NEVER block joins to guild B.
func TestGuildBanEnforcement_GuildIsolation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	nk := newBanTestNakamaModule()
	d := &DiscordIntegrator{nk: nk, logger: zap.NewNop()}

	groupA := uuid.Must(uuid.NewV4()).String()
	groupB := uuid.Must(uuid.NewV4()).String()
	userID := uuid.Must(uuid.NewV4()).String()

	if err := d.writeGuildBanEnforcement(ctx, zap.NewNop(), groupA, userID, "", "", "Discord guild ban"); err != nil {
		t.Fatalf("writeGuildBanEnforcement: %v", err)
	}

	journal := loadJournal(ctx, t, nk, userID)
	// No inheritance: guild B must not inherit guild A's suspension.
	enforcements, err := CheckEnforcementSuspensions(
		GuildEnforcementJournalList{userID: journal},
		map[string][]string{},
	)
	if err != nil {
		t.Fatalf("CheckEnforcementSuspensions: %v", err)
	}

	if _, ok := enforcements[groupA]; !ok {
		t.Fatalf("expected enforcement for banning guild %s", groupA)
	}
	if _, ok := enforcements[groupB]; ok {
		t.Fatalf("guild isolation violated: ban in guild A produced enforcement for guild B (%s)", groupB)
	}
}
