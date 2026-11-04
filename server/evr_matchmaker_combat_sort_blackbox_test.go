package server

// Combat team sort (#775), black box: every test here goes through the real
// EvrMatchmakerFn and uses only symbols that exist on origin/main, so the file
// compiles against main and the tests that are red there are red for the
// behaviour, not for a missing symbol.

import (
	"context"
	"fmt"
	"math"
	"math/bits"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/intinig/go-openskill/rating"
	"github.com/intinig/go-openskill/types"
	"go.uber.org/zap"
)

// combatSortEntry builds one matchmaker entry for the EvrMatchmakerFn path.
// ticket groups entries into a party; mode is the game_mode property. A
// negative mu leaves the rating properties off the ticket (SBMM off).
func combatSortEntry(mode, ticket, name string, mu float64) *MatchmakerEntry {
	props := map[string]any{
		"game_mode":        mode,
		"group_id":         "00000000-0000-4000-8000-0000000c0de1",
		"max_team_size":    4.0,
		"min_team_size":    4.0,
		"count_multiple":   2.0,
		"failsafe_timeout": 300.0,
		"timestamp":        float64(time.Now().UTC().Unix() - 10),
		"max_rtt":          250.0,
		"rtt_test":         40.0,
	}
	if mu >= 0 {
		props["rating_mu"] = mu
		props["rating_sigma"] = 10.0 / 3.0
	}
	return &MatchmakerEntry{
		Ticket:     ticket,
		Presence:   &MatchmakerPresence{UserId: "u-" + name, SessionId: "s-" + name, Username: name, Node: "n"},
		Properties: props,
	}
}

func combatSolos(mus ...float64) []*MatchmakerEntry {
	var out []*MatchmakerEntry
	for i, mu := range mus {
		name := fmt.Sprintf("p%02d", i)
		out = append(out, combatSortEntry(evr.ModeCombatPublic.String(), "t-"+name, name, mu))
	}
	return out
}

// runEvrMatchmaker runs the real EvrMatchmakerFn over entries.
func runEvrMatchmaker(entries []*MatchmakerEntry) [][]runtime.MatchmakerEntry {
	in := make([]runtime.MatchmakerEntry, len(entries))
	for i, e := range entries {
		in[i] = e
	}
	m := NewSkillBasedMatchmaker()
	return m.EvrMatchmakerFn(context.Background(), NewRuntimeGoLogger(zap.NewNop()), nil, nil, in)
}

func memberOrder(matches [][]runtime.MatchmakerEntry) string {
	var parts []string
	for _, m := range matches {
		var ids []string
		for _, e := range m {
			ids = append(ids, e.GetPresence().GetUsername())
		}
		parts = append(parts, strings.Join(ids, ","))
	}
	return strings.Join(parts, " | ")
}

func entryMuOf(e runtime.MatchmakerEntry) float64 {
	mu, _ := e.GetProperties()["rating_mu"].(float64)
	return mu
}

func teamsOf(match []runtime.MatchmakerEntry) (blue, orange []runtime.MatchmakerEntry) {
	return match[:len(match)/2], match[len(match)/2:]
}

func sumMu(es []runtime.MatchmakerEntry) float64 {
	var s float64
	for _, e := range es {
		s += entryMuOf(e)
	}
	return s
}

// bruteForceMinDelta is the smallest |sum(blue mu) - sum(orange mu)| over every
// split of mus into two equal teams, found without any shortcut: all 2^n masks.
func bruteForceMinDelta(mus []float64) float64 {
	n := len(mus)
	var total float64
	for _, m := range mus {
		total += m
	}
	best := math.Inf(1)
	for mask := 0; mask < 1<<n; mask++ {
		if bits.OnesCount(uint(mask)) != n/2 {
			continue
		}
		var blue float64
		for i := 0; i < n; i++ {
			if mask>>i&1 == 1 {
				blue += mus[i]
			}
		}
		best = math.Min(best, math.Abs(2*blue-total))
	}
	return best
}

func muList(es []runtime.MatchmakerEntry) []float64 {
	out := make([]float64, len(es))
	for i, e := range es {
		out[i] = entryMuOf(e)
	}
	return out
}

// combatTestDraw is rating.PredictDraw of a made match: first half blue, second half orange.
func combatTestDraw(match []runtime.MatchmakerEntry) float64 {
	z, mu0, sg := 3, 10.0, 10.0/3.0
	opts := &types.OpenSkillOptions{Z: &z, Mu: &mu0, Sigma: &sg}
	r := MatchmakerEntries(match).Ratings(opts)
	n := len(match)
	return rating.PredictDraw([]types.Team{r[:n/2], r[n/2:]}, opts)
}

func withServiceSettings(t *testing.T, s *ServiceSettingsData) {
	t.Helper()
	ServiceSettingsUpdate(s)
	t.Cleanup(func() { ServiceSettingsUpdate(nil) })
}

// N0: arena is untouched. A fixed arena pool with two duos and four solos goes
// through the real EvrMatchmakerFn; the exact member order of the match (blue
// half, then orange half) is pinned. Written and run green on origin/main before
// any combat change. Intent: no combat change may reorder an arena match.
func TestArenaEndToEnd_OrderUnchanged(t *testing.T) {
	const want = "a1,a2,c4,c1,b1,b2,c2,c3"
	for i := 0; i < 20; i++ {
		entries := []*MatchmakerEntry{
			combatSortEntry(evr.ModeArenaPublic.String(), "t-duo1", "a1", 31),
			combatSortEntry(evr.ModeArenaPublic.String(), "t-duo1", "a2", 27),
			combatSortEntry(evr.ModeArenaPublic.String(), "t-duo2", "b1", 12),
			combatSortEntry(evr.ModeArenaPublic.String(), "t-duo2", "b2", 9),
			combatSortEntry(evr.ModeArenaPublic.String(), "t-s1", "c1", 22),
			combatSortEntry(evr.ModeArenaPublic.String(), "t-s2", "c2", 17),
			combatSortEntry(evr.ModeArenaPublic.String(), "t-s3", "c3", 14),
			combatSortEntry(evr.ModeArenaPublic.String(), "t-s4", "c4", 25),
		}
		got := memberOrder(runEvrMatchmaker(entries))
		if got != want {
			t.Fatalf("run %d: arena member order changed:\n got  %s\n want %s", i, got, want)
		}
	}
}

// N1: the combat match is on the draw-closest teams. Eight solos with a wide
// spread; the made match's teams must have the smallest possible |sum(mu)
// difference| (found by trying all 2^8 masks) and a draw probability no other
// split beats. Red on main: the match comes out strongest-four-versus-rest.
func TestCombatSort_ChoosesDrawClosestSplit(t *testing.T) {
	mus := []float64{5, 8, 12, 15, 20, 24, 31, 40}
	matches := runEvrMatchmaker(combatSolos(mus...))
	if len(matches) != 1 || len(matches[0]) != 8 {
		t.Fatalf("expected one 8-player match, got %s", memberOrder(matches))
	}
	blue, orange := teamsOf(matches[0])
	got := math.Abs(sumMu(blue) - sumMu(orange))
	want := bruteForceMinDelta(mus)
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("teams differ by %.3f mu, the closest split differs by %.3f (blue %v, orange %v)", got, want, muList(blue), muList(orange))
	}

	// Independently of mu arithmetic: no split has a higher draw probability.
	best := 0.0
	all := matches[0]
	for mask := 0; mask < 1<<8; mask++ {
		if bits.OnesCount(uint(mask)) != 4 {
			continue
		}
		var arr []runtime.MatchmakerEntry
		for i := 0; i < 8; i++ {
			if mask>>i&1 == 1 {
				arr = append(arr, all[i])
			}
		}
		for i := 0; i < 8; i++ {
			if mask>>i&1 == 0 {
				arr = append(arr, all[i])
			}
		}
		best = math.Max(best, combatTestDraw(arr))
	}
	if d := combatTestDraw(matches[0]); best-d > 1e-12 {
		t.Errorf("draw probability %.6f, but another split reaches %.6f", d, best)
	}
}

// N2: the same eight players, four to a side. Intent: the sort moves players
// between teams, it never adds, drops or duplicates one.
func TestCombatSort_SamePlayersSameSize(t *testing.T) {
	entries := combatSolos(5, 8, 12, 15, 20, 24, 31, 40)
	matches := runEvrMatchmaker(entries)
	if len(matches) != 1 {
		t.Fatalf("expected one match, got %s", memberOrder(matches))
	}
	var want, got []string
	for _, e := range entries {
		want = append(want, e.Presence.SessionId)
	}
	for _, e := range matches[0] {
		got = append(got, e.GetPresence().GetSessionId())
	}
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(want, got) {
		t.Errorf("players changed: want %v got %v", want, got)
	}
	blue, orange := teamsOf(matches[0])
	if len(blue) != 4 || len(orange) != 4 {
		t.Errorf("teams are %d and %d, want 4 and 4", len(blue), len(orange))
	}
}

func sameTeam(match []runtime.MatchmakerEntry, a, b string) bool {
	idx := func(name string) int {
		for i, e := range match {
			if e.GetPresence().GetUsername() == name {
				return i
			}
		}
		return -1
	}
	half := len(match) / 2
	return (idx(a) < half) == (idx(b) < half)
}

// N3: a duo may be split. The duo are the two strongest players by far; keeping
// them together can only be done by stacking one team, which is outside any
// sane draw tolerance. Intent: no party preference in combat. Red on main: the
// match is strongest-four-versus-rest, which puts both strong players on blue.
func TestCombatSort_PartyMembersMaySplit(t *testing.T) {
	entries := combatSolos(5, 6, 7, 8, 9, 10)
	entries = append(entries,
		combatSortEntry(evr.ModeCombatPublic.String(), "t-duo", "d1", 40),
		combatSortEntry(evr.ModeCombatPublic.String(), "t-duo", "d2", 38),
	)
	matches := runEvrMatchmaker(entries)
	if len(matches) != 1 {
		t.Fatalf("expected one match, got %s", memberOrder(matches))
	}
	if sameTeam(matches[0], "d1", "d2") {
		t.Errorf("duo kept together although a stacked team is far from a draw: %s", memberOrder(matches))
	}
	blue, orange := teamsOf(matches[0])
	if d := math.Abs(sumMu(blue) - sumMu(orange)); d > 4 {
		t.Errorf("teams differ by %.1f mu after splitting the duo", d)
	}
}

// N3b: a duo stays together when that costs next to nothing. Everyone is within
// a point of everyone else, so a split that keeps the duo together is within the
// draw tolerance of the closest split. Intent: the sort does not split parties
// it has no reason to split. A guard: it may already be green on main.
func TestCombatSort_KeepsPartiesTogetherWithinTolerance(t *testing.T) {
	entries := combatSolos(10, 10.5, 9.5, 10.2, 9.8, 10.1)
	entries = append(entries,
		combatSortEntry(evr.ModeCombatPublic.String(), "t-duo", "d1", 10.4),
		combatSortEntry(evr.ModeCombatPublic.String(), "t-duo", "d2", 9.6),
	)
	matches := runEvrMatchmaker(entries)
	if len(matches) != 1 {
		t.Fatalf("expected one match, got %s", memberOrder(matches))
	}
	if !sameTeam(matches[0], "d1", "d2") {
		t.Errorf("duo split although together is within tolerance: %s", memberOrder(matches))
	}
	blue, orange := teamsOf(matches[0])
	if d := math.Abs(sumMu(blue) - sumMu(orange)); d > 1.5 {
		t.Errorf("teams differ by %.2f mu", d)
	}
}

// N5: combat has no quality gate. With the quality floor on at a level that
// would drop an arena candidate, the combat match still forms, and it is sorted.
// Intent: the sort is not a gate and the #479 exemption stands.
func TestCombatSort_NoQualityGate(t *testing.T) {
	withServiceSettings(t, &ServiceSettingsData{Matchmaking: GlobalMatchmakingSettings{
		EnableQualityFloor:         true,
		QualityFloorInitial:        0.99,
		QualityFloorDecayPerSecond: 0.0001,
	}})
	mus := []float64{5, 8, 12, 15, 20, 24, 31, 40}
	matches := runEvrMatchmaker(combatSolos(mus...))
	if len(matches) != 1 {
		t.Fatalf("combat match dropped by the quality floor: %v", matches)
	}
	blue, orange := teamsOf(matches[0])
	if got, want := math.Abs(sumMu(blue)-sumMu(orange)), bruteForceMinDelta(mus); math.Abs(got-want) > 1e-9 {
		t.Errorf("not sorted: teams differ by %.3f, closest split %.3f", got, want)
	}
}

// N6: undersized failsafe matches are sorted too. Six players, tickets old enough
// that failsafe_timeout has passed.
func TestCombatSort_UndersizedFailsafe(t *testing.T) {
	mus := []float64{5, 9, 14, 22, 31, 40}
	entries := combatSolos(mus...)
	for _, e := range entries {
		e.Properties["timestamp"] = float64(time.Now().UTC().Unix() - 3600)
		e.Properties["failsafe_timeout"] = 60.0
	}
	matches := runEvrMatchmaker(entries)
	if len(matches) != 1 || len(matches[0]) != 6 {
		t.Fatalf("expected one 6-player failsafe match, got %s", memberOrder(matches))
	}
	blue, orange := teamsOf(matches[0])
	if got, want := math.Abs(sumMu(blue)-sumMu(orange)), bruteForceMinDelta(mus); math.Abs(got-want) > 1e-9 {
		t.Errorf("failsafe match not sorted: teams differ by %.3f, closest split %.3f", got, want)
	}
}

// N7: with no ratings on the tickets (SBMM off) every split ties. The result
// must not depend on arrival order or on map iteration order.
func TestCombatSort_DeterministicTies(t *testing.T) {
	var first string
	for i := 0; i < 30; i++ {
		entries := combatSolos(-1, -1, -1, -1, -1, -1, -1, -1)
		if i%2 == 1 {
			slices.Reverse(entries)
		}
		got := memberOrder(runEvrMatchmaker(entries))
		if i == 0 {
			first = got
		}
		if got != first {
			t.Fatalf("run %d: order changed with arrival order:\n got  %s\n want %s", i, got, first)
		}
	}
}
