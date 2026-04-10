package server

import (
	"math"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

func TestAccumulateForStarving_IdentifiesStarvingTicket(t *testing.T) {
	m := NewSkillBasedMatchmaker()
	logger := NewRuntimeGoLogger(zap.NewNop())
	now := time.Now().UTC().Unix()

	settings := &GlobalMatchmakingSettings{
		EnableAccumulation:                  true,
		AccumulationThresholdSecs:           90,
		AccumulationMaxAgeSecs:              300,
		AccumulationInitialRadius:           5.0,
		AccumulationRadiusExpansionPerCycle: 1.0,
		AccumulationMaxRadius:               40.0,
	}

	// Party waiting 120s (above 90s threshold)
	party := createScenarioParty("starving_party", []playerDef{
		{"p1", 61.0, 3.0},
		{"p2", 61.0, 3.0},
		{"p3", 61.0, 3.0},
	}, now-120)

	// Solos at mu=25 — outside initial radius of 5 from CenterMu=61
	solos := make([]runtime.MatchmakerEntry, 0, 5)
	for i := 0; i < 5; i++ {
		solos = append(solos, createScenarioEntry(
			"solo_"+string(rune('a'+i)), 25.0, 5.0, now-30,
		))
	}

	entries := append(party, solos...)
	preFormed, remaining := m.accumulateForStarving(logger, entries, settings)

	if len(preFormed) != 0 {
		t.Errorf("Expected no pre-formed candidates, got %d", len(preFormed))
	}
	// Solos remain (5), party members are reserved for accumulation (3)
	if len(remaining) != 5 {
		t.Errorf("Expected 5 remaining (solos only), got %d", len(remaining))
	}

	if len(m.accumulationPools) != 1 {
		t.Fatalf("Expected 1 accumulation pool, got %d", len(m.accumulationPools))
	}
	pool := m.accumulationPools[party[0].GetTicket()]
	if pool == nil {
		t.Fatal("Expected pool for party ticket")
	}
	if math.Abs(pool.CenterMu-61.0) > 0.01 {
		t.Errorf("Expected CenterMu=61.0, got %.1f", pool.CenterMu)
	}
	if pool.TargetSize != 8 {
		t.Errorf("Expected TargetSize=8, got %d", pool.TargetSize)
	}
}

func TestAccumulateForStarving_ReservesAndPromotes(t *testing.T) {
	m := NewSkillBasedMatchmaker()
	logger := NewRuntimeGoLogger(zap.NewNop())
	now := time.Now().UTC().Unix()

	settings := &GlobalMatchmakingSettings{
		EnableAccumulation:                  true,
		AccumulationThresholdSecs:           90,
		AccumulationMaxAgeSecs:              300,
		AccumulationInitialRadius:           50.0, // wide enough to catch all solos
		AccumulationRadiusExpansionPerCycle: 1.0,
		AccumulationMaxRadius:               100.0,
	}

	party := createScenarioParty("starving_party", []playerDef{
		{"p1", 61.0, 3.0},
		{"p2", 61.0, 3.0},
		{"p3", 61.0, 3.0},
	}, now-120)

	// 7 solos — 3 party + 5 needed = 8; 2 should remain
	solos := make([]runtime.MatchmakerEntry, 0, 7)
	for i := 0; i < 7; i++ {
		solos = append(solos, createScenarioEntry(
			"solo_"+string(rune('a'+i)), 25.0+float64(i)*3, 5.0, now-30,
		))
	}

	entries := append(party, solos...)
	preFormed, remaining := m.accumulateForStarving(logger, entries, settings)

	if len(preFormed) != 1 {
		t.Fatalf("Expected 1 pre-formed candidate, got %d", len(preFormed))
	}
	if len(preFormed[0]) != 8 {
		t.Errorf("Expected 8 players in candidate, got %d", len(preFormed[0]))
	}
	if len(remaining) != 2 {
		t.Errorf("Expected 2 remaining, got %d", len(remaining))
	}
	if len(m.accumulationPools) != 0 {
		t.Errorf("Expected 0 pools after promotion, got %d", len(m.accumulationPools))
	}
}

func TestAccumulateForStarving_WidensRadiusOverTime(t *testing.T) {
	m := NewSkillBasedMatchmaker()
	logger := NewRuntimeGoLogger(zap.NewNop())
	now := time.Now().UTC().Unix()

	settings := &GlobalMatchmakingSettings{
		EnableAccumulation:                  true,
		AccumulationThresholdSecs:           90,
		AccumulationMaxAgeSecs:              300,
		AccumulationInitialRadius:           5.0,
		AccumulationRadiusExpansionPerCycle: 10.0, // aggressive for test
		AccumulationMaxRadius:               100.0,
	}

	party := createScenarioParty("starving_party", []playerDef{
		{"p1", 61.0, 3.0},
		{"p2", 61.0, 3.0},
		{"p3", 61.0, 3.0},
	}, now-120)

	// Solos at mu=30 — 31 units from CenterMu=61
	solos := make([]runtime.MatchmakerEntry, 0, 5)
	for i := 0; i < 5; i++ {
		solos = append(solos, createScenarioEntry(
			"solo_"+string(rune('a'+i)), 30.0, 5.0, now-30,
		))
	}

	entries := append(party, solos...)

	// Cycle 1: radius = 5.0, distance = 31 — outside
	preFormed, _ := m.accumulateForStarving(logger, entries, settings)
	if len(preFormed) != 0 {
		t.Errorf("Cycle 1: Expected no promotion, got %d", len(preFormed))
	}
	pool := m.accumulationPools[party[0].GetTicket()]
	if pool == nil {
		t.Fatal("Expected pool")
	}
	if len(pool.ReservedPlayers) != 0 {
		t.Errorf("Cycle 1: Expected 0 reserved, got %d", len(pool.ReservedPlayers))
	}

	// Simulate 3 cycles passing (~90s)
	pool.CreatedAt = pool.CreatedAt.Add(-90 * time.Second)

	// Cycle 4: radius = 5 + (90/30)*10 = 35, distance = 31 — inside
	preFormed, _ = m.accumulateForStarving(logger, entries, settings)
	if len(preFormed) != 1 {
		t.Fatalf("Cycle 4: Expected 1 promotion, got %d", len(preFormed))
	}
}

func TestAccumulateForStarving_PrunesStaleState(t *testing.T) {
	m := NewSkillBasedMatchmaker()
	logger := NewRuntimeGoLogger(zap.NewNop())
	now := time.Now().UTC().Unix()

	settings := &GlobalMatchmakingSettings{
		EnableAccumulation:                  true,
		AccumulationThresholdSecs:           90,
		AccumulationMaxAgeSecs:              300,
		AccumulationInitialRadius:           5.0,
		AccumulationRadiusExpansionPerCycle: 1.0,
		AccumulationMaxRadius:               40.0,
	}

	m.accumulationPools["gone_ticket"] = &AccumulationPool{
		StarvingTicket: "gone_ticket",
		CreatedAt:      time.Now(),
	}

	entry := createScenarioEntry("fresh", 25.0, 5.0, now-10)
	m.accumulateForStarving(logger, []runtime.MatchmakerEntry{entry}, settings)

	if _, exists := m.accumulationPools["gone_ticket"]; exists {
		t.Error("Expected stale pool to be pruned")
	}
}

func TestAccumulateForStarving_SafetyValve(t *testing.T) {
	m := NewSkillBasedMatchmaker()
	logger := NewRuntimeGoLogger(zap.NewNop())
	now := time.Now().UTC().Unix()

	settings := &GlobalMatchmakingSettings{
		EnableAccumulation:                  true,
		AccumulationThresholdSecs:           90,
		AccumulationMaxAgeSecs:              300,
		AccumulationInitialRadius:           5.0,
		AccumulationRadiusExpansionPerCycle: 1.0,
		AccumulationMaxRadius:               40.0,
	}

	ticket := createScenarioParty("old_party", []playerDef{
		{"p1", 61.0, 3.0},
	}, now-400)

	m.accumulationPools[ticket[0].GetTicket()] = &AccumulationPool{
		StarvingTicket:  ticket[0].GetTicket(),
		StarvingEntries: []string{ticket[0].GetPresence().GetSessionId()},
		ReservedPlayers: []string{"some_reserved"},
		CreatedAt:       time.Now().Add(-6 * time.Minute),
		TargetSize:      8,
	}

	m.accumulateForStarving(logger, []runtime.MatchmakerEntry{ticket[0]}, settings)

	if _, exists := m.accumulationPools[ticket[0].GetTicket()]; exists {
		t.Error("Expected safety valve to remove old pool")
	}
}

func TestAccumulateForStarving_DisabledReturnsAllEntries(t *testing.T) {
	m := NewSkillBasedMatchmaker()
	logger := NewRuntimeGoLogger(zap.NewNop())
	now := time.Now().UTC().Unix()

	settings := &GlobalMatchmakingSettings{
		EnableAccumulation: false,
	}

	entries := []runtime.MatchmakerEntry{
		createScenarioEntry("p1", 25.0, 5.0, now-10),
		createScenarioEntry("p2", 30.0, 5.0, now-10),
	}

	preFormed, remaining := m.accumulateForStarving(logger, entries, settings)
	if len(preFormed) != 0 {
		t.Errorf("Expected no pre-formed, got %d", len(preFormed))
	}
	if len(remaining) != 2 {
		t.Errorf("Expected 2 remaining, got %d", len(remaining))
	}
}

func TestAccumulateForStarving_ReservesClosestFirst(t *testing.T) {
	m := NewSkillBasedMatchmaker()
	logger := NewRuntimeGoLogger(zap.NewNop())
	now := time.Now().UTC().Unix()

	settings := &GlobalMatchmakingSettings{
		EnableAccumulation:                  true,
		AccumulationThresholdSecs:           90,
		AccumulationMaxAgeSecs:              300,
		AccumulationInitialRadius:           50.0,
		AccumulationRadiusExpansionPerCycle: 1.0,
		AccumulationMaxRadius:               100.0,
	}

	// 1 solo starving at mu=50
	starving := createScenarioEntry("starving", 50.0, 3.0, now-120)

	// Solos at varying distances from mu=50
	entries := []runtime.MatchmakerEntry{
		starving,
		createScenarioEntry("close1", 48.0, 5.0, now-30), // dist=2
		createScenarioEntry("close2", 52.0, 5.0, now-30), // dist=2
		createScenarioEntry("mid1", 40.0, 5.0, now-30),   // dist=10
		createScenarioEntry("mid2", 60.0, 5.0, now-30),   // dist=10
		createScenarioEntry("far1", 25.0, 5.0, now-30),   // dist=25
		createScenarioEntry("far2", 75.0, 5.0, now-30),   // dist=25
		createScenarioEntry("far3", 20.0, 5.0, now-30),   // dist=30
		createScenarioEntry("far4", 80.0, 5.0, now-30),   // dist=30
	}

	preFormed, remaining := m.accumulateForStarving(logger, entries, settings)

	// 1 starving + 7 needed = 8; should promote with closest 7
	if len(preFormed) != 1 {
		t.Fatalf("Expected 1 pre-formed, got %d", len(preFormed))
	}

	// The candidate should contain the starving player + 7 closest
	candidate := preFormed[0]
	if len(candidate) != 8 {
		t.Fatalf("Expected 8 in candidate, got %d", len(candidate))
	}

	// 1 player should remain (the farthest one)
	if len(remaining) != 1 {
		t.Errorf("Expected 1 remaining, got %d", len(remaining))
	}
}
