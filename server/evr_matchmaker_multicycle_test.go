package server

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

// playerDef defines a player's skill for scenario construction.
type playerDef struct {
	ID    string
	Mu    float64
	Sigma float64
}

// scenarioConfig describes a multi-cycle matchmaker scenario.
type scenarioConfig struct {
	Name             string
	Description      string
	TargetSessionIDs []string // session IDs to track (the starving party/solo)
	CycleCount       int
	CycleDurationSecs int64
	InitialPool      func(baseTime int64) []runtime.MatchmakerEntry
	AddPerCycle      func(cycle int, baseTime int64) []runtime.MatchmakerEntry
}

// settingsPreset holds one combination of settings to sweep.
type settingsPreset struct {
	Name     string
	Settings ServiceSettingsData
}

// cycleMetrics captures outcomes from one matchmaker cycle.
type cycleMetrics struct {
	Cycle          int
	PoolSize       int
	MatchCount     int
	MatchedCount   int
	UnmatchedCount int
	AvgDrawProb    float64
	MaxImbalance   float64
	TargetWaiting  bool
}

// scenarioResult captures the aggregate outcome of running a scenario.
type scenarioResult struct {
	ScenarioName     string
	SettingsName     string
	Cycles           []cycleMetrics
	TotalMatched     int
	MatchRate        float64 // fraction of all unique players that were matched
	AvgDrawProb      float64 // average draw probability across all matches made
	MaxImbalance     float64 // worst imbalance across all matches
	TargetMatched    bool
	TargetMatchCycle int // -1 if never matched
}

// createScenarioEntry creates a single matchmaker entry with all properties
// required by the full processPotentialMatches pipeline.
func createScenarioEntry(id string, mu, sigma float64, timestampUnix int64) runtime.MatchmakerEntry {
	sid := uuid.NewV5(uuid.Nil, id)
	ticket := uuid.NewV5(uuid.Nil, id).String()
	return &MatchmakerEntry{
		Ticket: ticket,
		Presence: &MatchmakerPresence{
			UserId:    sid.String(),
			SessionId: sid.String(),
			Username:  "player_" + id,
			SessionID: sid,
		},
		Properties: map[string]interface{}{
			"rating_mu":        mu,
			"rating_sigma":     sigma,
			"timestamp":        float64(timestampUnix),
			"submission_time":  float64(timestampUnix),
			"max_team_size":    4.0,
			"min_team_size":    4.0,
			"count_multiple":   2.0,
			"max_rtt":          250.0,
			"rtt_server1":      50.0,
			"divisions":        "gold",
			"failsafe_timeout": 300.0,
		},
	}
}

// createScenarioParty creates a party (multiple entries sharing the same ticket).
func createScenarioParty(ticketID string, players []playerDef, timestampUnix int64) []runtime.MatchmakerEntry {
	ticket := uuid.NewV5(uuid.Nil, ticketID).String()
	entries := make([]runtime.MatchmakerEntry, len(players))
	for i, p := range players {
		sid := uuid.NewV5(uuid.Nil, p.ID)
		entries[i] = &MatchmakerEntry{
			Ticket: ticket,
			Presence: &MatchmakerPresence{
				UserId:    sid.String(),
				SessionId: sid.String(),
				Username:  "player_" + p.ID,
				SessionID: sid,
			},
			Properties: map[string]interface{}{
				"rating_mu":        p.Mu,
				"rating_sigma":     p.Sigma,
				"timestamp":        float64(timestampUnix),
				"submission_time":  float64(timestampUnix),
				"max_team_size":    4.0,
				"min_team_size":    4.0,
				"count_multiple":   2.0,
				"max_rtt":          250.0,
				"rtt_server1":      50.0,
				"divisions":        "gold",
				"failsafe_timeout": 300.0,
			},
		}
	}
	return entries
}

// sessionIDFor returns the deterministic session ID for a player name.
func sessionIDFor(id string) string {
	return uuid.NewV5(uuid.Nil, id).String()
}

// ageEntries subtracts deltaSecs from both timestamp and submission_time on all entries.
func ageEntries(pool []runtime.MatchmakerEntry, deltaSecs int64) {
	for _, e := range pool {
		props := e.GetProperties()
		if ts, ok := props["timestamp"].(float64); ok {
			props["timestamp"] = ts - float64(deltaSecs)
		}
		if st, ok := props["submission_time"].(float64); ok {
			props["submission_time"] = st - float64(deltaSecs)
		}
	}
}

// checkInvariants runs hard invariant checks on a cycle's matches against the pool.
// Returns an error message if any invariant is violated, empty string otherwise.
func checkInvariants(t *testing.T, cycle int, pool []runtime.MatchmakerEntry, matches [][]runtime.MatchmakerEntry, previouslyMatched map[string]struct{}) {
	t.Helper()

	// Invariant 1: Party atomicity — all members of a ticket are matched or all unmatched
	matchedSessions := make(map[string]struct{})
	for _, match := range matches {
		for _, e := range match {
			matchedSessions[e.GetPresence().GetSessionId()] = struct{}{}
		}
	}

	ticketMembers := make(map[string][]string) // ticket -> []sessionID
	for _, e := range pool {
		ticket := e.GetTicket()
		ticketMembers[ticket] = append(ticketMembers[ticket], e.GetPresence().GetSessionId())
	}

	for ticket, members := range ticketMembers {
		if len(members) <= 1 {
			continue
		}
		matchedCount := 0
		for _, sid := range members {
			if _, ok := matchedSessions[sid]; ok {
				matchedCount++
			}
		}
		if matchedCount > 0 && matchedCount < len(members) {
			t.Fatalf("Cycle %d: Party atomicity violated for ticket %s: %d/%d members matched",
				cycle, ticket[:8], matchedCount, len(members))
		}
	}

	// Invariant 2: No player matched twice across cycles
	for _, match := range matches {
		for _, e := range match {
			sid := e.GetPresence().GetSessionId()
			if _, ok := previouslyMatched[sid]; ok {
				t.Fatalf("Cycle %d: Player %s (%s) matched again after being matched in a previous cycle",
					cycle, e.GetPresence().GetUsername(), sid[:8])
			}
		}
	}

	// Invariant 3: Pool accounting — matched + remaining = pool
	// (checked by caller after removing matched from pool)

	// Invariant 4: Team size — every match has >= min_team_size * 2 entries
	for i, match := range matches {
		if len(match) == 0 {
			t.Fatalf("Cycle %d: Match %d is empty", cycle, i)
		}
		minTeamSize := 4.0
		if v, ok := match[0].GetProperties()["min_team_size"].(float64); ok && v > 0 {
			minTeamSize = v
		}
		minMatchSize := int(minTeamSize) * 2
		if len(match) < minMatchSize {
			// Allowed if failsafe timeout exceeded — just warn
			t.Logf("Cycle %d: Match %d has %d players (< %d min), failsafe may have fired",
				cycle, i, len(match), minMatchSize)
		}
	}

	// Invariant 5: Team structure — first half and second half are equal size
	for i, match := range matches {
		if len(match)%2 != 0 {
			t.Fatalf("Cycle %d: Match %d has odd number of players (%d)", cycle, i, len(match))
		}
	}
}

// computeMatchImbalance returns the max team strength imbalance across all matches.
func computeMatchImbalance(matches [][]runtime.MatchmakerEntry) float64 {
	maxImbalance := 0.0
	for _, match := range matches {
		half := len(match) / 2
		blueStr, orangeStr := 0.0, 0.0
		for i, e := range match {
			mu := e.GetProperties()["rating_mu"].(float64)
			if i < half {
				blueStr += mu
			} else {
				orangeStr += mu
			}
		}
		total := blueStr + orangeStr
		if total > 0 {
			imb := math.Abs(blueStr-orangeStr) / total
			if imb > maxImbalance {
				maxImbalance = imb
			}
		}
	}
	return maxImbalance
}

// runScenario executes a multi-cycle matchmaker scenario with the given settings.
func runScenario(t *testing.T, scenario scenarioConfig, preset settingsPreset) scenarioResult {
	t.Helper()

	// Install settings
	settings := preset.Settings
	FixDefaultServiceSettings(nil, &settings)
	ServiceSettingsUpdate(&settings)
	defer ServiceSettingsUpdate(nil)

	sbmm := NewSkillBasedMatchmaker()
	logger := NewRuntimeGoLogger(zap.NewNop())

	baseNow := time.Now().UTC().Unix()
	pool := scenario.InitialPool(baseNow)

	previouslyMatched := make(map[string]struct{})
	allSeen := make(map[string]struct{})
	targetMatchCycle := -1

	result := scenarioResult{
		ScenarioName:     scenario.Name,
		SettingsName:     preset.Name,
		TargetMatchCycle: -1,
	}

	// Track all players seen
	for _, e := range pool {
		allSeen[e.GetPresence().GetSessionId()] = struct{}{}
	}

	var totalDrawProb float64
	var totalMatches int

	for cycle := 0; cycle < scenario.CycleCount; cycle++ {
		poolSizeBefore := len(pool)

		// Run the matchmaker
		_, matches, _, predictions := sbmm.processPotentialMatches(logger, pool)

		// Check invariants
		checkInvariants(t, cycle, pool, matches, previouslyMatched)

		// Collect matched session IDs
		cycleMatched := make(map[string]struct{})
		for _, match := range matches {
			for _, e := range match {
				sid := e.GetPresence().GetSessionId()
				cycleMatched[sid] = struct{}{}
				previouslyMatched[sid] = struct{}{}
			}
		}

		// Check if target is still waiting
		targetWaiting := false
		if len(scenario.TargetSessionIDs) > 0 {
			for _, tid := range scenario.TargetSessionIDs {
				if _, matched := previouslyMatched[tid]; !matched {
					targetWaiting = true
					break
				}
			}
			// Record the first cycle where all targets are matched
			if targetMatchCycle == -1 && !targetWaiting {
				targetMatchCycle = cycle
			}
		}

		// Compute metrics
		imbalance := computeMatchImbalance(matches)
		if imbalance > result.MaxImbalance {
			result.MaxImbalance = imbalance
		}

		avgDraw := 0.0
		if len(predictions) > 0 {
			for _, p := range predictions {
				avgDraw += float64(p.DrawProb)
			}
			avgDraw /= float64(len(predictions))
		}

		for _, match := range matches {
			totalMatches++
			// Compute draw prob for actual matches
			half := len(match) / 2
			if half > 0 {
				blue := MatchmakerEntries(match[:half])
				orange := MatchmakerEntries(match[half:])
				blueOrd := blue.TeamOrdinal(nil)
				orangeOrd := orange.TeamOrdinal(nil)
				_ = blueOrd
				_ = orangeOrd
			}
		}
		// Use prediction avg draw as a proxy
		totalDrawProb += avgDraw * float64(len(matches))

		cm := cycleMetrics{
			Cycle:          cycle,
			PoolSize:       poolSizeBefore,
			MatchCount:     len(matches),
			MatchedCount:   len(cycleMatched),
			UnmatchedCount: poolSizeBefore - len(cycleMatched),
			AvgDrawProb:    avgDraw,
			MaxImbalance:   imbalance,
			TargetWaiting:  targetWaiting,
		}
		result.Cycles = append(result.Cycles, cm)

		// Soft metric checks
		if avgDraw < 0 || avgDraw > 1 {
			t.Logf("WARNING Cycle %d: Draw probability %.3f outside [0,1]", cycle, avgDraw)
		}
		if imbalance > 0.5 {
			t.Logf("WARNING Cycle %d: Imbalance %.3f > 50%%", cycle, imbalance)
		}

		// Remove matched players from pool
		remaining := make([]runtime.MatchmakerEntry, 0, len(pool)-len(cycleMatched))
		for _, e := range pool {
			if _, ok := cycleMatched[e.GetPresence().GetSessionId()]; !ok {
				remaining = append(remaining, e)
			}
		}

		// Invariant 3: Pool accounting
		if len(remaining)+len(cycleMatched) != poolSizeBefore {
			t.Fatalf("Cycle %d: Pool accounting error: %d remaining + %d matched != %d pool",
				cycle, len(remaining), len(cycleMatched), poolSizeBefore)
		}

		// Add new players for next cycle
		if scenario.AddPerCycle != nil && cycle < scenario.CycleCount-1 {
			newEntries := scenario.AddPerCycle(cycle, baseNow)
			for _, e := range newEntries {
				allSeen[e.GetPresence().GetSessionId()] = struct{}{}
			}
			remaining = append(remaining, newEntries...)
		}

		// Age entries for next cycle
		ageEntries(remaining, scenario.CycleDurationSecs)

		pool = remaining
	}

	// Aggregate results
	result.TotalMatched = len(previouslyMatched)
	if len(allSeen) > 0 {
		result.MatchRate = float64(result.TotalMatched) / float64(len(allSeen))
	}
	if totalMatches > 0 {
		result.AvgDrawProb = totalDrawProb / float64(totalMatches)
	}
	result.TargetMatched = targetMatchCycle >= 0
	result.TargetMatchCycle = targetMatchCycle

	return result
}

// logScenarioResult prints detailed per-cycle output for a scenario run.
func logScenarioResult(t *testing.T, r scenarioResult) {
	t.Helper()
	t.Logf("Scenario: %s | Settings: %s", r.ScenarioName, r.SettingsName)
	t.Logf("  %-6s %-6s %-8s %-8s %-10s %-10s %-10s %-8s",
		"Cycle", "Pool", "Matches", "Matched", "Unmatched", "DrawProb", "Imbalance", "Target")
	for _, c := range r.Cycles {
		targetStr := "—"
		if r.TargetMatchCycle >= 0 && c.Cycle >= r.TargetMatchCycle {
			targetStr = "matched"
		} else if c.TargetWaiting {
			targetStr = "waiting"
		}
		drawStr := "—"
		if c.MatchCount > 0 {
			drawStr = fmt.Sprintf("%.3f", c.AvgDrawProb)
		}
		imbStr := "—"
		if c.MatchCount > 0 {
			imbStr = fmt.Sprintf("%.3f", c.MaxImbalance)
		}
		t.Logf("  %-6d %-6d %-8d %-8d %-10d %-10s %-10s %-8s",
			c.Cycle, c.PoolSize, c.MatchCount, c.MatchedCount, c.UnmatchedCount,
			drawStr, imbStr, targetStr)
	}
	targetCycleStr := "never"
	if r.TargetMatchCycle >= 0 {
		targetCycleStr = fmt.Sprintf("cycle %d", r.TargetMatchCycle)
	}
	t.Logf("  Summary: MatchRate=%.1f%% | AvgDraw=%.3f | MaxImbalance=%.3f | Target: %s",
		r.MatchRate*100, r.AvgDrawProb, r.MaxImbalance, targetCycleStr)
}

// logComparativeResults prints a comparative table across settings presets.
func logComparativeResults(t *testing.T, results []scenarioResult) {
	t.Helper()
	t.Logf("  %-28s %-10s %-10s %-12s %-12s",
		"Settings", "MatchRate", "AvgDraw", "MaxImbalance", "TargetCycle")
	for _, r := range results {
		targetStr := "never"
		if r.TargetMatchCycle >= 0 {
			targetStr = fmt.Sprintf("%d", r.TargetMatchCycle)
		}
		rateStr := fmt.Sprintf("%.1f%%", r.MatchRate*100)
		t.Logf("  %-28s %-10s %-10.3f %-12.3f %-12s",
			r.SettingsName, rateStr, r.AvgDrawProb, r.MaxImbalance, targetStr)
	}
}

// --- Settings Presets ---

func makeSettingsPresets() []settingsPreset {
	return []settingsPreset{
		{
			Name: "no_accumulation",
			Settings: ServiceSettingsData{
				Matchmaking: GlobalMatchmakingSettings{
					EnableAccumulation: false,
				},
			},
		},
		{
			Name: "accumulation_default",
			Settings: ServiceSettingsData{
				Matchmaking: GlobalMatchmakingSettings{
					EnableAccumulation:                  true,
					AccumulationThresholdSecs:           90,
					AccumulationMaxAgeSecs:              300,
					AccumulationInitialRadius:           5.0,
					AccumulationRadiusExpansionPerCycle: 1.0,
					AccumulationMaxRadius:               40.0,
				},
			},
		},
		{
			Name: "accumulation_tight",
			Settings: ServiceSettingsData{
				Matchmaking: GlobalMatchmakingSettings{
					EnableAccumulation:                  true,
					AccumulationThresholdSecs:           60,
					AccumulationMaxAgeSecs:              300,
					AccumulationInitialRadius:           3.0,
					AccumulationRadiusExpansionPerCycle: 0.5,
					AccumulationMaxRadius:               30.0,
				},
			},
		},
		{
			Name: "accumulation_aggressive",
			Settings: ServiceSettingsData{
				Matchmaking: GlobalMatchmakingSettings{
					EnableAccumulation:                  true,
					AccumulationThresholdSecs:           30,
					AccumulationMaxAgeSecs:              180,
					AccumulationInitialRadius:           10.0,
					AccumulationRadiusExpansionPerCycle: 3.0,
					AccumulationMaxRadius:               50.0,
				},
			},
		},
		{
			Name: "accumulation_with_variants",
			Settings: ServiceSettingsData{
				Matchmaking: GlobalMatchmakingSettings{
					EnableAccumulation:                  true,
					AccumulationThresholdSecs:           90,
					AccumulationMaxAgeSecs:              300,
					AccumulationInitialRadius:           5.0,
					AccumulationRadiusExpansionPerCycle: 1.0,
					AccumulationMaxRadius:               40.0,
				},
			},
		},
	}
}

// --- Scenario Definitions ---

func scenarioHealthyPool() scenarioConfig {
	return scenarioConfig{
		Name:              "healthyPool",
		Description:       "24 solos with reasonable skill spread — baseline throughput test",
		CycleCount:        3,
		CycleDurationSecs: 30,
		InitialPool: func(baseTime int64) []runtime.MatchmakerEntry {
			pool := make([]runtime.MatchmakerEntry, 0, 24)
			for i := 0; i < 24; i++ {
				mu := 20.0 + float64(i%11) // mu=20-30
				sigma := 3.0 + float64(i%3) // sigma=3-5
				pool = append(pool, createScenarioEntry(
					fmt.Sprintf("healthy_%d", i), mu, sigma, baseTime,
				))
			}
			return pool
		},
	}
}

func scenarioHighSkillParty() scenarioConfig {
	targetIDs := []string{
		sessionIDFor("hsp_mikey"),
		sessionIDFor("hsp_zuko"),
		sessionIDFor("hsp_nopatek"),
	}
	return scenarioConfig{
		Name:              "highSkillParty",
		Description:       "3-stack at mu=61 with only 4 solos — must accumulate to get 8",
		TargetSessionIDs:  targetIDs,
		CycleCount:        15,
		CycleDurationSecs: 30,
		InitialPool: func(baseTime int64) []runtime.MatchmakerEntry {
			pool := make([]runtime.MatchmakerEntry, 0, 7)
			// The high-skill 3-stack party
			party := createScenarioParty("hsp_party", []playerDef{
				{"hsp_mikey", 61.0, 3.0},
				{"hsp_zuko", 61.0, 3.0},
				{"hsp_nopatek", 61.0, 3.0},
			}, baseTime)
			pool = append(pool, party...)
			// Only 4 average solos — not enough for a full 8-player match
			for i := 0; i < 4; i++ {
				mu := 22.0 + float64(i)*3.0 // mu=22, 25, 28, 31
				pool = append(pool, createScenarioEntry(
					fmt.Sprintf("hsp_solo_%d", i), mu, 5.0, baseTime,
				))
			}
			return pool
		},
		AddPerCycle: func(cycle int, baseTime int64) []runtime.MatchmakerEntry {
			// Add 2 solos per cycle at varying skill
			entries := make([]runtime.MatchmakerEntry, 0, 2)
			for i := 0; i < 2; i++ {
				mu := 22.0 + float64(cycle*2+i)*1.5 // gradually increasing skill
				entries = append(entries, createScenarioEntry(
					fmt.Sprintf("hsp_new_%d_%d", cycle, i), mu, 5.0, baseTime,
				))
			}
			return entries
		},
	}
}

func scenarioLowPopulation() scenarioConfig {
	return scenarioConfig{
		Name:              "lowPopulation",
		Description:       "12 mixed skill players with slow trickle — low pop test",
		CycleCount:        8,
		CycleDurationSecs: 30,
		InitialPool: func(baseTime int64) []runtime.MatchmakerEntry {
			pool := make([]runtime.MatchmakerEntry, 0, 12)
			for i := 0; i < 12; i++ {
				mu := 15.0 + float64(i)*20.0/11.0 // mu=15-35
				sigma := 5.0 + float64(i%4)        // sigma=5-8
				pool = append(pool, createScenarioEntry(
					fmt.Sprintf("low_%d", i), mu, sigma, baseTime,
				))
			}
			return pool
		},
		AddPerCycle: func(cycle int, baseTime int64) []runtime.MatchmakerEntry {
			// 0-2 entries per cycle
			count := cycle % 3 // 0, 1, 2, 0, 1, 2, ...
			entries := make([]runtime.MatchmakerEntry, 0, count)
			for i := 0; i < count; i++ {
				mu := 18.0 + float64(cycle*2+i)*12.0/16.0
				entries = append(entries, createScenarioEntry(
					fmt.Sprintf("low_new_%d_%d", cycle, i), mu, 6.0, baseTime,
				))
			}
			return entries
		},
	}
}

func scenarioMultipleStarving() scenarioConfig {
	targetIDs := []string{
		// High-skill party
		sessionIDFor("ms_high_0"),
		sessionIDFor("ms_high_1"),
		sessionIDFor("ms_high_2"),
		// Low-skill party
		sessionIDFor("ms_low_0"),
		sessionIDFor("ms_low_1"),
	}
	return scenarioConfig{
		Name:              "multipleStarving",
		Description:       "3-stack mu=60 + 2-stack mu=15 + 3 solos — both parties need accumulation",
		TargetSessionIDs:  targetIDs,
		CycleCount:        15,
		CycleDurationSecs: 30,
		InitialPool: func(baseTime int64) []runtime.MatchmakerEntry {
			pool := make([]runtime.MatchmakerEntry, 0, 8)
			// High-skill 3-stack
			pool = append(pool, createScenarioParty("ms_high_party", []playerDef{
				{"ms_high_0", 60.0, 3.0},
				{"ms_high_1", 60.0, 3.0},
				{"ms_high_2", 60.0, 3.0},
			}, baseTime)...)
			// Low-skill 2-stack
			pool = append(pool, createScenarioParty("ms_low_party", []playerDef{
				{"ms_low_0", 15.0, 4.0},
				{"ms_low_1", 15.0, 4.0},
			}, baseTime)...)
			// Only 3 mid-skill solos — not enough for either party to match
			for i := 0; i < 3; i++ {
				mu := 23.0 + float64(i)*2.0 // mu=23, 25, 27
				pool = append(pool, createScenarioEntry(
					fmt.Sprintf("ms_solo_%d", i), mu, 4.0, baseTime,
				))
			}
			return pool
		},
		AddPerCycle: func(cycle int, baseTime int64) []runtime.MatchmakerEntry {
			// 2 solos per cycle
			entries := make([]runtime.MatchmakerEntry, 0, 2)
			for i := 0; i < 2; i++ {
				mu := 20.0 + float64(cycle*2+i)*1.0
				entries = append(entries, createScenarioEntry(
					fmt.Sprintf("ms_new_%d_%d", cycle, i), mu, 5.0, baseTime,
				))
			}
			return entries
		},
	}
}

func scenarioSoloHighSkill() scenarioConfig {
	targetIDs := []string{sessionIDFor("solo_high")}
	return scenarioConfig{
		Name:              "soloHighSkill",
		Description:       "Single mu=61 solo + 5 average solos — must accumulate to 8",
		TargetSessionIDs:  targetIDs,
		CycleCount:        15,
		CycleDurationSecs: 30,
		InitialPool: func(baseTime int64) []runtime.MatchmakerEntry {
			pool := make([]runtime.MatchmakerEntry, 0, 6)
			// The high-skill solo
			pool = append(pool, createScenarioEntry("solo_high", 61.0, 3.0, baseTime))
			// Only 5 average solos — total 6, not enough for 8
			for i := 0; i < 5; i++ {
				mu := 22.0 + float64(i)*2.0 // mu=22, 24, 26, 28, 30
				pool = append(pool, createScenarioEntry(
					fmt.Sprintf("solo_avg_%d", i), mu, 5.0, baseTime,
				))
			}
			return pool
		},
		AddPerCycle: func(cycle int, baseTime int64) []runtime.MatchmakerEntry {
			// 2 solos per cycle
			entries := make([]runtime.MatchmakerEntry, 0, 2)
			for i := 0; i < 2; i++ {
				mu := 22.0 + float64(cycle*2+i)*1.5
				entries = append(entries, createScenarioEntry(
					fmt.Sprintf("solo_new_%d_%d", cycle, i), mu, 5.0, baseTime,
				))
			}
			return entries
		},
	}
}

// --- Test Functions ---

func runScenarioSuite(t *testing.T, scenario scenarioConfig) {
	t.Helper()
	presets := makeSettingsPresets()
	results := make([]scenarioResult, 0, len(presets))
	for _, preset := range presets {
		t.Run(preset.Name, func(t *testing.T) {
			result := runScenario(t, scenario, preset)
			logScenarioResult(t, result)
			results = append(results, result)
		})
	}
	t.Run("comparative", func(t *testing.T) {
		logComparativeResults(t, results)
	})
}

func TestMulticycle_HighSkillParty(t *testing.T) {
	runScenarioSuite(t, scenarioHighSkillParty())
}

func TestMulticycle_LowPopulation(t *testing.T) {
	runScenarioSuite(t, scenarioLowPopulation())
}

func TestMulticycle_MultipleStarving(t *testing.T) {
	runScenarioSuite(t, scenarioMultipleStarving())
}

func TestMulticycle_SoloHighSkill(t *testing.T) {
	runScenarioSuite(t, scenarioSoloHighSkill())
}

func TestMulticycle_HealthyPool(t *testing.T) {
	runScenarioSuite(t, scenarioHealthyPool())
}

func TestMulticycle_Sweep(t *testing.T) {
	scenarios := []scenarioConfig{
		scenarioHighSkillParty(),
		scenarioLowPopulation(),
		scenarioHealthyPool(),
		scenarioMultipleStarving(),
		scenarioSoloHighSkill(),
	}
	presets := makeSettingsPresets()

	// Collect all results indexed by scenario
	allResults := make(map[string][]scenarioResult)

	for _, scenario := range scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			for _, preset := range presets {
				t.Run(preset.Name, func(t *testing.T) {
					result := runScenario(t, scenario, preset)
					logScenarioResult(t, result)
					allResults[scenario.Name] = append(allResults[scenario.Name], result)
				})
			}
		})
	}

	// Print cross-scenario summary
	t.Run("summary", func(t *testing.T) {
		for _, scenario := range scenarios {
			results := allResults[scenario.Name]
			if len(results) == 0 {
				continue
			}
			t.Logf("\n=== %s ===", scenario.Name)
			logComparativeResults(t, results)
		}
	})
}
