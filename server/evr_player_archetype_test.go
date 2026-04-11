package server

import (
	"testing"
)

func TestDetectArchetype_Rookie(t *testing.T) {
	stats := ArchetypeStats{
		Goals:       5,
		Assists:     3,
		Saves:       2,
		Steals:      4,
		Passes:      10,
		ShotsOnGoal: 12,
	}
	got := DetectArchetype(stats, 5, 50)
	if got != ArchetypeRookie {
		t.Errorf("expected %q for low games played, got %q", ArchetypeRookie, got)
	}
}

func TestDetectArchetype_Goalie(t *testing.T) {
	// save_focus = saves / (saves + goals + assists) = 20 / (20 + 5 + 3) = 0.714
	stats := ArchetypeStats{
		Goals:       5,
		Assists:     3,
		Saves:       20,
		Steals:      1,
		Passes:      5,
		ShotsOnGoal: 10,
	}
	got := DetectArchetype(stats, 100, 50)
	if got != ArchetypeGoalie {
		t.Errorf("expected %q, got %q", ArchetypeGoalie, got)
	}
}

func TestDetectArchetype_Striker(t *testing.T) {
	// goals/game = 150/100 = 1.5, shots/game = 300/100 = 3.0
	stats := ArchetypeStats{
		Goals:       150,
		Assists:     10,
		Saves:       5,
		Steals:      10,
		Passes:      30,
		ShotsOnGoal: 300,
	}
	got := DetectArchetype(stats, 100, 50)
	if got != ArchetypeStriker {
		t.Errorf("expected %q, got %q", ArchetypeStriker, got)
	}
}

func TestDetectArchetype_Playmaker(t *testing.T) {
	// assists/game = 50/100 = 0.5, passes/game = 250/100 = 2.5
	stats := ArchetypeStats{
		Goals:       30,
		Assists:     50,
		Saves:       20,
		Steals:      10,
		Passes:      250,
		ShotsOnGoal: 60,
	}
	got := DetectArchetype(stats, 100, 50)
	if got != ArchetypePlaymaker {
		t.Errorf("expected %q, got %q", ArchetypePlaymaker, got)
	}
}

func TestDetectArchetype_Interceptor(t *testing.T) {
	// steals/game = 70/100 = 0.7
	stats := ArchetypeStats{
		Goals:       30,
		Assists:     20,
		Saves:       15,
		Steals:      70,
		Passes:      50,
		ShotsOnGoal: 50,
	}
	got := DetectArchetype(stats, 100, 50)
	if got != ArchetypeInterceptor {
		t.Errorf("expected %q, got %q", ArchetypeInterceptor, got)
	}
}

func TestDetectArchetype_LowActivity(t *testing.T) {
	// Does not meet any specific archetype thresholds
	stats := ArchetypeStats{
		Goals:       20,
		Assists:     15,
		Saves:       10,
		Steals:      10,
		Passes:      30,
		ShotsOnGoal: 40,
	}
	got := DetectArchetype(stats, 100, 50)
	if got != ArchetypeLowActivity {
		t.Errorf("expected %q, got %q", ArchetypeLowActivity, got)
	}
}

func TestDetectArchetype_ZeroStats(t *testing.T) {
	stats := ArchetypeStats{}
	got := DetectArchetype(stats, 100, 50)
	if got != ArchetypeLowActivity {
		t.Errorf("expected %q for zero stats with enough games, got %q", ArchetypeLowActivity, got)
	}
}

func TestDetectArchetype_ZeroGamesPlayed(t *testing.T) {
	stats := ArchetypeStats{}
	got := DetectArchetype(stats, 0, 50)
	if got != ArchetypeRookie {
		t.Errorf("expected %q for zero games played, got %q", ArchetypeRookie, got)
	}
}

func TestDetectArchetype_BorderlineGoalie(t *testing.T) {
	// save_focus = 10 / (10 + 5 + 5) = 0.5 exactly -> should be goalie (> 0.5 is the threshold, so 0.5 is NOT goalie)
	stats := ArchetypeStats{
		Goals:       5,
		Assists:     5,
		Saves:       10,
		Steals:      1,
		Passes:      5,
		ShotsOnGoal: 10,
	}
	got := DetectArchetype(stats, 100, 50)
	if got == ArchetypeGoalie {
		t.Errorf("save_focus of exactly 0.5 should NOT classify as goalie, got %q", got)
	}
}

func TestDetectArchetype_GoaliePriorityOverStriker(t *testing.T) {
	// Player who has high saves AND high goals should be classified as goalie first
	// save_focus = 300 / (300 + 200 + 50) = 0.545
	// goals/game = 200/100 = 2.0, shots/game = 400/100 = 4.0
	stats := ArchetypeStats{
		Goals:       200,
		Assists:     50,
		Saves:       300,
		Steals:      10,
		Passes:      30,
		ShotsOnGoal: 400,
	}
	got := DetectArchetype(stats, 100, 50)
	if got != ArchetypeGoalie {
		t.Errorf("goalie should take priority over striker, got %q", got)
	}
}

func TestDetectArchetype_RookieOverridesEverything(t *testing.T) {
	// Even with amazing stats, low games_played means rookie
	stats := ArchetypeStats{
		Goals:       50,
		Assists:     30,
		Saves:       40,
		Steals:      20,
		Passes:      100,
		ShotsOnGoal: 100,
	}
	got := DetectArchetype(stats, 10, 50)
	if got != ArchetypeRookie {
		t.Errorf("rookie should override all other archetypes, got %q", got)
	}
}

func TestDetectArchetype_ExactThresholdNewPlayer(t *testing.T) {
	// games_played == threshold -> NOT rookie (threshold is exclusive: < threshold)
	stats := ArchetypeStats{}
	got := DetectArchetype(stats, 50, 50)
	if got == ArchetypeRookie {
		t.Errorf("games_played == threshold should NOT be rookie, got %q", got)
	}
}
