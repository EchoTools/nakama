package server

// Combat team sort (#775), unit level: these use the new functions directly.

import (
	"context"
	"encoding/json"
	"math"
	"math/bits"
	"math/rand"
	"slices"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/intinig/go-openskill/rating"
	"github.com/intinig/go-openskill/types"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func asRuntime(es []*MatchmakerEntry) []runtime.MatchmakerEntry {
	out := make([]runtime.MatchmakerEntry, len(es))
	for i, e := range es {
		out[i] = e
	}
	return out
}

func randomCombatEntries(r *rand.Rand, n int) []*MatchmakerEntry {
	es := combatSolos(make([]float64, n)...)
	for _, e := range es {
		e.Properties["rating_mu"] = 5 + r.Float64()*35
		e.Properties["rating_sigma"] = 1 + r.Float64()*5
	}
	return es
}

// N1b: for two teams the draw probability depends on the split only through
// |sum(mu) - sum(mu)| (the summed sigma^2 is the same for every split), so the
// smallest |dmu| split is the highest-draw split. This is what lets the search
// run on running sums for 16 players. Intent: pin that equivalence on random
// inputs, with unequal sigmas, for n = 4, 6, 8.
func TestCombatSort_DrawIsFunctionOfMuDifference(t *testing.T) {
	r := rand.New(rand.NewSource(775))
	opts := combatOpenSkillOptions(nil)
	for _, n := range []int{4, 6, 8} {
		for trial := 0; trial < 200; trial++ {
			es := asRuntime(randomCombatEntries(r, n))
			ratings := MatchmakerEntries(es).Ratings(opts)
			bestDraw := 0.0
			for mask := 0; mask < 1<<n; mask++ {
				if bits.OnesCount(uint(mask)) != n/2 {
					continue
				}
				var blue, orange types.Team
				for i := 0; i < n; i++ {
					if mask>>i&1 == 1 {
						blue = append(blue, ratings[i])
					} else {
						orange = append(orange, ratings[i])
					}
				}
				bestDraw = math.Max(bestDraw, rating.PredictDraw([]types.Team{blue, orange}, opts))
			}
			out, d := resortCombatTeams(es, opts, 1, 1, nil)
			if got := combatDrawOfArrangement(out, opts); bestDraw-got > 1e-12 {
				t.Fatalf("n=%d trial %d: sorted draw %.12f, best over all splits %.12f", n, trial, got, bestDraw)
			}
			if math.Abs(d.DrawAfter-combatDrawOfArrangement(out, opts)) > 1e-12 {
				t.Fatalf("decision DrawAfter %.12f differs from the arrangement's %.12f", d.DrawAfter, combatDrawOfArrangement(out, opts))
			}
		}
	}
}

// N1c: 16 players, 6,435 splits, exhaustively. The result has the smallest
// possible |dmu| (found over all 2^16 masks) and the search finishes inside a
// budget far above what it needs.
func TestCombatSort_SixteenIsExhaustive(t *testing.T) {
	r := rand.New(rand.NewSource(16))
	es := randomCombatEntries(r, 16)
	start := time.Now()
	out, d := resortCombatTeams(asRuntime(es), combatOpenSkillOptions(nil), 1, 1, nil)
	elapsed := time.Since(start)
	if d.Splits != 6435 {
		t.Errorf("evaluated %d splits, want C(15,7) = 6435", d.Splits)
	}
	blue, orange := teamsOf(out)
	if got, want := math.Abs(sumMu(blue)-sumMu(orange)), bruteForceMinDelta(muList(out)); math.Abs(got-want) > 1e-9 {
		t.Errorf("teams differ by %.6f, closest split %.6f", got, want)
	}
	t.Logf("16 players: %d splits in %s", d.Splits, elapsed)
	if elapsed > 5*time.Second {
		t.Errorf("16-player sort took %s", elapsed)
	}
}

// 8 players is 35 splits.
func TestCombatSort_EightIsThirtyFiveSplits(t *testing.T) {
	r := rand.New(rand.NewSource(8))
	_, d := resortCombatTeams(asRuntime(randomCombatEntries(r, 8)), combatOpenSkillOptions(nil), 1, 1, nil)
	if d.Splits != 35 {
		t.Errorf("evaluated %d splits, want 35", d.Splits)
	}
}

// N9: the sort never modifies what it is given. The input slice keeps its
// order and the output is a different slice.
func TestCombatSort_DoesNotMutateInput(t *testing.T) {
	r := rand.New(rand.NewSource(9))
	es := asRuntime(randomCombatEntries(r, 8))
	before := slices.Clone(es)
	out, _ := resortCombatTeams(es, combatOpenSkillOptions(nil), 0.003, 1, nil)
	if !slices.Equal(es, before) {
		t.Errorf("resortCombatTeams reordered its input")
	}
	if &out[0] == &es[0] {
		t.Errorf("output shares the input's backing array")
	}

	matches := [][]runtime.MatchmakerEntry{es}
	matchesBefore := slices.Clone(matches[0])
	out2, _ := sortCombatMatches(context.Background(), nil, NewRuntimeGoLogger(zap.NewNop()), matches, nil, nil)
	if !slices.Equal(matches[0], matchesBefore) {
		t.Errorf("sortCombatMatches reordered the made match it was given")
	}
	if &out2[0][0] == &matches[0][0] {
		t.Errorf("sortCombatMatches returned the made match's own backing array")
	}
}

// Arena passes through sortCombatMatches untouched (same slices, no decisions).
func TestSortCombatMatches_LeavesArenaAlone(t *testing.T) {
	es := combatSolos(5, 8, 12, 15, 20, 24, 31, 40)
	for _, e := range es {
		e.Properties["game_mode"] = evr.ModeArenaPublic.String()
	}
	matches := [][]runtime.MatchmakerEntry{asRuntime(es)}
	out, decisions := sortCombatMatches(context.Background(), nil, NewRuntimeGoLogger(zap.NewNop()), matches, nil, nil)
	if len(decisions) != 0 {
		t.Errorf("arena match produced %d combat decisions", len(decisions))
	}
	if &out[0][0] != &matches[0][0] || !slices.Equal(out[0], matches[0]) {
		t.Errorf("arena match was touched")
	}
}

// N10: one structured record per combat sort, none for arena, carrying who was
// split and the draw probability.
func TestCombatSort_LogsOneRecordPerSort(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	logger := NewRuntimeGoLogger(zap.New(core))

	duo := combatSolos(5, 6, 7, 8, 9, 10)
	duo = append(duo,
		combatSortEntry(evr.ModeCombatPublic.String(), "t-duo", "d1", 40),
		combatSortEntry(evr.ModeCombatPublic.String(), "t-duo", "d2", 38))
	solo := combatSolos(11, 12, 13, 14, 15, 16, 17, 18)
	for i, e := range solo {
		e.Presence.SessionId = "s-x" + string(rune('a'+i))
		e.Presence.UserId = "u-x" + string(rune('a'+i))
		e.Presence.Username = "x" + string(rune('a'+i))
	}
	arena := combatSolos(11, 12, 13, 14, 15, 16, 17, 18)
	for i, e := range arena {
		e.Properties["game_mode"] = evr.ModeArenaPublic.String()
		e.Presence.SessionId = "s-y" + string(rune('a'+i))
		e.Presence.UserId = "u-y" + string(rune('a'+i))
	}

	_, decisions := sortCombatMatches(context.Background(), nil, logger,
		[][]runtime.MatchmakerEntry{asRuntime(duo), asRuntime(arena), asRuntime(solo)}, nil, nil)
	if len(decisions) != 2 {
		t.Fatalf("decisions = %d, want 2 (two combat matches, one arena)", len(decisions))
	}
	recs := logs.FilterMessage("Combat teams sorted").All()
	if len(recs) != 2 {
		t.Fatalf("log records = %d, want 2", len(recs))
	}
	for _, rec := range recs {
		if rec.Level != zapcore.InfoLevel {
			t.Errorf("level = %v, want info", rec.Level)
		}
		ctx := rec.ContextMap()
		for _, k := range []string{"user_ids", "kept_parties", "moved", "draw_before", "draw_after", "draw_best", "parties", "tolerance", "weight", "splits"} {
			if _, ok := ctx[k]; !ok {
				t.Errorf("record lacks %q: %v", k, ctx)
			}
		}
	}
	first := recs[0].ContextMap()
	if first["kept_parties"] != false || first["parties"] != int64(1) {
		t.Errorf("duo match record: kept_parties=%v parties=%v", first["kept_parties"], first["parties"])
	}
	if moved, _ := first["moved"].([]any); len(moved) != 1 {
		t.Errorf("duo match record names %v moved, want exactly one", first["moved"])
	}
	if d1, _ := first["draw_after"].(float64); d1 <= first["draw_before"].(float64) {
		t.Errorf("draw_after %v not above draw_before %v", first["draw_after"], first["draw_before"])
	}
}

// The falsifier. One duo queued against the same pool ten times, with the sort
// forced to split them every time (both are far stronger than anyone else).
// With the recent-split score fed back each round, the burden of being moved must
// not stay on one member: no member is moved twice in a row while the other has
// never been moved. Intent: the split penalty is what spreads the cost; a
// version with no memory would move the same member every round and fail here.
func TestCombatSort_PartySplitBurdenAlternates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		d2     float64
		strict bool
	}{
		{"equal mu: strict alternation", 40, true},
		{"mu 0.3 apart", 39.7, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scores := map[string]float64{}
			var moved []string
			for round := 0; round < 10; round++ {
				es := combatSolos(5, 6, 7, 8, 9, 10)
				es = append(es,
					combatSortEntry(evr.ModeCombatPublic.String(), "t-duo", "d1", 40),
					combatSortEntry(evr.ModeCombatPublic.String(), "t-duo", "d2", tc.d2))
				_, d := resortCombatTeams(asRuntime(es), combatOpenSkillOptions(nil), DefaultCombatDrawTolerance, DefaultCombatSplitPenaltyWeight, scores)
				if d.KeptParties || len(d.Moved) != 1 {
					t.Fatalf("round %d: expected the duo to be split with one member moved, got kept=%v moved=%v", round, d.KeptParties, d.Moved)
				}
				m := d.Moved[0]
				moved = append(moved, m)
				for _, u := range d.partyUserIDs {
					scores[u] = nextCombatSplitScore(scores[u], u == m)
				}
			}
			t.Logf("moved: %v", moved)
			seen := map[string]bool{}
			for i, m := range moved {
				seen[m] = true
				if i > 0 && moved[i-1] == m {
					other := "u-d1"
					if m == "u-d1" {
						other = "u-d2"
					}
					if !seen[other] {
						t.Errorf("round %d: %s moved twice in a row while %s was never moved: %v", i, m, other, moved)
					}
					if tc.strict {
						t.Errorf("round %d: %s moved twice in a row: %v", i, m, moved)
					}
				}
			}
			if len(seen) != 2 {
				t.Errorf("only %v ever moved: %v", seen, moved)
			}
		})
	}
}

// The score decays by combatSplitHistoryDecay per match and gains 1 when moved.
func TestNextCombatSplitScore(t *testing.T) {
	if got := nextCombatSplitScore(0, true); got != 1 {
		t.Errorf("moved from 0 = %v, want 1", got)
	}
	if got := nextCombatSplitScore(1, false); math.Abs(got-combatSplitHistoryDecay) > 1e-12 {
		t.Errorf("decay of 1 = %v, want %v", got, combatSplitHistoryDecay)
	}
	s := 1.0
	for i := 0; i < 40; i++ {
		s = nextCombatSplitScore(s, false)
	}
	if s != 0 {
		t.Errorf("after 40 quiet matches the score is %v, want 0 (under the floor)", s)
	}
}

// The recent-split score is kept in the player's storage next to their matchmaking
// data (collection Matchmaker, key combat_splits), written only for the matches
// that stand, and read back by the next sort.
func TestCombatSplitHistory_PersistedInMatchmakerCollection(t *testing.T) {
	nk := newOCCTestNakamaModule()
	logger := NewRuntimeGoLogger(zap.NewNop())
	ctx := context.Background()

	build := func() []runtime.MatchmakerEntry {
		es := combatSolos(5, 6, 7, 8, 9, 10)
		es = append(es,
			combatSortEntry(evr.ModeCombatPublic.String(), "t-duo", "d1", 40),
			combatSortEntry(evr.ModeCombatPublic.String(), "t-duo", "d2", 40))
		return asRuntime(es)
	}
	read := func(uid string) float64 {
		h := &CombatSplitHistory{}
		if err := StorableRead(ctx, nk, uid, h, false); err != nil {
			if isStorageNotFoundError(err) {
				return 0
			}
			t.Fatal(err)
		}
		return h.Score
	}

	if meta := (CombatSplitHistory{}).StorageMeta(); meta.Collection != MatchmakerStorageCollection || meta.Key != "combat_splits" {
		t.Fatalf("storage location %s/%s", meta.Collection, meta.Key)
	}

	// Round 1: d1 and d2 tie on score 0; the lowest session id's side is home, d2 is moved.
	_, decisions := sortCombatMatches(ctx, nk, logger, [][]runtime.MatchmakerEntry{build()}, nil, nil)
	if len(decisions) != 1 || len(decisions[0].Moved) != 1 {
		t.Fatalf("decisions = %+v", decisions)
	}
	persistCombatSplitHistory(ctx, nk, logger, decisions)
	moved1 := decisions[0].Moved[0]
	other := "u-d1"
	if moved1 == "u-d1" {
		other = "u-d2"
	}
	if got := read(moved1); got != 1 {
		t.Errorf("moved player %s stored %v, want 1", moved1, got)
	}
	if got := read(other); got != 0 {
		t.Errorf("player not moved stored %v, want 0", got)
	}
	if got := read("u-p00"); got != 0 {
		t.Errorf("a solo player has a record: %v", got)
	}

	// Round 2 reads round 1: the other member is moved, the first decays.
	_, decisions = sortCombatMatches(ctx, nk, logger, [][]runtime.MatchmakerEntry{build()}, nil, nil)
	if len(decisions[0].Moved) != 1 || decisions[0].Moved[0] != other {
		t.Fatalf("round 2 moved %v, want [%s]", decisions[0].Moved, other)
	}
	persistCombatSplitHistory(ctx, nk, logger, decisions)
	if got := read(moved1); math.Abs(got-combatSplitHistoryDecay) > 1e-9 {
		t.Errorf("round 1's moved player decayed to %v, want %v", got, combatSplitHistoryDecay)
	}
	if got := read(other); got != 1 {
		t.Errorf("round 2's moved player stored %v, want 1", got)
	}

	// A decision whose match was dropped (backfill-first) is not recorded.
	if kept := combatDecisionsStillMade(decisions, nil); len(kept) != 0 {
		t.Errorf("decisions kept for a dropped match: %v", kept)
	}
}

// Parties kept together write nothing: no stored record is created for a sort that moved no one.
func TestCombatSplitHistory_NoWriteWhenNothingChanges(t *testing.T) {
	nk := newOCCTestNakamaModule()
	logger := NewRuntimeGoLogger(zap.NewNop())
	es := combatSolos(10, 10.5, 9.5, 10.2, 9.8, 10.1)
	es = append(es,
		combatSortEntry(evr.ModeCombatPublic.String(), "t-duo", "d1", 10.4),
		combatSortEntry(evr.ModeCombatPublic.String(), "t-duo", "d2", 9.6))
	_, decisions := sortCombatMatches(context.Background(), nk, logger, [][]runtime.MatchmakerEntry{asRuntime(es)}, nil, nil)
	if len(decisions) != 1 || !decisions[0].KeptParties {
		t.Fatalf("expected the duo kept together: %+v", decisions)
	}
	persistCombatSplitHistory(context.Background(), nk, logger, decisions)
	h := &CombatSplitHistory{}
	if err := StorableRead(context.Background(), nk, "u-d1", h, false); !isStorageNotFoundError(err) {
		t.Errorf("a record was written for a kept party: err=%v score=%v", err, h.Score)
	}
}

// N8: reservation sets are the ones assembly chose. With ticket reservation on
// and one starving player in a pool of 16, the players in each made match are the
// players of the prediction assembleMatchesWithReservations picked from the same
// predictions: the sort only moves them between teams.
func TestCombatSort_ReservationSetsUnchanged(t *testing.T) {
	settings := &ServiceSettingsData{}
	FixDefaultServiceSettings(nil, settings)
	settings.Matchmaking.EnableTicketReservation = true
	withServiceSettings(t, settings)

	es := combatSolos(5, 8, 12, 15, 20, 24, 31, 40, 6, 9, 13, 16, 21, 25, 30, 39)
	es[3].Properties["timestamp"] = float64(time.Now().UTC().Unix() - 200) // starving
	m := NewSkillBasedMatchmaker()
	logger := NewRuntimeGoLogger(zap.NewNop())
	candidates, matches, _, predictions, _ := m.processPotentialMatchesWith(context.Background(), nil, logger, asRuntime(es))
	if len(matches) == 0 {
		t.Fatal("no matches made")
	}
	starving, reserved := m.buildReservations(logger, candidates, predictions, &settings.Matchmaking)
	want := m.assembleMatchesWithReservations(logger, predictions, starving, reserved)

	key := func(ms [][]runtime.MatchmakerEntry) []string {
		var out []string
		for _, mm := range ms {
			ids := make([]string, 0, len(mm))
			for _, e := range mm {
				ids = append(ids, e.GetPresence().GetSessionId())
			}
			slices.Sort(ids)
			out = append(out, join(ids))
		}
		slices.Sort(out)
		return out
	}
	if g, w := key(matches), key(want); !slices.Equal(g, w) {
		t.Errorf("made matches differ from assembly's:\n got  %v\n want %v", g, w)
	}
}

func join(ss []string) string {
	b, _ := json.Marshal(ss)
	return string(b)
}

// Settings (evr_global_settings.go conventions): two float64 settings, JSON keys
// as documented, zero means the default (as PartySkillBoostPercent), a set value is kept.
func TestCombatSortSettings_Defaults(t *testing.T) {
	var s ServiceSettingsData
	FixDefaultServiceSettings(nil, &s)
	if s.Matchmaking.CombatDrawTolerance != DefaultCombatDrawTolerance {
		t.Errorf("CombatDrawTolerance = %v, want %v", s.Matchmaking.CombatDrawTolerance, DefaultCombatDrawTolerance)
	}
	if s.Matchmaking.CombatSplitPenaltyWeight != DefaultCombatSplitPenaltyWeight {
		t.Errorf("CombatSplitPenaltyWeight = %v, want %v", s.Matchmaking.CombatSplitPenaltyWeight, DefaultCombatSplitPenaltyWeight)
	}

	set := ServiceSettingsData{Matchmaking: GlobalMatchmakingSettings{CombatDrawTolerance: 0.01, CombatSplitPenaltyWeight: 2.5}}
	FixDefaultServiceSettings(nil, &set)
	if set.Matchmaking.CombatDrawTolerance != 0.01 || set.Matchmaking.CombatSplitPenaltyWeight != 2.5 {
		t.Errorf("configured values replaced: %v %v", set.Matchmaking.CombatDrawTolerance, set.Matchmaking.CombatSplitPenaltyWeight)
	}

	raw, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	var round map[string]json.RawMessage
	if err := json.Unmarshal(raw, &round); err != nil {
		t.Fatal(err)
	}
	var mm map[string]any
	if err := json.Unmarshal(round["matchmaking"], &mm); err != nil {
		t.Fatal(err)
	}
	if mm["combat_draw_tolerance"] != 0.01 || mm["combat_split_penalty_weight"] != 2.5 {
		t.Errorf("wire keys: %v %v", mm["combat_draw_tolerance"], mm["combat_split_penalty_weight"])
	}
}

// The setting is read: a tolerance big enough to accept any party-keeping split
// keeps the duo together where the default splits it.
func TestCombatSortSettings_ToleranceIsRead(t *testing.T) {
	build := func() []runtime.MatchmakerEntry {
		es := combatSolos(5, 6, 7, 8, 9, 10)
		es = append(es,
			combatSortEntry(evr.ModeCombatPublic.String(), "t-duo", "d1", 40),
			combatSortEntry(evr.ModeCombatPublic.String(), "t-duo", "d2", 38))
		return asRuntime(es)
	}
	logger := NewRuntimeGoLogger(zap.NewNop())
	_, def := sortCombatMatches(context.Background(), nil, logger, [][]runtime.MatchmakerEntry{build()}, nil, nil)
	if def[0].KeptParties {
		t.Fatalf("default tolerance kept the stacked duo together")
	}
	_, wide := sortCombatMatches(context.Background(), nil, logger, [][]runtime.MatchmakerEntry{build()}, nil, &GlobalMatchmakingSettings{CombatDrawTolerance: 1})
	if !wide[0].KeptParties {
		t.Errorf("tolerance 1 still split the duo")
	}
}

// N4: combat keeps the sorted teams; arena still reunites a split party.
func TestRepairTeamsForMode(t *testing.T) {
	mk := func(ticket, name string) *MatchmakerEntry {
		return &MatchmakerEntry{Ticket: ticket, Presence: &MatchmakerPresence{UserId: "u-" + name, SessionId: "s-" + name, Username: name}, PartyId: ""}
	}
	build := func() [2][]*MatchmakerEntry {
		return [2][]*MatchmakerEntry{
			{mk("P", "a1"), mk("s1", "s1"), mk("s2", "s2"), mk("s3", "s3")},
			{mk("P", "a2"), mk("s4", "s4"), mk("s5", "s5"), mk("s6", "s6")},
		}
	}
	teamOf := func(teams [2][]*MatchmakerEntry, name string) int {
		for ti, team := range teams {
			for _, e := range team {
				if e.Presence.Username == name {
					return ti
				}
			}
		}
		return -1
	}
	logger := zap.NewNop()

	combat, err := repairTeamsForMode(logger, evr.ModeCombatPublic, build())
	if err != nil {
		t.Fatal(err)
	}
	if teamOf(combat, "a1") == teamOf(combat, "a2") {
		t.Errorf("combat: split party was reunited")
	}

	arena, err := repairTeamsForMode(logger, evr.ModeArenaPublic, build())
	if err != nil {
		t.Fatal(err)
	}
	if teamOf(arena, "a1") != teamOf(arena, "a2") {
		t.Errorf("arena twin: split party was not reunited")
	}
	ref, _ := repairSplitTicketTeams(logger, build())
	for ti := range ref {
		for i := range ref[ti] {
			if arena[ti][i].Presence.Username != ref[ti][i].Presence.Username {
				t.Errorf("arena team differs from repairSplitTicketTeams at %d/%d", ti, i)
			}
		}
	}
}

// The unit for the winner: combat keys first, arena keys as the fallback, and
// nothing when the player says nothing.
func TestCombatBlueWins(t *testing.T) {
	cases := []struct {
		name         string
		team         TeamIndex
		stats        evr.MatchTypeStats
		decided, blu bool
	}{
		{"blue CombatWins", BlueTeam, evr.MatchTypeStats{CombatWins: 1}, true, true},
		{"orange CombatWins", OrangeTeam, evr.MatchTypeStats{CombatWins: 1}, true, false},
		{"blue CombatLosses", BlueTeam, evr.MatchTypeStats{CombatLosses: 1}, true, false},
		{"orange CombatLosses", OrangeTeam, evr.MatchTypeStats{CombatLosses: 1}, true, true},
		{"blue ArenaWins fallback", BlueTeam, evr.MatchTypeStats{ArenaWins: 1}, true, true},
		{"orange ArenaLosses fallback", OrangeTeam, evr.MatchTypeStats{ArenaLosses: 1}, true, true},
		{"neither", BlueTeam, evr.MatchTypeStats{XP: 100}, false, false},
	}
	for _, c := range cases {
		decided, blue := combatBlueWins(c.team, c.stats)
		if decided != c.decided || blue != c.blu {
			t.Errorf("%s: got (%v,%v), want (%v,%v)", c.name, decided, blue, c.decided, c.blu)
		}
	}
}
