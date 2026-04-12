package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/atomic"
)

const (
	XPProgressionStorageCollection = "Global"
	XPProgressionStorageKey        = "xp_progression"
)

// xpProgressionCache holds the loaded (or default) progression table.
var xpProgressionCache = atomic.NewPointer(DefaultXPProgressionTable())

// XPProgression returns the current cached progression table.
func XPProgression() *XPProgressionTable {
	return xpProgressionCache.Load()
}

// XPProgressionLoad reads the progression table from system user storage.
// If no storage object exists, it writes the default table and caches it.
func XPProgressionLoad(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule) (*XPProgressionTable, error) {
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: XPProgressionStorageCollection,
			Key:        XPProgressionStorageKey,
			UserID:     SystemUserID,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to read xp progression: %w", err)
	}

	table := DefaultXPProgressionTable()

	if len(objs) > 0 {
		if err := json.Unmarshal([]byte(objs[0].Value), table); err != nil {
			return nil, fmt.Errorf("failed to unmarshal xp progression: %w", err)
		}
	} else {
		// Write the default table to storage on first load
		data, err := json.Marshal(table)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal xp progression: %w", err)
		}
		if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
			Collection:      XPProgressionStorageCollection,
			Key:             XPProgressionStorageKey,
			UserID:          SystemUserID,
			PermissionRead:  0,
			PermissionWrite: 0,
			Value:           string(data),
		}}); err != nil {
			return nil, fmt.Errorf("failed to write default xp progression: %w", err)
		}
	}

	xpProgressionCache.Store(table)
	return table, nil
}

// consumeXPIntoLevel converts lifetime total XP into (level, remainder) for
// arena and combat alltime stats. Only the profile sent to clients is modified;
// leaderboard records remain unchanged.
func consumeXPIntoLevel(stats evr.PlayerStatistics) {
	table := XPProgression()

	arenaKey := evr.StatisticsGroup{Mode: evr.ModeArenaPublic, ResetSchedule: evr.ResetScheduleAllTime}
	if s, ok := stats[arenaKey]; ok {
		if arena, ok := s.(*evr.ArenaStatistics); ok && arena.XP != nil {
			level, remainder := table.ComputeLevel(arena.XP.Value)
			arena.Level = &evr.StatisticValue{Count: 1, Value: float64(level)}
			arena.XP = &evr.StatisticValue{Count: arena.XP.Count, Value: remainder}
		}
	}

	combatKey := evr.StatisticsGroup{Mode: evr.ModeCombatPublic, ResetSchedule: evr.ResetScheduleAllTime}
	if s, ok := stats[combatKey]; ok {
		if combat, ok := s.(*evr.CombatStatistics); ok && combat.XP != nil {
			level, remainder := table.ComputeLevel(combat.XP.Value)
			combat.Level = &evr.StatisticValue{Count: 1, Value: float64(level)}
			combat.XP = &evr.StatisticValue{Count: combat.XP.Count, Value: remainder}
		}
	}
}

// XPProgressionTable defines the XP required to advance from each level to the next.
// XPPerLevel[0] is the XP needed to go from level 1 to level 2, etc.
type XPProgressionTable struct {
	MaxLevel   int       `json:"max_level"`
	XPPerLevel []float64 `json:"xp_per_level"`
}

// ComputeLevel walks the progression table and returns (level, remainderXP).
// Level 1 is the starting level (0 XP). Negative XP is treated as 0.
// Players at max level accumulate no further remainder.
func (t *XPProgressionTable) ComputeLevel(totalXP float64) (level int, remainder float64) {
	if totalXP <= 0 {
		return 1, 0
	}

	remaining := totalXP
	for i, required := range t.XPPerLevel {
		if remaining < required {
			return i + 1, remaining
		}
		remaining -= required
	}

	// At or beyond max level
	return t.MaxLevel, 0
}

// DefaultXPProgressionTable returns the EchoVR XP progression table.
// 49 entries for levels 1→2 through 49→50:
//   - Levels 1-9:   500, 1000, 1500, 2000, 2500, 3000, 3500, 4000, 4500
//   - Levels 10-19: 5000 each
//   - Levels 20-29: 7500 each
//   - Levels 30-39: 10000 each
//   - Levels 40-49: 15000 each
func DefaultXPProgressionTable() *XPProgressionTable {
	xp := make([]float64, 0, 49)

	// Levels 1→2 through 9→10: incrementing by 500
	for i := 1; i <= 9; i++ {
		xp = append(xp, float64(i*500))
	}

	// Levels 10→11 through 19→20: 5000 each
	for range 10 {
		xp = append(xp, 5000)
	}

	// Levels 20→21 through 29→30: 7500 each
	for range 10 {
		xp = append(xp, 7500)
	}

	// Levels 30→31 through 39→40: 10000 each
	for range 10 {
		xp = append(xp, 10000)
	}

	// Levels 40→41 through 49→50: 15000 each
	for range 10 {
		xp = append(xp, 15000)
	}

	return &XPProgressionTable{
		MaxLevel:   50,
		XPPerLevel: xp,
	}
}
