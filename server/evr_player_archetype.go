package server

import (
	"context"
	"database/sql"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

// Player archetype constants classify play styles based on match statistics.
const (
	ArchetypeStriker     = "striker"
	ArchetypePlaymaker   = "playmaker"
	ArchetypeGoalie      = "goalie"
	ArchetypeInterceptor = "interceptor"
	ArchetypeRookie      = "rookie"
	ArchetypeLowActivity = "low_activity"
)

// ArchetypeStats holds cumulative stat totals used for archetype classification.
type ArchetypeStats struct {
	Goals       float64
	Assists     float64
	Saves       float64
	Steals      float64
	Passes      float64
	ShotsOnGoal float64
}

// DetectArchetype classifies a player into one of 6 play style archetypes
// based on their cumulative match statistics. Classification is deterministic
// and uses simple threshold rules applied in priority order.
//
// Priority order: Rookie > Goalie > Striker > Playmaker > Interceptor > LowActivity
func DetectArchetype(stats ArchetypeStats, gamesPlayed int, newPlayerThreshold int) string {
	if gamesPlayed < newPlayerThreshold {
		return ArchetypeRookie
	}

	gp := float64(gamesPlayed)

	// save_focus = saves / (saves + goals + assists)
	saveDenom := stats.Saves + stats.Goals + stats.Assists
	if saveDenom > 0 {
		saveFocus := stats.Saves / saveDenom
		if saveFocus > 0.5 {
			return ArchetypeGoalie
		}
	}

	// goals/game > 1.0 AND shots/game > 2.0
	if gp > 0 && stats.Goals/gp > 1.0 && stats.ShotsOnGoal/gp > 2.0 {
		return ArchetypeStriker
	}

	// assists/game > 0.4 AND passes/game > 2.0
	if gp > 0 && stats.Assists/gp > 0.4 && stats.Passes/gp > 2.0 {
		return ArchetypePlaymaker
	}

	// steals/game > 0.6
	if gp > 0 && stats.Steals/gp > 0.6 {
		return ArchetypeInterceptor
	}

	return ArchetypeLowActivity
}

// LoadArchetypeStats reads the player's cumulative arena statistics from
// leaderboard records and returns the stats needed for archetype detection
// along with the total games played.
func LoadArchetypeStats(ctx context.Context, db *sql.DB, logger *zap.Logger, userID, groupID string) (ArchetypeStats, int, error) {
	statNames := []string{"Goals", "Assists", "Saves", "Steals", "Passes", "ShotsOnGoal", "ArenaWins", "ArenaLosses"}
	mode := evr.ModeArenaPublic
	resetSchedule := evr.ResetScheduleAllTime

	boardIDs := make([]string, len(statNames))
	for i, name := range statNames {
		boardIDs[i] = StatisticBoardID(groupID, mode, name, resetSchedule)
	}

	query := `
		SELECT
			lr.leaderboard_id,
			lr.score,
			lr.subscore
		FROM
			leaderboard_record lr
		WHERE
			lr.owner_id = $1
			AND lr.leaderboard_id = ANY($2)
			AND (lr.expiry_time > NOW() OR lr.expiry_time = '1970-01-01 00:00:00+00')
	`

	rows, err := db.QueryContext(ctx, query, userID, boardIDs)
	if err != nil {
		return ArchetypeStats{}, 0, err
	}
	defer rows.Close()

	values := make(map[string]float64)
	for rows.Next() {
		var boardID string
		var score, subscore int64
		if err := rows.Scan(&boardID, &score, &subscore); err != nil {
			logger.Warn("Failed to scan archetype stat", zap.Error(err))
			continue
		}
		if score == 0 && subscore == 0 {
			continue
		}
		val, err := ScoreToFloat64(score, subscore)
		if err != nil {
			logger.Warn("Failed to decode archetype stat score", zap.String("board_id", boardID), zap.Error(err))
			continue
		}

		// Extract stat name from board ID: groupID:mode:statName:resetSchedule
		meta, err := LeaderboardMetaFromID(boardID)
		if err != nil {
			continue
		}
		values[meta.StatName] = val
	}
	if err := rows.Err(); err != nil {
		return ArchetypeStats{}, 0, err
	}

	stats := ArchetypeStats{
		Goals:       values["Goals"],
		Assists:     values["Assists"],
		Saves:       values["Saves"],
		Steals:      values["Steals"],
		Passes:      values["Passes"],
		ShotsOnGoal: values["ShotsOnGoal"],
	}

	gamesPlayed := int(values["ArenaWins"] + values["ArenaLosses"])

	return stats, gamesPlayed, nil
}
