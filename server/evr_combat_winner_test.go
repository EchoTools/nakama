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
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
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
	r := runCombatPostMatch(t, mode, blueKey, orangeKey, true, NewRuntimeGoLogger(zap.NewNop()))
	if r.n != 2 {
		t.Fatalf("expected a TeamSkillRatingMu entry for both players, got %d", r.n)
	}
	return r.blueMu, r.orangeMu
}

type postMatchRatings struct {
	blueMu, orangeMu float64
	n                int // TeamSkillRatingMu entries written
}

// runCombatPostMatch feeds a two-player match (blue, orange) through
// processPostMatchMessages. blueKey / orangeKey replace the sample's
// "CombatWins":1 in each player's payload. sbmm is enable_skill_based_mm.
func runCombatPostMatch(t *testing.T, mode evr.Symbol, blueKey, orangeKey string, sbmm bool, logger runtime.Logger) postMatchRatings {
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
	settings.Matchmaking.EnableSBMM = sbmm
	withServiceSettings(t, settings)

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
	return postMatchRatings{blueMu: mu[label.Players[0].UserID], orangeMu: mu[label.Players[1].UserID], n: len(mu)}
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

// With SBMM off arena writes no ratings (and neither does combat: both follow
// enable_skill_based_mm, which stays on in production).
func TestArenaPostMatch_SBMMOff_NoRatingWrites(t *testing.T) {
	r := runCombatPostMatch(t, evr.ModeArenaPublic, `"ArenaWins":1`, `"ArenaLosses":1`, false, NewRuntimeGoLogger(zap.NewNop()))
	if r.n != 0 {
		t.Errorf("arena with SBMM off wrote %d rating entries", r.n)
	}
}

// A combat match with no decided winner moves no rating. A tie reports
// neither CombatWins nor CombatLosses (here the key is renamed to CombatTies,
// which evr.MatchTypeStats does not parse on main); the same goes for a payload
// with no result key at all. It is logged at Info with the reason.
// Red before: such a match was rated as an orange win.
func TestCombatPostMatch_NoDecidedWinner_MovesNoRating(t *testing.T) {
	const sbmm = true
	{
		for _, key := range []string{`"CombatTies":1`, `"CombatMVPs":1`} {
			core, logs := observer.New(zapcore.InfoLevel)
			r := runCombatPostMatch(t, evr.ModeCombatPublic, key, key, sbmm, NewRuntimeGoLogger(zap.New(core)))
			if r.n != 0 {
				t.Errorf("sbmm=%v %s: an undecided match wrote %d rating entries (blue mu=%.4f orange mu=%.4f)", sbmm, key, r.n, r.blueMu, r.orangeMu)
			}
			recs := logs.FilterMessage("Combat match has no decided winner; ratings not updated").All()
			if len(recs) != 1 || recs[0].Level != zapcore.InfoLevel {
				t.Errorf("sbmm=%v %s: expected one Info record, got %d", sbmm, key, len(recs))
			}
		}
	}
}
