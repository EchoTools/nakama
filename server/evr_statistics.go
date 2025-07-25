package server

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/intinig/go-openskill/types"
)

const (
	TabletStatisticIntegerValue = iota
	TabletStatisticFloatValue

	GamesPlayedStatisticID        = "GamesPlayed"
	RankPercentileStatisticID     = "RankPercentile"
	SkillRatingMuStatisticID      = "SkillRatingMu"
	SkillRatingSigmaStatisticID   = "SkillRatingSigma"
	SkillRatingOrdinalStatisticID = "SkillRatingOrdinal"
	LobbyTimeStatisticID          = "LobbyTime"
	GameServerTimeStatisticsID    = "GameServerTime"
	EarlyQuitStatisticID          = "EarlyQuits"

	LeaderboardScoreScalingFactor = float64(1000000000)
)

type LeaderboardOperator string

const (
	OperatorBest      LeaderboardOperator = "best"
	OperatorSet       LeaderboardOperator = "set"
	OperatorIncrement LeaderboardOperator = "incr"
	OperatorDecrement LeaderboardOperator = "decr"
)

var (
	ValidLeaderboardModes = []evr.Symbol{
		evr.ModeCombatPublic,
		evr.ModeArenaPublic,
		evr.ModeCombatPrivate,
		evr.ModeArenaPrivate,
		evr.ModeSocialPublic,
		evr.ModeSocialPrivate,
	}
)

const ()

// Float64ToInt64Pair converts a float64 into a leaderboard score.
// It uses a fixed scaling factor to preserve precision.
func Float64ToScore(f float64) (int64, error) {
	if f < 0 {
		return 0, fmt.Errorf("negative value: %f", f)
	}
	// Limit the float to a maximum of 2^32
	if f > float64(1<<32) {
		return 0, fmt.Errorf("value too large: %f", f)
	}

	// The fraction part is the scaled float
	return int64(f * float64(LeaderboardScoreScalingFactor)), nil
}

// Int64PairToFloat64 converts a leaderboard score into a float64.
func ScoreToFloat64(score int64) float64 {
	if score == 0 {
		return 0
	}
	// The fraction part is the scaled float
	return float64(score) / LeaderboardScoreScalingFactor
}

type LeaderboardMeta struct {
	GroupID       string
	Mode          evr.Symbol
	StatName      string
	Operator      LeaderboardOperator
	ResetSchedule evr.ResetSchedule
}

func (l LeaderboardMeta) ID() string {
	return StatisticBoardID(l.GroupID, l.Mode, l.StatName, l.ResetSchedule)
}

func LeaderboardMetaFromID(id string) (LeaderboardMeta, error) {
	parts := strings.Split(id, ":")
	if len(parts) != 4 {
		return LeaderboardMeta{}, fmt.Errorf("invalid leaderboard ID: %s", id)
	}

	mode := evr.Symbol(evr.ToSymbol(parts[1]))

	return LeaderboardMeta{
		GroupID:       parts[0],
		Mode:          mode,
		StatName:      parts[2],
		ResetSchedule: evr.ResetSchedule(parts[3]),
	}, nil
}

func MatchmakingRatingLoad(ctx context.Context, nk runtime.NakamaModule, userID, groupID string, mode evr.Symbol) (types.Rating, error) {
	// Look for an existing account.

	var sigma, mu float64

	structMap := map[string]*float64{
		SkillRatingMuStatisticID:    &mu,
		SkillRatingSigmaStatisticID: &sigma,
	}

	for statName, ptr := range structMap {
		boardID := StatisticBoardID(groupID, mode, statName, evr.ResetScheduleAllTime)

		_, ownerRecords, _, _, err := nk.LeaderboardRecordsList(ctx, boardID, []string{userID}, 1, "", 0)
		if err != nil {
			return NewDefaultRating(), err
		}

		if len(ownerRecords) == 0 {
			return NewDefaultRating(), nil
		}

		record := ownerRecords[0]
		*ptr = ScoreToFloat64(record.Score)
	}
	return NewRating(0, mu, sigma), nil
}

func MatchmakingRatingStore(ctx context.Context, nk runtime.NakamaModule, userID, discordID, displayName, groupID string, mode evr.Symbol, r types.Rating) error {

	scores := map[string]float64{
		StatisticBoardID(groupID, mode, SkillRatingSigmaStatisticID, "alltime"): r.Sigma,
		StatisticBoardID(groupID, mode, SkillRatingMuStatisticID, "alltime"):    r.Mu,
	}
	metadata := map[string]any{
		"discord_id": discordID,
	}
	for id, value := range scores {
		score, err := Float64ToScore(value)
		if err != nil {
			return fmt.Errorf("failed to convert float64 to int64 pair: %w", err)
		}

		// Write the record
		if _, err := nk.LeaderboardRecordWrite(ctx, id, userID, displayName, score, 0, metadata, nil); err != nil {
			// Try to create the leaderboard
			err = nk.LeaderboardCreate(ctx, id, true, "desc", "set", "", nil, true)
			if err != nil {
				return fmt.Errorf("Leaderboard create error: %w", err)
			} else if _, err := nk.LeaderboardRecordWrite(ctx, id, userID, displayName, score, 0, metadata, nil); err != nil {
				return fmt.Errorf("Leaderboard record write error: %w", err)
			}
		}
	}

	return nil
}

func MatchmakingRankPercentileLoad(ctx context.Context, nk runtime.NakamaModule, userID, groupID string, mode evr.Symbol) (percentile float64, err error) {

	boardID := StatisticBoardID(groupID, mode, RankPercentileStatisticID, "alltime")

	_, records, _, _, err := nk.LeaderboardRecordsList(ctx, boardID, []string{userID}, 10000, "", 0)
	if err != nil {
		return ServiceSettings().Matchmaking.RankPercentile.Default, nil
	}

	if len(records) == 0 {
		return ServiceSettings().Matchmaking.RankPercentile.Default, nil
	}

	return ScoreToFloat64(records[0].Score), nil
}

func MatchmakingRankPercentileStore(ctx context.Context, nk runtime.NakamaModule, userID, username, groupID string, mode evr.Symbol, percentile float64) error {

	id := StatisticBoardID(groupID, mode, RankPercentileStatisticID, "alltime")

	score, err := Float64ToScore(percentile)
	if err != nil {
		return fmt.Errorf("failed to convert float64 to int64 pair: %w", err)
	}

	if score == 0 {
		return nil
	}
	// Write the record
	if _, err := nk.LeaderboardRecordWrite(ctx, id, userID, username, score, 0, nil, nil); err != nil {
		// Try to create the leaderboard
		err = nk.LeaderboardCreate(ctx, id, true, "asc", "set", "", nil, true)

		if err != nil {
			return fmt.Errorf("Leaderboard create error: %w", err)
		} else {
			// Retry the write
			_, err := nk.LeaderboardRecordWrite(ctx, id, userID, username, score, 0, nil, nil)
			if err != nil {
				return fmt.Errorf("Leaderboard record write error: %w", err)
			}
		}
	}

	return nil
}

func StatisticBoardID(groupID string, mode evr.Symbol, statName string, resetSchedule evr.ResetSchedule) string {
	return fmt.Sprintf("%s:%s:%s:%s", groupID, mode.String(), statName, resetSchedule)
}

func ParseStatisticBoardID(id string) (groupID string, mode evr.Symbol, statName string, resetSchedule string, err error) {
	parts := strings.SplitN(id, ":", 4)
	if len(parts) != 4 {
		err = fmt.Errorf("invalid leaderboard ID: %s", id)
		return
	}
	return parts[0], evr.ToSymbol(parts[1]), parts[2], parts[3], nil
}

func ResetScheduleToCron(resetSchedule evr.ResetSchedule) string {
	switch resetSchedule {
	case evr.ResetScheduleDaily:
		return "0 16 * * *"
	case evr.ResetScheduleWeekly:
		return "0 16 * * 4"
	case evr.ResetScheduleAllTime:
		fallthrough
	default:
		return ""
	}
}

func PlayerStatisticsGetID(ctx context.Context, db *sql.DB, nk runtime.NakamaModule, ownerID, groupID string, modes []evr.Symbol, dailyWeeklyStatMode evr.Symbol) (evr.PlayerStatistics, map[string]*evr.StatisticValue, error) {

	startTime := time.Now()

	defer func() {
		nk.MetricsTimerRecord("player_statistics_get_latency", nil, time.Since(startTime))
	}()

	statGroups := make(map[evr.Symbol][]evr.ResetSchedule)

	// Always include Arena and Combat Public stats
	modes = append(modes, evr.ModeCombatPublic, evr.ModeArenaPublic)
	slices.Sort(modes)
	modes = slices.Compact(modes)

	for _, m := range modes {
		if m == evr.Symbol(0) {
			continue
		}
		statGroups[m] = []evr.ResetSchedule{evr.ResetScheduleAllTime}
		if dailyWeeklyStatMode == m {
			statGroups[m] = append(statGroups[m], evr.ResetScheduleDaily, evr.ResetScheduleWeekly)
		}
	}

	playerStatistics := evr.NewStatistics()

	boardMap := make(map[string]*evr.StatisticValue)
	boardIDs := make([]string, 0, len(boardMap))
	gamesPlayedBoardIDs := make(map[evr.StatisticsGroup]string)

	// Build the stats structs
	for m, resetSchedules := range statGroups {
		for _, r := range resetSchedules {

			var stats evr.Statistics
			switch m {
			case evr.ModeCombatPublic:
				stats = &evr.CombatStatistics{}
			case evr.ModeArenaPublic:
				stats = &evr.ArenaStatistics{}
			case evr.ModeCombatPrivate:
				stats = &evr.GenericStats{}
			case evr.ModeArenaPrivate:
				stats = &evr.GenericStats{}
			case evr.ModeSocialPublic:
				stats = &evr.GenericStats{}
			case evr.ModeSocialPrivate:
				stats = &evr.GenericStats{}
			default:
				return nil, nil, fmt.Errorf("invalid mode: %s", m)
			}

			playerStatistics[evr.StatisticsGroup{
				Mode:          m,
				ResetSchedule: r,
			}] = stats

			statsValue := reflect.ValueOf(stats)
			statsType := statsValue.Elem().Type()

			for i := 0; i < statsType.NumField(); i++ {
				fieldType := statsType.Field(i)

				boardID := StatisticBoardID(groupID, m, fieldType.Name, r)
				boardIDs = append(boardIDs, boardID)

				if fieldType.Name == "GamesPlayed" {
					gamesPlayedBoardIDs[evr.StatisticsGroup{
						Mode:          m,
						ResetSchedule: r,
					}] = boardID
				}

				fieldValue := statsValue.Elem().Field(i)
				fieldValue.Set(reflect.New(fieldType.Type.Elem()))
				boardMap[boardID] = fieldValue.Interface().(*evr.StatisticValue)
			}
		}

	}

	query := `
		SELECT 
			lr.leaderboard_id,
			lr.score, 
			lr.subscore
		FROM 
			leaderboard_record lr
		JOIN
			ROWS FROM (
				unnest(
                $2::TEXT[]
				)
			) t(id)
		ON 
			lr.leaderboard_id = t.id
		WHERE 
			lr.owner_id = $1
			AND (lr.expiry_time > NOW() OR lr.expiry_time = '1970-01-01 00:00:00+00') -- Include "alltime" records
			`

	rows, err := db.QueryContext(ctx, query, ownerID, boardIDs)
	if err != nil {
		if err == sql.ErrNoRows {
			// Return the default profile's stats
			return playerStatistics, boardMap, nil
		}
		return nil, nil, fmt.Errorf("failed to query leaderboard records: %w", err)
	}
	defer rows.Close()

	var dbLeaderboardID string
	var dbScore, dbSubscore int64

	for rows.Next() {

		err = rows.Scan(&dbLeaderboardID, &dbScore, &dbSubscore)
		if err != nil {

			return nil, nil, fmt.Errorf("failed to scan leaderboard record: %w", err)
		}

		if dbScore == 0 && dbSubscore == 0 {
			continue
		}

		v, ok := boardMap[dbLeaderboardID]
		if !ok {
			log.Printf("Leaderboard record found for unknown leaderboard ID: %s", dbLeaderboardID)
			continue
		}
		v.SetCount(1)
		v.SetValue(ScoreToFloat64(dbScore))

	}

	// Use the GamesPlayed stat to fill in all the cnt's
	for m, resetSchedules := range statGroups {
		for _, r := range resetSchedules {

			gamesPlayedID := gamesPlayedBoardIDs[evr.StatisticsGroup{
				Mode:          m,
				ResetSchedule: r,
			}]
			if stat, ok := boardMap[gamesPlayedID]; ok {
				for _, boardID := range boardIDs {
					if v, ok := boardMap[boardID]; ok {
						// If the board's value is 0, remove it.
						if v.GetValue() == 0 {
							delete(boardMap, boardID)
							continue
						}
						v.SetCount(int64(stat.GetValue()))
					}
				}
			}
		}
	}

	// Ensure arena level is always set
	if s := playerStatistics[evr.StatisticsGroup{
		Mode:          evr.ModeArenaPublic,
		ResetSchedule: evr.ResetScheduleAllTime,
	}].(*evr.ArenaStatistics); s != nil {
		if s.Level == nil {
			s.Level = &evr.StatisticValue{
				Count: 1,
				Value: 1,
			}
		} else {
			if s.Level.GetCount() <= 0 {
				s.Level.SetCount(1)
			}
			if s.Level.GetValue() <= 0 {
				s.Level.SetValue(1)
			}
		}
	}

	// Ensure combat level is always set
	if s := playerStatistics[evr.StatisticsGroup{
		Mode:          evr.ModeCombatPublic,
		ResetSchedule: evr.ResetScheduleAllTime,
	}].(*evr.CombatStatistics); s != nil {
		if s.Level == nil {
			s.Level = &evr.StatisticValue{
				Count: 1,
				Value: 1,
			}
		} else {
			if s.Level.GetCount() <= 0 {
				s.Level.SetCount(1)
			}
			if s.Level.GetValue() <= 0 {
				s.Level.SetValue(1)
			}
		}
	}

	return playerStatistics, boardMap, nil
}

func NewRating[T int | int64 | float64](z, mu, sigma T) (r types.Rating) {
	if zInt, ok := any(z).(int); ok {
		r.Z = zInt
	} else {
		r.Z = 3
	}
	if muFloat := float64(mu); muFloat > 0 {
		r.Mu = muFloat
	} else {
		r.Mu = 25.0
	}
	if sigmaFloat := float64(sigma); sigmaFloat > 0 {
		r.Sigma = sigmaFloat
	} else {
		r.Sigma = r.Mu / float64(r.Z)
	}
	return r
}
