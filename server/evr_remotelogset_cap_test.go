package server

// Remote-log entry cap: a post-match block is 9 entries per player (5 types
// batched across all players, then a quad per player). With a 50-entry cap an
// 8-player match (72 entries) lost POST_MATCH_MATCH_TYPE_STATS for most of its
// players, and those players got no rating, match record or leaderboard write.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// postMatchSet builds the entries one game server sends after a combat match
// with n players (n/2 blue, n/2 orange; blue won), in send order: five batched
// types for every player, then per player the type-stats quad.
func postMatchSet(n int) (set []string, xpids []string) {
	batched := []string{
		"POST_MATCH_BATTLE_PASS_STATS", "POST_MATCH_BATTLE_PASS_UNLOCKS", "POST_MATCH_BATTLE_PASS_XP",
		"POST_MATCH_EARNED_AWARD", "POST_MATCH_MATCH_STATS",
	}
	quad := []string{"POST_MATCH_MATCH_TYPE_UNLOCKS", "POST_MATCH_MATCH_TYPE_XP", "POST_MATCH_MATCH_TYPE_XP_LEVEL"}
	for i := 0; i < n; i++ {
		xpids = append(xpids, fmt.Sprintf("OVR-ORG-20000000000000%02d", i+1))
	}
	filler := func(typ, xpid string) string {
		return fmt.Sprintf(`{"message":"synthetic","message_type":%q,"userid":%q}`, typ, xpid)
	}
	for _, typ := range batched {
		for _, x := range xpids {
			set = append(set, filler(typ, x))
		}
	}
	for i, x := range xpids {
		result := `"CombatWins":1`
		if i >= n/2 {
			result = `"CombatLosses":1`
		}
		stats := strings.ReplaceAll(strings.ReplaceAll(combatWinnerPayload, "OVR-ORG-0000000000000000", x), `"CombatWins":1`, result)
		set = append(set, stats)
		for _, typ := range quad {
			set = append(set, filler(typ, x))
		}
	}
	return set, xpids
}

// ratedPlayers sends a capped, filtered post-match set through the same steps
// the server runs (cap, filter, unmarshal, processPostMatchMessages) and
// returns the number of players that got a TeamSkillRatingMu write.
func ratedPlayers(t *testing.T, logger *zap.Logger, set, xpids []string) int {
	t.Helper()
	n := len(xpids)

	label := &MatchLabel{
		ID:      MatchID{UUID: uuid.Must(uuid.FromString("00000000-0000-4000-8000-00000000c210")), Node: "capnode"},
		Mode:    evr.ModeCombatPublic,
		GroupID: func() *uuid.UUID { g := uuid.Must(uuid.FromString("00000000-0000-4000-8000-00000000c211")); return &g }(),
	}
	for i, x := range xpids {
		id, err := evr.ParseEvrId(x)
		if err != nil {
			t.Fatal(err)
		}
		team := BlueTeam
		if i >= n/2 {
			team = OrangeTeam
		}
		label.Players = append(label.Players, PlayerInfo{
			UserID:      fmt.Sprintf("00000000-0000-4000-8000-0000000c22%02d", i),
			SessionID:   fmt.Sprintf("00000000-0000-4000-8000-0000000c23%02d", i),
			EvrID:       *id,
			Team:        team,
			DisplayName: fmt.Sprintf("synthetic-%d", i),
		})
	}
	nk := &combatWinnerNK{evrTestNakamaModule: newEvrTestNakamaModule(), label: label}

	settings := &ServiceSettingsData{}
	FixDefaultServiceSettings(nil, settings)
	settings.Matchmaking.EnableSBMM = true
	withServiceSettings(t, settings)

	s := &EventRemoteLogSet{Node: "capnode", UserID: "00000000-0000-4000-8000-00000000c2ff", SessionID: "00000000-0000-4000-8000-00000000c2fe", matchStatsStore: &capturePlayerMatchStatsStore{}}
	rtLogger := NewRuntimeGoLogger(zap.NewNop())
	msgs, err := s.unmarshalLogs(rtLogger, filterRemoteLogs(applyRemoteLogCaps(logger, s.SessionID, set)))
	if err != nil {
		t.Fatal(err)
	}

	queue := &StatisticsQueue{logger: rtLogger, ch: make(chan []*StatisticsQueueEntry, 64)}
	if err := s.processPostMatchMessages(context.Background(), rtLogger, nil, nk, NewLocalSessionRegistry(&testMetrics{}), queue, label.ID, msgs); err != nil {
		t.Fatalf("processPostMatchMessages: %v", err)
	}

	rated := map[string]bool{}
	for len(queue.ch) > 0 {
		for _, e := range <-queue.ch {
			if e.BoardMeta.StatName == TeamSkillRatingMuStatisticID {
				rated[e.UserID] = true
			}
		}
	}
	return len(rated)
}

// Every player in a full post-match set is rated. Red on main: the first 50 of
// 72 (54) entries survive, which holds the type stats of 3 of 8 (3 of 6).
func TestRemoteLogCap_PostMatchSetReachesEveryPlayer(t *testing.T) {
	for _, n := range []int{6, 8} {
		t.Run(fmt.Sprintf("%d_players", n), func(t *testing.T) {
			set, xpids := postMatchSet(n)
			if len(set) != n*9 {
				t.Fatalf("built %d entries, want %d", len(set), n*9)
			}
			if got := ratedPlayers(t, zap.NewNop(), set, xpids); got != n {
				t.Errorf("%d entries, %d players: %d got a rating write", len(set), n, got)
			}
		})
	}
}

// Nothing is dropped, so nothing is logged.
func TestRemoteLogCap_NoDropNoWarn(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	set, _ := postMatchSet(8)
	if got := applyRemoteLogCaps(zap.New(core), "sid", set); len(got) != len(set) {
		t.Fatalf("kept %d of %d", len(got), len(set))
	}
	if logs.Len() != 0 {
		t.Errorf("logged %d lines for an untruncated set: %v", logs.Len(), logs.All())
	}
}

// The byte cap still bounds a set, and when it fires it says so once, with the
// session and the counts.
func TestRemoteLogCap_ByteCapWarnsOnce(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	entry := strings.Repeat("x", 1024)
	set := make([]string, 300) // 300 KB > 256 KB
	for i := range set {
		set[i] = entry
	}
	kept := applyRemoteLogCaps(zap.New(core), "sid-1", set)
	total := 0
	for _, l := range kept {
		total += len(l)
	}
	if total > maxRemoteLogPayload || len(kept) == len(set) {
		t.Fatalf("byte cap did not bound the set: kept %d entries, %d bytes", len(kept), total)
	}
	warns := logs.FilterLevelExact(zapcore.WarnLevel).All()
	if len(warns) != 1 {
		t.Fatalf("want exactly one Warn, got %d: %v", len(warns), logs.All())
	}
	f := warns[0].ContextMap()
	if f["sid"] != "sid-1" || f["kept"] != int64(len(kept)) || f["dropped"] != int64(len(set)-len(kept)) {
		t.Errorf("Warn fields = %v, want sid-1, kept=%d, dropped=%d", f, len(kept), len(set)-len(kept))
	}
}
