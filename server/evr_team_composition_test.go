package server

import (
	"fmt"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

func makeCompEntry(ticket string, archetype string, mu float64, gamesPlayed int) *MatchmakerEntry {
	sessionID := uuid.NewV5(uuid.Nil, ticket)
	return &MatchmakerEntry{
		Ticket: ticket,
		Presence: &MatchmakerPresence{
			UserId:    uuid.NewV5(uuid.Nil, ticket+"user").String(),
			SessionId: sessionID.String(),
			SessionID: sessionID,
			Username:  ticket,
		},
		Properties: map[string]any{
			"rating_mu":    mu,
			"rating_sigma": 3.0,
			"archetype":    archetype,
			"games_played": float64(gamesPlayed),
		},
	}
}

func TestScoreTeamComposition(t *testing.T) {
	tests := []struct {
		name              string
		team1Archetypes   []string
		team2Archetypes   []string
		team1HasNewPlayer bool
		team2HasNewPlayer bool
		want              int
	}{
		{
			name:            "both teams have striker and playmaker",
			team1Archetypes: []string{ArchetypeStriker, ArchetypePlaymaker, ArchetypeInterceptor, ArchetypeGoalie},
			team2Archetypes: []string{ArchetypeStriker, ArchetypePlaymaker, ArchetypeInterceptor, ArchetypeGoalie},
			want:            6, // +2+1 per team = 6
		},
		{
			name:            "team without striker scores lower",
			team1Archetypes: []string{ArchetypeStriker, ArchetypePlaymaker, ArchetypeInterceptor, ArchetypeGoalie},
			team2Archetypes: []string{ArchetypeGoalie, ArchetypePlaymaker, ArchetypeInterceptor, ArchetypeGoalie},
			want:            4, // team1: +2+1, team2: +0+1 = 4
		},
		{
			name:            "rookie stacking penalized",
			team1Archetypes: []string{ArchetypeRookie, ArchetypeRookie, ArchetypeStriker, ArchetypePlaymaker},
			team2Archetypes: []string{ArchetypeStriker, ArchetypePlaymaker, ArchetypeInterceptor, ArchetypeGoalie},
			want:            4, // team1: +2+1-2=1, team2: +2+1=3, total=4
		},
		{
			name:              "new player without striker or playmaker penalized",
			team1Archetypes:   []string{ArchetypeGoalie, ArchetypeInterceptor, ArchetypeLowActivity, ArchetypeGoalie},
			team2Archetypes:   []string{ArchetypeStriker, ArchetypePlaymaker, ArchetypeInterceptor, ArchetypeGoalie},
			team1HasNewPlayer: true,
			want:              2, // team1: 0-1=-1, team2: +2+1=3, total=2
		},
		{
			name:              "new player with playmaker teammate is fine",
			team1Archetypes:   []string{ArchetypePlaymaker, ArchetypeInterceptor, ArchetypeLowActivity, ArchetypeGoalie},
			team2Archetypes:   []string{ArchetypeStriker, ArchetypePlaymaker, ArchetypeInterceptor, ArchetypeGoalie},
			team1HasNewPlayer: true,
			want:              4, // team1: 0+1=1, team2: +2+1=3, total=4
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scoreTeamComposition(tt.team1Archetypes, tt.team2Archetypes, tt.team1HasNewPlayer, tt.team2HasNewPlayer)
			if got != tt.want {
				t.Errorf("scoreTeamComposition() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestScoreTeamComposition_StrikerPlaymakerBetterThanRookies(t *testing.T) {
	good := scoreTeamComposition(
		[]string{ArchetypeStriker, ArchetypePlaymaker, ArchetypeInterceptor, ArchetypeGoalie},
		[]string{ArchetypeStriker, ArchetypePlaymaker, ArchetypeInterceptor, ArchetypeGoalie},
		false, false,
	)
	bad := scoreTeamComposition(
		[]string{ArchetypeRookie, ArchetypeRookie, ArchetypeInterceptor, ArchetypeGoalie},
		[]string{ArchetypeRookie, ArchetypeRookie, ArchetypeInterceptor, ArchetypeGoalie},
		false, false,
	)
	if good <= bad {
		t.Errorf("striker+playmaker teams (%d) should score higher than double-rookie teams (%d)", good, bad)
	}
}

func TestSelectBestTeamSplit(t *testing.T) {
	newPlayerThreshold := 50

	entries := []runtime.MatchmakerEntry{
		makeCompEntry("t1-striker", ArchetypeStriker, 20.0, 200),
		makeCompEntry("t1-playmaker", ArchetypePlaymaker, 19.0, 150),
		makeCompEntry("t1-goalie", ArchetypeGoalie, 18.0, 100),
		makeCompEntry("t1-interceptor", ArchetypeInterceptor, 17.0, 80),
		makeCompEntry("t2-striker", ArchetypeStriker, 20.0, 200),
		makeCompEntry("t2-playmaker", ArchetypePlaymaker, 19.0, 150),
		makeCompEntry("t2-goalie", ArchetypeGoalie, 18.0, 100),
		makeCompEntry("t2-interceptor", ArchetypeInterceptor, 17.0, 80),
	}

	// Good split: each team gets a striker + playmaker
	goodSplit := teamSplit{
		blueIndices:   []int{0, 1, 6, 7}, // striker, playmaker, goalie, interceptor
		orangeIndices: []int{4, 5, 2, 3}, // striker, playmaker, goalie, interceptor
	}

	// Bad split: one team has all the strikers/playmakers
	badSplit := teamSplit{
		blueIndices:   []int{0, 1, 4, 5}, // 2 strikers, 2 playmakers
		orangeIndices: []int{2, 3, 6, 7}, // 2 goalies, 2 interceptors
	}

	splits := []teamSplit{badSplit, goodSplit}

	best := selectBestTeamSplit(entries, splits, newPlayerThreshold)

	goodScore := scoreTeamSplitComposition(entries, goodSplit, newPlayerThreshold)
	badScore := scoreTeamSplitComposition(entries, badSplit, newPlayerThreshold)
	bestScore := scoreTeamSplitComposition(entries, best, newPlayerThreshold)

	if bestScore < goodScore {
		t.Errorf("selectBestTeamSplit chose split with score %d, expected at least %d (good=%d, bad=%d)",
			bestScore, goodScore, goodScore, badScore)
	}
}

func TestSelectBestTeamSplit_NewPlayerWithSupportiveTeammate(t *testing.T) {
	newPlayerThreshold := 50

	entries := []runtime.MatchmakerEntry{
		makeCompEntry("new-player", ArchetypeRookie, 10.0, 5),
		makeCompEntry("striker", ArchetypeStriker, 20.0, 200),
		makeCompEntry("playmaker", ArchetypePlaymaker, 19.0, 150),
		makeCompEntry("goalie1", ArchetypeGoalie, 18.0, 100),
		makeCompEntry("interceptor1", ArchetypeInterceptor, 20.0, 200),
		makeCompEntry("interceptor2", ArchetypeInterceptor, 19.0, 150),
		makeCompEntry("goalie2", ArchetypeGoalie, 18.0, 100),
		makeCompEntry("low-activity", ArchetypeLowActivity, 17.0, 80),
	}

	// New player paired with striker
	goodSplit := teamSplit{
		blueIndices:   []int{0, 1, 6, 7}, // new player + striker + goalie + low_activity
		orangeIndices: []int{2, 3, 4, 5}, // playmaker + goalie + 2 interceptors
	}

	// New player with only goalies and low-activity
	badSplit := teamSplit{
		blueIndices:   []int{0, 3, 6, 7}, // new player + 2 goalies + low_activity
		orangeIndices: []int{1, 2, 4, 5}, // striker + playmaker + 2 interceptors
	}

	splits := []teamSplit{badSplit, goodSplit}

	best := selectBestTeamSplit(entries, splits, newPlayerThreshold)
	bestScore := scoreTeamSplitComposition(entries, best, newPlayerThreshold)
	goodScore := scoreTeamSplitComposition(entries, goodSplit, newPlayerThreshold)

	if bestScore < goodScore {
		t.Errorf("expected new player to be paired with supportive teammate, best score=%d, good score=%d", bestScore, goodScore)
	}
}

func TestSelectBestTeamSplit_SingleSplit(t *testing.T) {
	entries := []runtime.MatchmakerEntry{
		makeCompEntry("p1", ArchetypeStriker, 20.0, 200),
		makeCompEntry("p2", ArchetypePlaymaker, 19.0, 150),
		makeCompEntry("p3", ArchetypeGoalie, 18.0, 100),
		makeCompEntry("p4", ArchetypeInterceptor, 17.0, 80),
	}

	splits := []teamSplit{
		{blueIndices: []int{0, 1}, orangeIndices: []int{2, 3}},
	}

	best := selectBestTeamSplit(entries, splits, 50)
	if len(best.blueIndices) != 2 || len(best.orangeIndices) != 2 {
		t.Errorf("expected the only available split to be returned")
	}
}

func TestSelectBestTeamSplit_EmptySplits(t *testing.T) {
	entries := []runtime.MatchmakerEntry{
		makeCompEntry("p1", ArchetypeStriker, 20.0, 200),
	}

	best := selectBestTeamSplit(entries, nil, 50)
	if len(best.blueIndices) != 0 || len(best.orangeIndices) != 0 {
		t.Errorf("expected empty split for nil splits input")
	}
}

func TestEntryArchetype_MissingReturnsEmpty(t *testing.T) {
	// Entry with no archetype property should return empty string, not "rookie"
	entry := makeCompEntry("no-archetype", "", 20.0, 200)
	entry.Properties["archetype"] = ""
	got := entryArchetype(entry)
	if got != "" {
		t.Errorf("entryArchetype() with empty archetype = %q, want empty string", got)
	}
}

func TestEntryArchetype_UnknownReturnsEmpty(t *testing.T) {
	entry := makeCompEntry("unknown", "not_a_real_archetype", 20.0, 200)
	got := entryArchetype(entry)
	if got != "" {
		t.Errorf("entryArchetype() with unknown archetype = %q, want empty string", got)
	}
}

func TestEntryArchetype_ValidArchetypes(t *testing.T) {
	for _, arch := range []string{ArchetypeStriker, ArchetypePlaymaker, ArchetypeRookie,
		ArchetypeGoalie, ArchetypeInterceptor, ArchetypeLowActivity} {
		entry := makeCompEntry("test", arch, 20.0, 200)
		got := entryArchetype(entry)
		if got != arch {
			t.Errorf("entryArchetype() = %q, want %q", got, arch)
		}
	}
}

func TestScoreTeamComposition_AllSameArchetype(t *testing.T) {
	// Both teams entirely Strikers: each team gets +2 for having a striker, no playmaker bonus.
	team1 := []string{ArchetypeStriker, ArchetypeStriker, ArchetypeStriker, ArchetypeStriker}
	team2 := []string{ArchetypeStriker, ArchetypeStriker, ArchetypeStriker, ArchetypeStriker}
	got := scoreTeamComposition(team1, team2, false, false)
	if got <= 0 {
		t.Errorf("all-striker teams should have positive score, got %d", got)
	}
	if got != 4 {
		t.Errorf("scoreTeamComposition() = %d, want 4 (+2 striker per team)", got)
	}
}

func TestScoreTeamComposition_AllRookies(t *testing.T) {
	// Both teams entirely Rookies: each team gets -2 for 2+ rookies, no striker/playmaker bonus.
	team1 := []string{ArchetypeRookie, ArchetypeRookie, ArchetypeRookie, ArchetypeRookie}
	team2 := []string{ArchetypeRookie, ArchetypeRookie, ArchetypeRookie, ArchetypeRookie}
	got := scoreTeamComposition(team1, team2, false, false)
	if got >= 0 {
		t.Errorf("all-rookie teams should have negative score, got %d", got)
	}
	if got != -4 {
		t.Errorf("scoreTeamComposition() = %d, want -4 (-2 rookie penalty per team)", got)
	}
}

func TestScoreTeamComposition_EmptyArchetypes(t *testing.T) {
	// Empty archetype strings (detection disabled): should not crash, should be neutral.
	team1 := []string{"", "", "", ""}
	team2 := []string{"", "", "", ""}
	got := scoreTeamComposition(team1, team2, false, false)
	if got != 0 {
		t.Errorf("scoreTeamComposition() with empty archetypes = %d, want 0", got)
	}
}

func TestScoreTeamComposition_SinglePlayerTeams(t *testing.T) {
	// Teams of 1 player each. Scoring should still work correctly.
	team1 := []string{ArchetypeStriker}
	team2 := []string{ArchetypePlaymaker}
	got := scoreTeamComposition(team1, team2, false, false)
	// team1: +2 (striker), team2: +1 (playmaker) = 3
	if got != 3 {
		t.Errorf("scoreTeamComposition() = %d, want 3", got)
	}
}

func TestSelectBestTeamSplit_AllSplitsEqual(t *testing.T) {
	// When all splits have the same composition score, should return the first one.
	entries := []runtime.MatchmakerEntry{
		makeCompEntry("p1", ArchetypeStriker, 20.0, 200),
		makeCompEntry("p2", ArchetypeStriker, 20.0, 200),
		makeCompEntry("p3", ArchetypeStriker, 20.0, 200),
		makeCompEntry("p4", ArchetypeStriker, 20.0, 200),
	}

	split0 := teamSplit{blueIndices: []int{0, 1}, orangeIndices: []int{2, 3}}
	split1 := teamSplit{blueIndices: []int{0, 2}, orangeIndices: []int{1, 3}}
	split2 := teamSplit{blueIndices: []int{0, 3}, orangeIndices: []int{1, 2}}

	splits := []teamSplit{split0, split1, split2}
	best := selectBestTeamSplit(entries, splits, 50)

	// All have same score, so the first split should be returned (deterministic).
	if best.blueIndices[0] != split0.blueIndices[0] || best.blueIndices[1] != split0.blueIndices[1] ||
		best.orangeIndices[0] != split0.orangeIndices[0] || best.orangeIndices[1] != split0.orangeIndices[1] {
		t.Errorf("expected first split to be returned when all scores are equal, got blue=%v orange=%v",
			best.blueIndices, best.orangeIndices)
	}
}

func TestEntryArchetype_InvalidValues(t *testing.T) {
	// Random garbage strings should return empty, not match any valid archetype.
	for _, garbage := range []string{"xyzzy", "STRIKER", "Playmaker", "goalie!", "123", "striker "} {
		entry := makeCompEntry("garbage", garbage, 20.0, 200)
		got := entryArchetype(entry)
		if got != "" {
			t.Errorf("entryArchetype(%q) = %q, want empty string", garbage, got)
		}
	}
}

func BenchmarkScoreTeamComposition(b *testing.B) {
	team1 := []string{ArchetypeStriker, ArchetypePlaymaker, ArchetypeInterceptor, ArchetypeGoalie}
	team2 := []string{ArchetypeStriker, ArchetypePlaymaker, ArchetypeInterceptor, ArchetypeGoalie}
	b.ResetTimer()
	for range b.N {
		scoreTeamComposition(team1, team2, false, false)
	}
}

func BenchmarkSelectBestTeamSplit(b *testing.B) {
	entries := make([]runtime.MatchmakerEntry, 8)
	archetypes := []string{ArchetypeStriker, ArchetypePlaymaker, ArchetypeGoalie, ArchetypeInterceptor,
		ArchetypeStriker, ArchetypePlaymaker, ArchetypeGoalie, ArchetypeInterceptor}
	for i := range 8 {
		entries[i] = makeCompEntry(fmt.Sprintf("p%d", i), archetypes[i], 20.0, 200)
	}

	splits := make([]teamSplit, 0, 35)
	for mask := 0; mask < (1 << 8); mask++ {
		blue := make([]int, 0, 4)
		orange := make([]int, 0, 4)
		for bit := range 8 {
			if mask&(1<<bit) != 0 {
				blue = append(blue, bit)
			} else {
				orange = append(orange, bit)
			}
		}
		if len(blue) == 4 && len(orange) == 4 {
			splits = append(splits, teamSplit{blueIndices: blue, orangeIndices: orange})
		}
	}

	b.ResetTimer()
	for range b.N {
		selectBestTeamSplit(entries, splits, 50)
	}
}
