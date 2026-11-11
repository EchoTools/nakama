package server

import (
	"context"
	"sync"
	"testing"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.mongodb.org/mongo-driver/bson"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type storedPlayerMatchStats struct {
	matchID string
	rec     PlayerMatchStats
}

// capturePlayerMatchStatsStore records what would be written to the match's
// record, synchronously.
type capturePlayerMatchStatsStore struct {
	mu     sync.Mutex
	stored []storedPlayerMatchStats
}

func (c *capturePlayerMatchStatsStore) Store(_ runtime.Logger, matchID string, rec PlayerMatchStats) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stored = append(c.stored, storedPlayerMatchStats{matchID: matchID, rec: rec})
}

// runCombatMatchStats sends one POST_MATCH_MATCH_STATS from sender through
// processPostMatchMessages on a match of the given mode, and returns what was
// kept and what was logged.
func runCombatMatchStats(t *testing.T, mode evr.Symbol, sender string) ([]storedPlayerMatchStats, *observer.ObservedLogs, *combatStatsHarness) {
	t.Helper()
	h := newCombatStatsHarness(t)
	h.nk.label.Mode = mode

	core, logs := observer.New(zap.DebugLevel)
	h.logger = NewRuntimeGoLogger(zap.New(core))

	senderXPID, err := evr.ParseEvrId(sender)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := evr.UnmarshalRemoteLog(readServerRemoteLogFixture(t, "post_match_match_stats_combat.json"))
	if err != nil {
		t.Fatalf("UnmarshalRemoteLog: %v", err)
	}

	store := &capturePlayerMatchStatsStore{}
	s := &EventRemoteLogSet{Node: combatTestNode, UserID: combatTestUserID, SessionID: combatTestSessionID, XPID: *senderXPID, matchStatsStore: store}
	if err := s.processPostMatchMessages(context.Background(), h.logger, nil, h.nk, nil, h.queue, h.matchID, []evr.RemoteLog{msg}); err != nil {
		t.Fatalf("processPostMatchMessages: %v", err)
	}
	return store.stored, logs, h
}

// POST_MATCH_MATCH_STATS was discarded outright (`continue`) before it was
// even looked at. A combat match's stats now land on the match's own record,
// private matches included -- the fixture is from a private match, and the
// leaderboard work that follows is public-only, so the record is written
// before that gate.
func TestCombatMatchStats_KeptOnTheMatchRecord(t *testing.T) {
	sent := fixtureObject(t, readServerRemoteLogFixture(t, "post_match_match_stats_combat.json"), "match_stats")

	for _, mode := range []evr.Symbol{evr.ModeCombatPrivate, evr.ModeCombatPublic} {
		t.Run(mode.String(), func(t *testing.T) {
			stored, logs, h := runCombatMatchStats(t, mode, combatTestXPID)

			if len(stored) != 1 {
				t.Fatalf("stored %d records, want 1", len(stored))
			}
			got := stored[0]
			if got.matchID != h.matchID.String() {
				t.Errorf("match record %q, want %q", got.matchID, h.matchID.String())
			}
			if got.rec.XPID != combatTestXPID || got.rec.UserID != combatTestUserID || got.rec.SenderUserID != combatTestUserID {
				t.Errorf("record identifies xpid=%q user=%q sender=%q", got.rec.XPID, got.rec.UserID, got.rec.SenderUserID)
			}
			if got.rec.MatchType != "Echo_Combat_Private" {
				t.Errorf("match_type %q, want the fixture's Echo_Combat_Private", got.rec.MatchType)
			}
			if got.rec.ReceivedAt.IsZero() {
				t.Error("received_at not set")
			}
			for k, v := range sent {
				if got.rec.Stats[k] != v {
					t.Errorf("%s: kept %v, the game sent %v", k, got.rec.Stats[k], v)
				}
			}
			for k := range got.rec.Stats {
				if _, ok := sent[k]; !ok {
					t.Errorf("%s: kept a key the game did not send", k)
				}
			}

			entries := logs.FilterMessage("Combat match stats received").All()
			if len(entries) != 1 {
				t.Fatalf("%d \"Combat match stats received\" records, want 1", len(entries))
			}
			fields := entries[0].ContextMap()
			if entries[0].Level != zap.InfoLevel || fields["mid"] != h.matchID.String() || fields["player_xpid"] != combatTestXPID || fields["sender_uid"] != combatTestUserID {
				t.Errorf("log record = %v %v", entries[0].Level, fields)
			}
		})
	}
}

// The same gate as POST_MATCH_MATCH_TYPE_STATS: a client may report only its
// own stats; only a game server (no valid XPID) may report for others.
func TestCombatMatchStats_RejectsAnotherPlayersStats(t *testing.T) {
	stored, logs, _ := runCombatMatchStats(t, evr.ModeCombatPublic, "OVR-ORG-1000000000000002")
	if len(stored) != 0 {
		t.Fatalf("kept %d records from a client reporting someone else's stats", len(stored))
	}
	if n := logs.FilterMessage("Rejected stats submission for mismatched XPID").Len(); n != 1 {
		t.Fatalf("%d rejection records, want 1", n)
	}
}

// Scope: combat only. Arena match stats are still not kept.
func TestCombatMatchStats_ArenaMatchStatsNotKept(t *testing.T) {
	stored, _, _ := runCombatMatchStats(t, evr.ModeArenaPublic, combatTestXPID)
	if len(stored) != 0 {
		t.Fatalf("kept %d records for an arena match", len(stored))
	}
}

// The write sets one player's entry by path, so eight players reporting the
// same match do not overwrite each other, and the stats sub-document carries
// the game's key names (a struct here would be lowercased by the BSON codec).
func TestPlayerMatchStatsUpdate_SetsOnlyThatPlayer(t *testing.T) {
	rec := PlayerMatchStats{XPID: combatTestXPID, Stats: map[string]float64{"CombatHealingReceived": 50}}
	update := playerMatchStatsUpdate(rec)

	set, ok := update["$set"].(bson.M)
	if !ok || len(update) != 1 || len(set) != 1 {
		t.Fatalf("update = %v, want a single $set of one path", update)
	}
	if _, ok := set["player_stats."+combatTestXPID]; !ok {
		t.Fatalf("update sets %v, want player_stats.%s", set, combatTestXPID)
	}

	raw, err := bson.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bson.Raw(raw).LookupErr("stats", "CombatHealingReceived"); err != nil {
		t.Fatalf("stats.CombatHealingReceived not in the stored document: %v", err)
	}
}

// EventMatchSummary $sets the whole MatchSummaryState onto the same document.
// It must not carry a player_stats key, or it would clear what the players
// reported before it.
func TestMatchSummaryState_DoesNotClearPlayerStats(t *testing.T) {
	raw, err := bson.Marshal(MatchSummaryState{MatchID: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bson.Raw(raw).LookupErr("player_stats"); err == nil {
		t.Fatal("a summary without player stats still writes player_stats")
	}
}
