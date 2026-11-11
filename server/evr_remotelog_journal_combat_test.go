package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

// The raw remote logs a client sends are journaled per user in storage
// (RemoteLogs/journal, evr_remotelog_journal.go) after the filter at
// evr_remotelogset.go:14-35 and before any parsing. That journal is the
// history a backfill replays through the fixed parser, so both combat
// post-match messages have to survive the filter and land in it verbatim.
func TestRemoteLogJournal_KeepsCombatPostMatchStatsVerbatim(t *testing.T) {
	typeStats := string(readServerRemoteLogFixture(t, "post_match_match_type_stats_combat.json"))
	matchStats := string(readServerRemoteLogFixture(t, "post_match_match_stats_combat.json"))
	// A message the filter drops, so the test also proves the filter ran.
	filtered := `{"message":"Podium Interaction","message_type":"PODIUM_INTERACTION"}`

	if got := filterRemoteLogs([]string{typeStats, matchStats, filtered}); len(got) != 2 || got[0] != typeStats || got[1] != matchStats {
		t.Fatalf("filterRemoteLogs kept %d of the 3 logs: %q", len(got), got)
	}

	nk := newEvrTestNakamaModule()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry := NewUserRemoteLogJournalRegistry(ctx, zap.NewNop(), nk, NewLocalSessionRegistry(nil))

	userID := uuid.Must(uuid.FromString(combatTestUserID))
	sessionID := uuid.Must(uuid.FromString(combatTestSessionID))

	// evr_remotelogset.go:85-89, minus the session plumbing: EvrPipeline.nk is
	// the concrete runtime, which this package's tests cannot build without a
	// database.
	registry.Add(sessionID, userID, filterRemoteLogs([]string{typeStats, matchStats, filtered}))

	// The registry's worker finishes handling a queued batch before it can
	// observe cancellation, so once the queue is empty, cancelling flushes
	// exactly that batch -- the same write it makes at shutdown.
	waitFor(t, "journal queue to drain", func() bool { return len(registry.queueCh) == 0 })
	cancel()

	var objs []*apiStorageObject
	waitFor(t, "journal storage write", func() bool {
		objs = readJournal(t, nk, combatTestUserID)
		return len(objs) > 0
	})

	var journal map[time.Time][]*RemoteUserLogMessage
	if err := json.Unmarshal([]byte(objs[0].Value), &journal); err != nil {
		t.Fatalf("decode journal: %v", err)
	}
	kept := map[string]uuid.UUID{}
	for _, msgs := range journal {
		for _, m := range msgs {
			kept[m.Message] = m.SessionID
		}
	}
	for name, raw := range map[string]string{"POST_MATCH_MATCH_TYPE_STATS": typeStats, "POST_MATCH_MATCH_STATS": matchStats} {
		sid, ok := kept[raw]
		if !ok {
			t.Errorf("%s is not in the journal verbatim", name)
		} else if sid != sessionID {
			t.Errorf("%s journaled under session %s, want %s", name, sid, sessionID)
		}
	}
	if _, ok := kept[filtered]; ok {
		t.Error("the filtered message reached the journal")
	}
}

type apiStorageObject = struct {
	Value string
}

func readJournal(t *testing.T, nk *evrTestNakamaModule, userID string) []*apiStorageObject {
	t.Helper()
	objs, err := nk.StorageRead(context.Background(), []*runtime.StorageRead{{
		Collection: RemoteLogStorageCollection,
		Key:        RemoteLogStorageJournalKey,
		UserID:     userID,
	}})
	if err != nil {
		t.Fatalf("StorageRead: %v", err)
	}
	out := make([]*apiStorageObject, 0, len(objs))
	for _, o := range objs {
		out = append(out, &apiStorageObject{Value: o.Value})
	}
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}
