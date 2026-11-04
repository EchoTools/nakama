package server

// Combat winner (#775): a combat post-match payload carries CombatWins /
// CombatLosses, not ArenaWins / ArenaLosses. blueWins in processPostMatchMessages
// decides which team's TeamSkillRating goes up. Compiles against origin/main.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// combatWinnerPayload is a combat winner's POST_MATCH_MATCH_TYPE_STATS as the
// game sent it, identity anonymised (the recorded sample in #771). Note: no
// ArenaWins, no ArenaLosses; the win is "CombatWins":1.
const combatWinnerPayload = `{"message":"process post-match match type stats","message_type":"POST_MATCH_MATCH_TYPE_STATS","userid":"OVR-ORG-0000000000000000","[session][uuid]":"{00000000-0000-0000-0000-000000000000}","match_type":"Echo_Combat","stats":{"XP":1450,"CombatWins":1,"CombatWinPercentage":100.000000,"CombatPointCaptureGamesPlayed":1,"CombatPointCaptureWins":1,"CombatPointCaptureWinPercentage":100.000000,"CombatMVPs":1,"CombatKills":7,"CombatDeaths":4,"CombatDamage":1607.402100,"CombatDamageTaken":1248.424683,"CombatEliminations":7,"CombatObjectiveEliminations":3,"CombatObjectiveTime":253.187057,"CombatObjectiveDamage":1030.936035,"CombatAverageEliminationDeathRatio":1.750000,"CombatBestEliminationStreak":4,"CombatHealing":287.646820,"CombatStuns":2,"CombatSoloKills":7,"CombatHeadshotKills":4,"CombatHillCaptures":5,"CombatHillDefends":1}}`

type combatWinnerNK struct {
	*evrTestNakamaModule
	label *MatchLabel
}

func (n *combatWinnerNK) MatchGet(_ context.Context, id string) (*api.Match, error) {
	if id != n.label.ID.String() {
		return nil, nil
	}
	data, err := json.Marshal(n.label)
	if err != nil {
		return nil, err
	}
	return &api.Match{MatchId: id, Label: wrapperspb.String(string(data))}, nil
}

// No stored ratings: MatchmakingRatingLoad returns the default rating.
func (n *combatWinnerNK) LeaderboardRecordsList(context.Context, string, []string, int, string, int64) ([]*api.LeaderboardRecord, []*api.LeaderboardRecord, string, string, error) {
	return nil, nil, "", "", nil
}

// ratedAfterBlueWon runs a two-player public combat match through
// processPostMatchMessages with blue's payload using blueKey for its result and
// orange's using orangeKey, and returns the TeamSkillRating mu each is rewritten to.
func ratedAfterBlueWon(t *testing.T, mode evr.Symbol, blueKey, orangeKey string) (blueMu, orangeMu float64) {
	t.Helper()
	const blueXPID, orangeXPID = "OVR-ORG-1000000000000011", "OVR-ORG-1000000000000012"
	blueRaw := strings.ReplaceAll(strings.ReplaceAll(combatWinnerPayload, "OVR-ORG-0000000000000000", blueXPID), `"CombatWins":1`, blueKey)
	// No loser sample is recorded anywhere; orange's payload is the winner's with the win renamed to its result key.
	orangeRaw := strings.ReplaceAll(strings.ReplaceAll(combatWinnerPayload, "OVR-ORG-0000000000000000", orangeXPID), `"CombatWins":1`, orangeKey)
	blueMsg, err := evr.UnmarshalRemoteLog([]byte(blueRaw))
	if err != nil {
		t.Fatal(err)
	}
	orangeMsg, err := evr.UnmarshalRemoteLog([]byte(orangeRaw))
	if err != nil {
		t.Fatal(err)
	}

	blueX, _ := evr.ParseEvrId(blueXPID)
	orangeX, _ := evr.ParseEvrId(orangeXPID)
	matchID := MatchID{UUID: uuid.Must(uuid.FromString("00000000-0000-4000-8000-00000000c110")), Node: "winnernode"}
	groupID := uuid.Must(uuid.FromString("00000000-0000-4000-8000-00000000c111"))
	label := &MatchLabel{
		ID: matchID, Mode: mode, GroupID: &groupID,
		Players: []PlayerInfo{
			{UserID: "00000000-0000-4000-8000-00000000c121", SessionID: "00000000-0000-4000-8000-00000000c131", EvrID: *blueX, Team: BlueTeam, DisplayName: "synthetic-blue"},
			{UserID: "00000000-0000-4000-8000-00000000c122", SessionID: "00000000-0000-4000-8000-00000000c132", EvrID: *orangeX, Team: OrangeTeam, DisplayName: "synthetic-orange"},
		},
	}
	nk := &combatWinnerNK{evrTestNakamaModule: newEvrTestNakamaModule(), label: label}

	settings := &ServiceSettingsData{}
	FixDefaultServiceSettings(nil, settings)
	settings.Matchmaking.EnableSBMM = true
	withServiceSettings(t, settings)

	logger := NewRuntimeGoLogger(zap.NewNop())
	queue := &StatisticsQueue{logger: logger, ch: make(chan []*StatisticsQueueEntry, 64)}
	// A game-server operator (invalid XPID) may submit on behalf of players.
	s := &EventRemoteLogSet{Node: "winnernode", UserID: "00000000-0000-4000-8000-00000000c1ff", SessionID: "00000000-0000-4000-8000-00000000c1fe", XPID: evr.EvrId{}}
	if err := s.processPostMatchMessages(context.Background(), logger, nil, nk, NewLocalSessionRegistry(&testMetrics{}), queue, matchID, []evr.RemoteLog{blueMsg, orangeMsg}); err != nil {
		t.Fatalf("processPostMatchMessages: %v", err)
	}

	mu := map[string]float64{}
	for len(queue.ch) > 0 {
		for _, e := range <-queue.ch {
			if e.BoardMeta.StatName != TeamSkillRatingMuStatisticID {
				continue
			}
			v, err := ScoreToFloat64(e.Score, e.Subscore)
			if err != nil {
				t.Fatal(err)
			}
			mu[e.UserID] = v
		}
	}
	if len(mu) != 2 {
		t.Fatalf("expected a TeamSkillRatingMu entry for both players, got %v", mu)
	}
	return mu[label.Players[0].UserID], mu[label.Players[1].UserID]
}

// The winner fix. Blue won a public combat match; blue's payload says
// CombatWins, orange's says CombatLosses. Blue's rating must go up and orange's
// down from the default. Red on main: CombatWins is not a field of
// evr.MatchTypeStats there, ArenaWins/ArenaLosses stay 0 and blueWins stays
// false, so the ratings move the wrong way.
func TestCombatPostMatch_BlueWinRatesBlueUp(t *testing.T) {
	blueMu, orangeMu := ratedAfterBlueWon(t, evr.ModeCombatPublic, `"CombatWins":1`, `"CombatLosses":1`)
	def := NewDefaultRating().Mu
	if !(blueMu > def && orangeMu < def) {
		t.Errorf("blue won but blue mu=%.4f and orange mu=%.4f (default %.4f)", blueMu, orangeMu, def)
	}
}

// Control: the same harness and match with the arena keys. Green on main and
// here; it shows the harness is not inverted, and that the arena key path is
// still read.
func TestCombatPostMatch_Control_ArenaKeysStillRead(t *testing.T) {
	blueMu, orangeMu := ratedAfterBlueWon(t, evr.ModeCombatPublic, `"ArenaWins":1`, `"ArenaLosses":1`)
	def := NewDefaultRating().Mu
	if !(blueMu > def && orangeMu < def) {
		t.Errorf("blue won (arena keys) but blue mu=%.4f and orange mu=%.4f (default %.4f)", blueMu, orangeMu, def)
	}
}

// Arena: a real arena match is read from the arena keys exactly as before.
func TestArenaPostMatch_BlueWinRatesBlueUp(t *testing.T) {
	blueMu, orangeMu := ratedAfterBlueWon(t, evr.ModeArenaPublic, `"ArenaWins":1`, `"ArenaLosses":1`)
	def := NewDefaultRating().Mu
	if !(blueMu > def && orangeMu < def) {
		t.Errorf("arena blue won but blue mu=%.4f and orange mu=%.4f (default %.4f)", blueMu, orangeMu, def)
	}
}

// The combat result keys are not leaderboard stats of the remote-log path: the
// game server's profile update is the combat boards' writer.
func TestIterateMatchTypeStatsFields_SkipsCombatResultKeys(t *testing.T) {
	stats := evr.MatchTypeStats{CombatWins: 1, CombatLosses: 1, ArenaWins: 1}
	seen := map[string]bool{}
	iterateMatchTypeStatsFields(stats, func(name string, _ LeaderboardOperator, _ float64) { seen[name] = true })
	if seen["CombatWins"] || seen["CombatLosses"] {
		t.Errorf("combat result keys were written as leaderboard stats: %v", seen)
	}
	if !seen["ArenaWins"] {
		t.Errorf("ArenaWins no longer written: %v", seen)
	}
}
