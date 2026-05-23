package server

import (
	"math"
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

// ----- Override tests -----

// Override() must only return non-nil for OperatorSet.
// All other operators (including OperatorBest) are resolved to OperatorSet via RMW
// before Override() is called, so they should never appear at this stage.
func TestOverride_OnlySetIsAccepted(t *testing.T) {
	e := StatisticsQueueEntry{BoardMeta: LeaderboardMeta{Operator: OperatorSet}}
	got := e.Override()
	if got == nil {
		t.Fatal("Override() returned nil for OperatorSet")
	}
	if *got != 2 {
		t.Fatalf("Override() = %d, want 2 (SET)", *got)
	}
}

func TestOverride_RejectsNonSet(t *testing.T) {
	// All operators other than OperatorSet must return nil.
	// OperatorBest is resolved to Set via RMW before this point, so reaching
	// Override() with Best is an invariant violation and must be rejected.
	invalidOps := []LeaderboardOperator{
		OperatorBest,
		OperatorIncrement,
		OperatorDecrement,
		"unknown",
		"",
		"garbage",
	}

	for _, op := range invalidOps {
		t.Run(string(op), func(t *testing.T) {
			e := StatisticsQueueEntry{BoardMeta: LeaderboardMeta{Operator: op}}
			if got := e.Override(); got != nil {
				t.Fatalf("Override() = %d, want nil for operator %q", *got, op)
			}
		})
	}
}

// ----- applyIncrementDecrement tests -----

func encodeScore(t *testing.T, v float64) (score, subscore int64) {
	t.Helper()
	s, ss, err := Float64ToScore(v)
	if err != nil {
		t.Fatalf("Float64ToScore(%f): %v", v, err)
	}
	return s, ss
}

func TestApplyIncrementDecrement_Add(t *testing.T) {
	score5, sub5 := encodeScore(t, 5.0)
	score15, sub15 := encodeScore(t, 15.0)

	e := &StatisticsQueueEntry{
		BoardMeta: LeaderboardMeta{Operator: OperatorIncrement},
		Score:     score5,
		Subscore:  sub5,
	}

	if err := applyIncrementDecrement(e, 10.0); err != nil {
		t.Fatalf("applyIncrementDecrement: %v", err)
	}

	if e.Score != score15 || e.Subscore != sub15 {
		t.Fatalf("expected score=%d subscore=%d, got score=%d subscore=%d",
			score15, sub15, e.Score, e.Subscore)
	}
	if e.BoardMeta.Operator != OperatorSet {
		t.Fatalf("expected operator to be converted to Set, got %s", e.BoardMeta.Operator)
	}
}

func TestApplyIncrementDecrement_Subtract(t *testing.T) {
	score3, sub3 := encodeScore(t, 3.0)
	score4, sub4 := encodeScore(t, 4.0)

	e := &StatisticsQueueEntry{
		BoardMeta: LeaderboardMeta{Operator: OperatorDecrement},
		Score:     score3,
		Subscore:  sub3,
	}

	if err := applyIncrementDecrement(e, 7.0); err != nil {
		t.Fatalf("applyIncrementDecrement: %v", err)
	}

	if e.Score != score4 || e.Subscore != sub4 {
		t.Fatalf("expected score=%d subscore=%d, got score=%d subscore=%d",
			score4, sub4, e.Score, e.Subscore)
	}
	if e.BoardMeta.Operator != OperatorSet {
		t.Fatalf("expected operator to be converted to Set, got %s", e.BoardMeta.Operator)
	}
}

func TestApplyIncrementDecrement_FloatPrecision(t *testing.T) {
	// Verify float round-trip preserves precision
	delta := 0.1
	current := 0.3
	want := current + delta

	scoreDelta, subDelta := encodeScore(t, delta)
	scoreWant, subWant := encodeScore(t, want)

	e := &StatisticsQueueEntry{
		BoardMeta: LeaderboardMeta{Operator: OperatorIncrement},
		Score:     scoreDelta,
		Subscore:  subDelta,
	}

	if err := applyIncrementDecrement(e, current); err != nil {
		t.Fatalf("applyIncrementDecrement: %v", err)
	}

	if e.Score != scoreWant || e.Subscore != subWant {
		// Decode for a more readable error
		got, _ := ScoreToFloat64(e.Score, e.Subscore)
		t.Fatalf("expected %f (score=%d sub=%d), got %f (score=%d sub=%d)",
			want, scoreWant, subWant, got, e.Score, e.Subscore)
	}
}

func TestApplyIncrementDecrement_NegativeDecodeError(t *testing.T) {
	// ScoreToFloat64 with negative score should error
	e := &StatisticsQueueEntry{
		BoardMeta: LeaderboardMeta{Operator: OperatorIncrement},
		Score:     -1,
		Subscore:  0,
	}

	if err := applyIncrementDecrement(e, 10.0); err == nil {
		t.Fatal("expected error for invalid delta encoding")
	}
}

// ----- applyBest tests -----

func TestApplyBest_KeepsHigherValue(t *testing.T) {
	// incoming (20) > current (10) → result should be 20
	score20, sub20 := encodeScore(t, 20.0)

	e := &StatisticsQueueEntry{
		BoardMeta: LeaderboardMeta{Operator: OperatorBest},
		Score:     score20,
		Subscore:  sub20,
	}

	if err := applyBest(e, 10.0); err != nil {
		t.Fatalf("applyBest: %v", err)
	}

	if e.Score != score20 || e.Subscore != sub20 {
		got, _ := ScoreToFloat64(e.Score, e.Subscore)
		t.Fatalf("expected 20.0 (score=%d sub=%d), got %f (score=%d sub=%d)",
			score20, sub20, got, e.Score, e.Subscore)
	}
	if e.BoardMeta.Operator != OperatorSet {
		t.Fatalf("expected operator converted to Set, got %s", e.BoardMeta.Operator)
	}
}

func TestApplyBest_KeepsCurrentWhenHigher(t *testing.T) {
	// current (30) > incoming (15) → result should be 30
	score15, sub15 := encodeScore(t, 15.0)
	score30, sub30 := encodeScore(t, 30.0)

	e := &StatisticsQueueEntry{
		BoardMeta: LeaderboardMeta{Operator: OperatorBest},
		Score:     score15,
		Subscore:  sub15,
	}

	if err := applyBest(e, 30.0); err != nil {
		t.Fatalf("applyBest: %v", err)
	}

	if e.Score != score30 || e.Subscore != sub30 {
		got, _ := ScoreToFloat64(e.Score, e.Subscore)
		t.Fatalf("expected 30.0 (score=%d sub=%d), got %f (score=%d sub=%d)",
			score30, sub30, got, e.Score, e.Subscore)
	}
	if e.BoardMeta.Operator != OperatorSet {
		t.Fatalf("expected operator converted to Set, got %s", e.BoardMeta.Operator)
	}
}

func TestApplyBest_EqualValues(t *testing.T) {
	// current == incoming → result stays the same
	score5, sub5 := encodeScore(t, 5.0)

	e := &StatisticsQueueEntry{
		BoardMeta: LeaderboardMeta{Operator: OperatorBest},
		Score:     score5,
		Subscore:  sub5,
	}

	if err := applyBest(e, 5.0); err != nil {
		t.Fatalf("applyBest: %v", err)
	}

	if e.Score != score5 || e.Subscore != sub5 {
		t.Fatalf("expected unchanged score=%d sub=%d, got score=%d sub=%d",
			score5, sub5, e.Score, e.Subscore)
	}
	if e.BoardMeta.Operator != OperatorSet {
		t.Fatalf("expected operator converted to Set, got %s", e.BoardMeta.Operator)
	}
}

func TestApplyBest_NegativeDecodeError(t *testing.T) {
	// Invalid incoming encoding should return an error
	e := &StatisticsQueueEntry{
		BoardMeta: LeaderboardMeta{Operator: OperatorBest},
		Score:     -1,
		Subscore:  0,
	}

	if err := applyBest(e, 10.0); err == nil {
		t.Fatal("expected error for invalid incoming encoding")
	}
}

// ----- propagateGameCounts tests -----

func TestPropagateGameCounts_ScopedToGroup(t *testing.T) {
	// Two groups: (Arena, AllTime) and (Combat, AllTime)
	// Each has its own GamesPlayed stat with a different count.
	// Count must NOT bleed across groups.

	groupArena := evr.StatisticsGroup{Mode: evr.ModeArenaPublic, ResetSchedule: evr.ResetScheduleAllTime}
	groupCombat := evr.StatisticsGroup{Mode: evr.ModeCombatPublic, ResetSchedule: evr.ResetScheduleAllTime}

	// Build board IDs
	gpArena := StatisticBoardID("group1", evr.ModeArenaPublic, "GamesPlayed", evr.ResetScheduleAllTime)
	winsArena := StatisticBoardID("group1", evr.ModeArenaPublic, "Wins", evr.ResetScheduleAllTime)
	clearsArena := StatisticBoardID("group1", evr.ModeArenaPublic, "Clears", evr.ResetScheduleAllTime)

	gpCombat := StatisticBoardID("group1", evr.ModeCombatPublic, "GamesPlayed", evr.ResetScheduleAllTime)
	killsCombat := StatisticBoardID("group1", evr.ModeCombatPublic, "Kills", evr.ResetScheduleAllTime)

	boardMap := map[string]*evr.StatisticValue{
		gpArena:     {Value: 10, Count: 0},
		winsArena:   {Value: 7, Count: 0},
		clearsArena: {Value: 3, Count: 0},
		gpCombat:    {Value: 5, Count: 0},
		killsCombat: {Value: 20, Count: 0},
	}

	boardIDsByGroup := map[evr.StatisticsGroup][]string{
		groupArena:  {winsArena, clearsArena, gpArena},
		groupCombat: {killsCombat, gpCombat},
	}

	gamesPlayedBoardIDs := map[evr.StatisticsGroup]string{
		groupArena:  gpArena,
		groupCombat: gpCombat,
	}

	statGroups := map[evr.Symbol][]evr.ResetSchedule{
		evr.ModeArenaPublic:  {evr.ResetScheduleAllTime},
		evr.ModeCombatPublic: {evr.ResetScheduleAllTime},
	}

	propagateGameCounts(boardMap, boardIDsByGroup, gamesPlayedBoardIDs, statGroups)

	// Arena group should have Count=10
	if boardMap[winsArena].GetCount() != 10 {
		t.Fatalf("Arena Wins.Count = %d, want 10", boardMap[winsArena].GetCount())
	}
	if boardMap[clearsArena].GetCount() != 10 {
		t.Fatalf("Arena Clears.Count = %d, want 10", boardMap[clearsArena].GetCount())
	}

	// Combat group should have Count=5 (NOT 10 — no bleed)
	if boardMap[killsCombat].GetCount() != 5 {
		t.Fatalf("Combat Kills.Count = %d, want 5", boardMap[killsCombat].GetCount())
	}
}

func TestPropagateGameCounts_RemovesZeroValueBoards(t *testing.T) {
	group := evr.StatisticsGroup{Mode: evr.ModeArenaPublic, ResetSchedule: evr.ResetScheduleAllTime}
	gpID := StatisticBoardID("g", evr.ModeArenaPublic, "GamesPlayed", evr.ResetScheduleAllTime)
	winsID := StatisticBoardID("g", evr.ModeArenaPublic, "Wins", evr.ResetScheduleAllTime)
	zeroID := StatisticBoardID("g", evr.ModeArenaPublic, "ZeroStat", evr.ResetScheduleAllTime)

	boardMap := map[string]*evr.StatisticValue{
		gpID:   {Value: 3, Count: 0},
		winsID: {Value: 5, Count: 0},
		zeroID: {Value: 0, Count: 0},
	}

	boardIDsByGroup := map[evr.StatisticsGroup][]string{
		group: {winsID, zeroID, gpID},
	}

	gamesPlayedBoardIDs := map[evr.StatisticsGroup]string{
		group: gpID,
	}

	statGroups := map[evr.Symbol][]evr.ResetSchedule{
		evr.ModeArenaPublic: {evr.ResetScheduleAllTime},
	}

	propagateGameCounts(boardMap, boardIDsByGroup, gamesPlayedBoardIDs, statGroups)

	// Zero-value board should be removed
	if _, ok := boardMap[zeroID]; ok {
		t.Fatal("zero-value board should have been removed from boardMap")
	}

	// Wins board should still be there with Count=3
	if boardMap[winsID].GetCount() != 3 {
		t.Fatalf("Wins.Count = %d, want 3", boardMap[winsID].GetCount())
	}
}

func TestPropagateGameCounts_NoGamesPlayed(t *testing.T) {
	// If the group doesn't have a GamesPlayed board, nothing should happen
	group := evr.StatisticsGroup{Mode: evr.ModeArenaPublic, ResetSchedule: evr.ResetScheduleAllTime}
	killsID := StatisticBoardID("g", evr.ModeArenaPublic, "Kills", evr.ResetScheduleAllTime)

	boardMap := map[string]*evr.StatisticValue{
		killsID: {Value: 10, Count: 0},
	}

	boardIDsByGroup := map[evr.StatisticsGroup][]string{
		group: {killsID},
	}

	gamesPlayedBoardIDs := map[evr.StatisticsGroup]string{} // empty — no GamesPlayed

	statGroups := map[evr.Symbol][]evr.ResetSchedule{
		evr.ModeArenaPublic: {evr.ResetScheduleAllTime},
	}

	propagateGameCounts(boardMap, boardIDsByGroup, gamesPlayedBoardIDs, statGroups)

	// Kills should still have Count=0 (no propagation without GamesPlayed)
	if boardMap[killsID].GetCount() != 0 {
		t.Fatalf("Kills.Count = %d, want 0 (no GamesPlayed board)", boardMap[killsID].GetCount())
	}
}

// ----- ensureLevelDefaults tests -----

func TestEnsureLevelDefaults_SetsNilLevel(t *testing.T) {
	stats := evr.NewStatistics()

	arena := &evr.ArenaStatistics{}
	combat := &evr.CombatStatistics{}
	stats[evr.StatisticsGroup{Mode: evr.ModeArenaPublic, ResetSchedule: evr.ResetScheduleAllTime}] = arena
	stats[evr.StatisticsGroup{Mode: evr.ModeCombatPublic, ResetSchedule: evr.ResetScheduleAllTime}] = combat

	ensureLevelDefaults(stats)

	if arena.Level == nil {
		t.Fatal("Arena level should not be nil after ensureLevelDefaults")
	}
	if arena.Level.GetCount() != 1 || int64(math.Round(arena.Level.GetValue())) != 1 {
		t.Fatalf("Arena level = (count=%d, val=%f), want (1, 1)", arena.Level.GetCount(), arena.Level.GetValue())
	}

	if combat.Level == nil {
		t.Fatal("Combat level should not be nil after ensureLevelDefaults")
	}
}

func TestEnsureLevelDefaults_DoesNotOverrideExistingLevel(t *testing.T) {
	stats := evr.NewStatistics()

	arena := &evr.ArenaStatistics{
		Level: &evr.StatisticValue{Count: 42, Value: 100.5},
	}
	stats[evr.StatisticsGroup{Mode: evr.ModeArenaPublic, ResetSchedule: evr.ResetScheduleAllTime}] = arena

	ensureLevelDefaults(stats)

	if arena.Level.GetCount() != 42 {
		t.Fatalf("Arena level Count overwritten: got %d, want 42", arena.Level.GetCount())
	}
	if arena.Level.GetValue() != 100.5 {
		t.Fatalf("Arena level Value overwritten: got %f, want 100.5", arena.Level.GetValue())
	}
}

func TestEnsureLevelDefaults_FixesZeroLevel(t *testing.T) {
	stats := evr.NewStatistics()

	arena := &evr.ArenaStatistics{
		Level: &evr.StatisticValue{Count: 0, Value: 0},
	}
	stats[evr.StatisticsGroup{Mode: evr.ModeArenaPublic, ResetSchedule: evr.ResetScheduleAllTime}] = arena

	ensureLevelDefaults(stats)

	if arena.Level.GetCount() != 1 {
		t.Fatalf("Arena zero Count not fixed: got %d, want 1", arena.Level.GetCount())
	}
	if int64(math.Round(arena.Level.GetValue())) != 1 {
		t.Fatalf("Arena zero Value not fixed: got %f, want 1", arena.Level.GetValue())
	}
}

// ----- buildPlayerStatisticsMaps tests -----

func TestBuildPlayerStatisticsMaps_GroupsByModeAndReset(t *testing.T) {
	statGroups := map[evr.Symbol][]evr.ResetSchedule{
		evr.ModeArenaPublic:  {evr.ResetScheduleAllTime, evr.ResetScheduleDaily},
		evr.ModeCombatPublic: {evr.ResetScheduleAllTime},
	}

	_, boardMap, boardIDsByGroup, gamesPlayedBoardIDs, err := buildPlayerStatisticsMaps(statGroups, "group1")
	if err != nil {
		t.Fatalf("buildPlayerStatisticsMaps: %v", err)
	}

	// Should have 3 groups: Arena AllTime, Arena Daily, Combat AllTime
	if len(boardIDsByGroup) != 3 {
		t.Fatalf("expected 3 groups in boardIDsByGroup, got %d", len(boardIDsByGroup))
	}

	// Arena AllTime and Arena Daily should have different boardID sets
	arenaAllTime := evr.StatisticsGroup{Mode: evr.ModeArenaPublic, ResetSchedule: evr.ResetScheduleAllTime}
	arenaDaily := evr.StatisticsGroup{Mode: evr.ModeArenaPublic, ResetSchedule: evr.ResetScheduleDaily}
	combatAllTime := evr.StatisticsGroup{Mode: evr.ModeCombatPublic, ResetSchedule: evr.ResetScheduleAllTime}

	for _, g := range []evr.StatisticsGroup{arenaAllTime, arenaDaily, combatAllTime} {
		if len(boardIDsByGroup[g]) == 0 {
			t.Fatalf("group %v has no board IDs", g)
		}
	}

	// Each group should have a GamesPlayed board tracked
	for _, g := range []evr.StatisticsGroup{arenaAllTime, arenaDaily, combatAllTime} {
		gpID, ok := gamesPlayedBoardIDs[g]
		if !ok {
			t.Fatalf("group %v has no GamesPlayed board tracked", g)
		}
		if _, ok := boardMap[gpID]; !ok {
			t.Fatalf("GamesPlayed board %s not found in boardMap", gpID)
		}
	}

	// Arena AllTime and Arena Daily should have different board IDs for the same stat name
	arenaAllTimeGP := gamesPlayedBoardIDs[arenaAllTime]
	arenaDailyGP := gamesPlayedBoardIDs[arenaDaily]
	if arenaAllTimeGP == arenaDailyGP {
		t.Fatal("Arena AllTime and Daily GamesPlayed board IDs should differ (different reset schedule)")
	}
}

func TestBuildPlayerStatisticsMaps_InitializesNilFields(t *testing.T) {
	statGroups := map[evr.Symbol][]evr.ResetSchedule{
		evr.ModeArenaPublic: {evr.ResetScheduleAllTime},
	}

	playerStats, boardMap, _, _, err := buildPlayerStatisticsMaps(statGroups, "group1")
	if err != nil {
		t.Fatalf("buildPlayerStatisticsMaps: %v", err)
	}

	arena, ok := playerStats[evr.StatisticsGroup{
		Mode: evr.ModeArenaPublic, ResetSchedule: evr.ResetScheduleAllTime,
	}].(*evr.ArenaStatistics)
	if !ok {
		t.Fatal("expected ArenaStatistics")
	}

	// Non-nil pointer fields should have been initialized
	if arena.Level == nil {
		t.Fatal("Level field was not initialized (should have been set to non-nil by the map-building loop)")
	}

	// boardMap should reference the same StatisticValue as the struct field
	levelBoardID := StatisticBoardID("group1", evr.ModeArenaPublic, "Level", evr.ResetScheduleAllTime)
	if boardMap[levelBoardID] != arena.Level {
		t.Fatal("boardMap entry for Level should point to the same StatisticValue as the struct field")
	}
}
